package relay

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/secrets"
)

func TestRecoveryEnvelopeRoundTripUsesConstantTimeStoreVerifier(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	values, err := secrets.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, values.RecoveryKey)
	if err != nil {
		t.Fatalf("SealRecoveryEnvelope: %v", err)
	}
	if err := VerifyRecoveryEnvelope(identity, envelope, context, values.VerifyRecoveryKey); err != nil {
		t.Fatalf("VerifyRecoveryEnvelope: %v", err)
	}

	wrongEnvelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, "test-only-wrong-recovery-material")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryEnvelope(identity, wrongEnvelope, context, values.VerifyRecoveryKey); !errors.Is(err, ErrRecoveryKeyRejected) {
		t.Fatalf("wrong recovery material error = %v, want ErrRecoveryKeyRejected", err)
	}
}

func TestRecoveryEnvelopeCiphertextAndMetadataOmitPlaintext(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	plaintext := "test-only-recovery-material-that-must-not-appear"
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), plaintext) || strings.Contains(envelope.Ciphertext, plaintext) {
		t.Fatalf("recovery envelope exposed plaintext: %s", encoded)
	}
	if envelope.Version != ProtocolVersion || envelope.Algorithm != RecoveryEnvelopeAlgorithm {
		t.Fatalf("unversioned recovery envelope: %#v", envelope)
	}
}

func TestRecoveryEnvelopeAADBindsEveryContextField(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*RecoveryEnvelope, *RecoveryContext)
	}{
		{name: "protocol version", mutate: func(envelope *RecoveryEnvelope, _ *RecoveryContext) { envelope.Version++ }},
		{name: "device ID", mutate: func(envelope *RecoveryEnvelope, expected *RecoveryContext) {
			envelope.DeviceID = "other-device"
			expected.DeviceID = "other-device"
		}},
		{name: "access subject", mutate: func(envelope *RecoveryEnvelope, expected *RecoveryContext) {
			envelope.AccessSubject = "other-access"
			expected.AccessSubject = "other-access"
		}},
		{name: "browser ID", mutate: func(envelope *RecoveryEnvelope, expected *RecoveryContext) {
			envelope.BrowserID = "other-browser"
			expected.BrowserID = "other-browser"
		}},
		{name: "credential generation", mutate: func(envelope *RecoveryEnvelope, expected *RecoveryContext) {
			envelope.Generation++
			expected.Generation++
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := envelope
			expected := context
			test.mutate(&tampered, &expected)
			called := false
			err := VerifyRecoveryEnvelope(identity, tampered, expected, func(string) bool {
				called = true
				return true
			})
			if err == nil {
				t.Fatal("context substitution unexpectedly verified")
			}
			if called {
				t.Fatal("verifier received plaintext after AAD authentication failed")
			}
		})
	}
}

func TestRecoveryEnvelopeRejectsWrongExpectedContextBeforeDecrypting(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}

	tests := []RecoveryContext{
		{DeviceID: "wrong", AccessSubject: context.AccessSubject, BrowserID: context.BrowserID, Generation: context.Generation},
		{DeviceID: context.DeviceID, AccessSubject: "wrong", BrowserID: context.BrowserID, Generation: context.Generation},
		{DeviceID: context.DeviceID, AccessSubject: context.AccessSubject, BrowserID: "wrong", Generation: context.Generation},
		{DeviceID: context.DeviceID, AccessSubject: context.AccessSubject, BrowserID: context.BrowserID, Generation: context.Generation + 1},
	}
	for _, expected := range tests {
		called := false
		err := VerifyRecoveryEnvelope(identity, envelope, expected, func(string) bool {
			called = true
			return true
		})
		if !errors.Is(err, ErrRecoveryContextMismatch) {
			t.Fatalf("wrong context error = %v, want ErrRecoveryContextMismatch", err)
		}
		if called {
			t.Fatal("verifier called for wrong context")
		}
	}
}

func TestRecoveryEnvelopeRejectsTamperingAndMalformedFieldsWithoutLeakingContent(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	plaintext := "test-only-sensitive-marker"
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	tests := []RecoveryEnvelope{
		func() RecoveryEnvelope { value := envelope; value.Algorithm = "unknown"; return value }(),
		func() RecoveryEnvelope { value := envelope; value.EphemeralPublicKey.X = "bad"; return value }(),
		func() RecoveryEnvelope { value := envelope; value.Salt = "bad"; return value }(),
		func() RecoveryEnvelope { value := envelope; value.Nonce = "bad"; return value }(),
		func() RecoveryEnvelope { value := envelope; value.Ciphertext = "bad"; return value }(),
		func() RecoveryEnvelope {
			value := envelope
			value.Ciphertext = flipBase64URLCharacter(value.Ciphertext)
			return value
		}(),
	}
	for _, tampered := range tests {
		called := false
		err := VerifyRecoveryEnvelope(identity, tampered, context, func(string) bool {
			called = true
			return true
		})
		if err == nil {
			t.Fatalf("tampered envelope verified: %#v", tampered)
		}
		if called {
			t.Fatal("verifier called for unauthenticated envelope")
		}
		if strings.Contains(err.Error(), plaintext) || strings.Contains(err.Error(), envelope.Ciphertext) {
			t.Fatalf("error exposed recovery material: %v", err)
		}
	}
}

func recoveryContextFixture() RecoveryContext {
	return RecoveryContext{
		DeviceID:      "device-1",
		AccessSubject: "access-1",
		BrowserID:     "browser-1",
		Generation:    7,
	}
}

func flipBase64URLCharacter(value string) string {
	if value[0] == 'A' {
		return "B" + value[1:]
	}
	return "A" + value[1:]
}

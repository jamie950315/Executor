package relay

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/secrets"
)

func TestParseRecoveryEnvelopeStrictWireRoundTrip(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	want, err := SealRecoveryEnvelope(identity.PublicJWK(), context, "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecoveryEnvelope(encoded)
	if err != nil {
		t.Fatalf("ParseRecoveryEnvelope: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed envelope = %#v, want %#v", got, want)
	}
}

func TestParseRecoveryEnvelopeRejectsUnknownTrailingNullAndMissingFields(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	want, err := SealRecoveryEnvelope(identity.PublicJWK(), recoveryContextFixture(), "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}
	validJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	secretMarker := "test-only-sensitive-marker"
	inputs := [][]byte{
		[]byte("null"),
		append(append([]byte(nil), validJSON...), []byte(` {"extra":"`+secretMarker+`"}`)...),
	}
	var object map[string]any
	if err := json.Unmarshal(validJSON, &object); err != nil {
		t.Fatal(err)
	}
	withUnknown := cloneJSONMap(t, object)
	withUnknown["unexpected"] = secretMarker
	inputs = append(inputs, marshalJSONForTest(t, withUnknown))
	withNestedUnknown := cloneJSONMap(t, object)
	publicKey := withNestedUnknown["ephemeral_public_key"].(map[string]any)
	publicKey["unexpected"] = secretMarker
	inputs = append(inputs, marshalJSONForTest(t, withNestedUnknown))

	for _, required := range []string{
		"version", "algorithm", "device_id", "access_subject", "browser_id", "generation",
		"ephemeral_public_key", "salt", "nonce", "ciphertext",
	} {
		missing := cloneJSONMap(t, object)
		delete(missing, required)
		inputs = append(inputs, marshalJSONForTest(t, missing))
	}

	for _, input := range inputs {
		if _, err := ParseRecoveryEnvelope(input); err == nil {
			t.Fatalf("ParseRecoveryEnvelope accepted invalid JSON: %s", input)
		} else if strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), want.Ciphertext) {
			t.Fatalf("error exposed recovery envelope content: %v", err)
		}
	}
}

func TestDecodeRecoveryUnlockPayloadAppliesNestedStrictParser(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recoveryEnvelope, err := SealRecoveryEnvelope(identity.PublicJWK(), recoveryContextFixture(), "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}
	validJSON, err := json.Marshal(recoveryEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	secretMarker := "test-only-sensitive-marker"
	nestedUnknown := strings.TrimSuffix(string(validJSON), "}") + `,"unexpected":"` + secretMarker + `"}`

	for _, payload := range []string{
		`{"envelope":null}`,
		`{"envelope":{"version":1}}`,
		`{"envelope":` + nestedUnknown + `}`,
	} {
		envelope, err := NewEnvelope(MessageTypeRecoveryUnlock, "msg-1", json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePayload[RecoveryUnlockPayload](envelope); err == nil {
			t.Fatalf("DecodePayload accepted invalid nested recovery envelope: %s", payload)
		} else if strings.Contains(err.Error(), secretMarker) {
			t.Fatalf("error exposed nested input: %v", err)
		}
	}
}

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

func TestRecoveryEnvelopeRejectsDifferentDevicePrivateKeyBeforeVerification(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := recoveryContextFixture()
	envelope, err := SealRecoveryEnvelope(identity.PublicJWK(), context, "test-only-recovery-material")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = VerifyRecoveryEnvelope(otherIdentity, envelope, context, func(string) bool {
		called = true
		return true
	})
	if !errors.Is(err, ErrInvalidRecoveryEnvelope) {
		t.Fatalf("wrong device identity error = %v, want ErrInvalidRecoveryEnvelope", err)
	}
	if called {
		t.Fatal("verifier called after decryption with the wrong device identity")
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

func cloneJSONMap(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	encoded := marshalJSONForTest(t, source)
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func marshalJSONForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

package relay

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type wireVectors struct {
	ProtocolVersion          uint16        `json:"protocol_version"`
	TestOnlyDevicePrivateKey PrivateKeyJWK `json:"test_only_device_private_key"`
	DevicePublicKey          PublicKeyJWK  `json:"device_public_key"`
	Recovery                 struct {
		Context                     RecoveryContext  `json:"context"`
		TestOnlyPlaintext           string           `json:"test_only_plaintext"`
		TestOnlyEphemeralPrivateKey PrivateKeyJWK    `json:"test_only_ephemeral_private_key"`
		Salt                        string           `json:"salt"`
		Nonce                       string           `json:"nonce"`
		AAD                         string           `json:"aad"`
		ExpectedEnvelope            RecoveryEnvelope `json:"expected_envelope"`
	} `json:"recovery"`
	Grant struct {
		Claims     GrantClaims `json:"claims"`
		CompactJWS string      `json:"compact_jws"`
	} `json:"grant"`
}

func TestCrossLanguageWireVectors(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/wire-vectors.json")
	if err != nil {
		t.Fatalf("read wire vectors: %v", err)
	}
	var vectors wireVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode wire vectors: %v", err)
	}
	if vectors.ProtocolVersion != ProtocolVersion {
		t.Fatalf("vector protocol version = %d, want %d", vectors.ProtocolVersion, ProtocolVersion)
	}

	privateJSON, err := json.Marshal(vectors.TestOnlyDevicePrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ParseDeviceIdentity(privateJSON)
	if err != nil {
		t.Fatalf("parse vector device identity: %v", err)
	}
	if got := identity.PublicJWK(); !reflect.DeepEqual(got, vectors.DevicePublicKey) {
		t.Fatalf("vector public key = %#v, derived %#v", vectors.DevicePublicKey, got)
	}

	ephemeralPrivate, err := base64.RawURLEncoding.Strict().DecodeString(vectors.Recovery.TestOnlyEphemeralPrivateKey.D)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := base64.RawURLEncoding.Strict().DecodeString(vectors.Recovery.Salt)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(vectors.Recovery.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealRecoveryEnvelopeWithMaterial(
		vectors.DevicePublicKey,
		vectors.Recovery.Context,
		vectors.Recovery.TestOnlyPlaintext,
		ephemeralPrivate,
		salt,
		nonce,
	)
	if err != nil {
		t.Fatalf("seal vector recovery envelope: %v", err)
	}
	if !reflect.DeepEqual(envelope, vectors.Recovery.ExpectedEnvelope) {
		t.Fatalf("recovery vector = %#v, want %#v", envelope, vectors.Recovery.ExpectedEnvelope)
	}
	if got := string(recoveryAdditionalData(vectors.Recovery.Context)); got != vectors.Recovery.AAD {
		t.Fatalf("recovery AAD = %s, want %s", got, vectors.Recovery.AAD)
	}
	if err := VerifyRecoveryEnvelope(identity, vectors.Recovery.ExpectedEnvelope, vectors.Recovery.Context, func(candidate string) bool {
		return candidate == vectors.Recovery.TestOnlyPlaintext
	}); err != nil {
		t.Fatalf("verify recovery vector: %v", err)
	}

	claims, err := VerifyDeviceGrant(vectors.DevicePublicKey, vectors.Grant.CompactJWS, GrantExpectation{
		DeviceID:      vectors.Grant.Claims.DeviceID,
		AccessSubject: vectors.Grant.Claims.AccessSubject,
		BrowserID:     vectors.Grant.Claims.BrowserID,
		Generation:    vectors.Grant.Claims.Generation,
		Now:           time.Unix(vectors.Grant.Claims.IssuedAt, 0),
	})
	if err != nil {
		t.Fatalf("verify compact JWS vector: %v", err)
	}
	if claims != vectors.Grant.Claims {
		t.Fatalf("grant vector claims = %#v, want %#v", claims, vectors.Grant.Claims)
	}
}

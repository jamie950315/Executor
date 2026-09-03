package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type wireVectors struct {
	ProtocolVersion          uint16                          `json:"protocol_version"`
	TestOnlyDevicePrivateKey PrivateKeyJWK                   `json:"test_only_device_private_key"`
	DevicePublicKey          PublicKeyJWK                    `json:"device_public_key"`
	Messages                 map[MessageType]json.RawMessage `json:"messages"`
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

func TestCrossLanguageMessageWireVectors(t *testing.T) {
	t.Parallel()

	vectors := loadWireVectors(t)
	expectedTypes := []MessageType{
		MessageTypeVersionNegotiation,
		MessageTypeEnrollment,
		MessageTypeHeartbeat,
		MessageTypeRequest,
		MessageTypeResponse,
		MessageTypeStreamChunk,
		MessageTypeCancellation,
		MessageTypeRecoveryUnlock,
		MessageTypeGrantVerification,
	}
	if len(vectors.Messages) != len(expectedTypes) {
		t.Fatalf("message vector count = %d, want %d", len(vectors.Messages), len(expectedTypes))
	}

	for _, messageType := range expectedTypes {
		t.Run(string(messageType), func(t *testing.T) {
			wireJSON, ok := vectors.Messages[messageType]
			if !ok {
				t.Fatalf("missing %s vector", messageType)
			}
			envelope, err := DecodeEnvelope(wireJSON)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if envelope.Type != messageType {
				t.Fatalf("envelope type = %q, want %q", envelope.Type, messageType)
			}

			var payload any
			switch messageType {
			case MessageTypeVersionNegotiation:
				decoded := decodeVectorPayload[VersionNegotiationPayload](t, envelope)
				if !reflect.DeepEqual(decoded.SupportedVersions, []uint16{1}) {
					t.Fatalf("supported versions = %#v", decoded.SupportedVersions)
				}
				payload = decoded
			case MessageTypeEnrollment:
				decoded := decodeVectorPayload[EnrollmentPayload](t, envelope)
				if decoded.DeviceID != "device-vector-1" || decoded.AccessSubject != "access-vector-1" ||
					decoded.BrowserID != "browser-vector-1" || decoded.Generation != 7 ||
					!reflect.DeepEqual(decoded.DevicePublicKey, vectors.DevicePublicKey) {
					t.Fatalf("enrollment payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeHeartbeat:
				decoded := decodeVectorPayload[HeartbeatPayload](t, envelope)
				if decoded.DeviceID != "device-vector-1" || decoded.Generation != 7 || decoded.SentAt != 1_700_000_000 {
					t.Fatalf("heartbeat payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeRequest:
				decoded := decodeVectorPayload[RequestPayload](t, envelope)
				if decoded.RequestID != "request-vector-1" || decoded.Method != "device.status" || compactJSONForTest(t, decoded.Arguments) != `{"verbose":true,"limit":3}` {
					t.Fatalf("request payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeResponse:
				decoded := decodeVectorPayload[ResponsePayload](t, envelope)
				if decoded.RequestID != "request-vector-1" || compactJSONForTest(t, decoded.Result) != `{"ready":true,"generation":7}` || decoded.Failure != nil {
					t.Fatalf("response payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeStreamChunk:
				decoded := decodeVectorPayload[StreamChunkPayload](t, envelope)
				if decoded.RequestID != "request-vector-1" || decoded.Sequence != 2 || string(decoded.Data) != "stream-vector" || !decoded.Final {
					t.Fatalf("stream chunk payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeCancellation:
				decoded := decodeVectorPayload[CancellationPayload](t, envelope)
				if decoded.RequestID != "request-vector-1" {
					t.Fatalf("cancellation payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeRecoveryUnlock:
				decoded := decodeVectorPayload[RecoveryUnlockPayload](t, envelope)
				if !reflect.DeepEqual(decoded.Envelope, vectors.Recovery.ExpectedEnvelope) {
					t.Fatalf("recovery unlock payload = %#v", decoded)
				}
				payload = decoded
			case MessageTypeGrantVerification:
				decoded := decodeVectorPayload[GrantVerificationPayload](t, envelope)
				if decoded.Grant != vectors.Grant.CompactJWS {
					t.Fatalf("grant verification payload = %#v", decoded)
				}
				payload = decoded
			default:
				t.Fatalf("unhandled message type %q", messageType)
			}

			rebuilt, err := NewEnvelope(messageType, envelope.MessageID, payload)
			if err != nil {
				t.Fatalf("NewEnvelope: %v", err)
			}
			encoded, err := json.Marshal(rebuilt)
			if err != nil {
				t.Fatalf("Marshal rebuilt envelope: %v", err)
			}
			if got, want := string(encoded), compactJSONForTest(t, wireJSON); got != want {
				t.Fatalf("canonical wire JSON = %s, want %s", got, want)
			}
		})
	}
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

func loadWireVectors(t *testing.T) wireVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/wire-vectors.json")
	if err != nil {
		t.Fatalf("read wire vectors: %v", err)
	}
	var vectors wireVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode wire vectors: %v", err)
	}
	return vectors
}

func decodeVectorPayload[T any](t *testing.T, envelope Envelope) T {
	t.Helper()
	payload, err := DecodePayload[T](envelope)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	return payload
}

func compactJSONForTest(t *testing.T, value []byte) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

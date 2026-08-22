package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeWireShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		messageType MessageType
		payload     any
		want        string
	}{
		{
			name:        "version negotiation",
			messageType: MessageTypeVersionNegotiation,
			payload:     VersionNegotiationPayload{SupportedVersions: []uint16{1}},
			want:        `{"version":1,"type":"version_negotiation","message_id":"msg-1","payload":{"supported_versions":[1]}}`,
		},
		{
			name:        "enrollment",
			messageType: MessageTypeEnrollment,
			payload: EnrollmentPayload{
				DeviceID: "device-1", AccessSubject: "access-1", BrowserID: "browser-1", Generation: 7,
				DevicePublicKey: PublicKeyJWK{KeyType: "EC", Curve: "P-256", X: "eA", Y: "eQ"},
			},
			want: `{"version":1,"type":"enrollment","message_id":"msg-1","payload":{"device_id":"device-1","access_subject":"access-1","browser_id":"browser-1","generation":7,"device_public_key":{"kty":"EC","crv":"P-256","x":"eA","y":"eQ"}}}`,
		},
		{
			name:        "heartbeat",
			messageType: MessageTypeHeartbeat,
			payload:     HeartbeatPayload{DeviceID: "device-1", Generation: 7, SentAt: 1_700_000_000},
			want:        `{"version":1,"type":"heartbeat","message_id":"msg-1","payload":{"device_id":"device-1","generation":7,"sent_at":1700000000}}`,
		},
		{
			name:        "request",
			messageType: MessageTypeRequest,
			payload:     RequestPayload{RequestID: "req-1", Method: "device.status", Arguments: json.RawMessage(`{"verbose":true}`)},
			want:        `{"version":1,"type":"request","message_id":"msg-1","payload":{"request_id":"req-1","method":"device.status","arguments":{"verbose":true}}}`,
		},
		{
			name:        "response",
			messageType: MessageTypeResponse,
			payload:     ResponsePayload{RequestID: "req-1", Result: json.RawMessage(`{"ready":true}`)},
			want:        `{"version":1,"type":"response","message_id":"msg-1","payload":{"request_id":"req-1","result":{"ready":true}}}`,
		},
		{
			name:        "stream chunk",
			messageType: MessageTypeStreamChunk,
			payload:     StreamChunkPayload{RequestID: "req-1", Sequence: 2, Data: []byte("chunk"), Final: true},
			want:        `{"version":1,"type":"stream_chunk","message_id":"msg-1","payload":{"request_id":"req-1","sequence":2,"data":"Y2h1bms=","final":true}}`,
		},
		{
			name:        "cancellation",
			messageType: MessageTypeCancellation,
			payload:     CancellationPayload{RequestID: "req-1"},
			want:        `{"version":1,"type":"cancellation","message_id":"msg-1","payload":{"request_id":"req-1"}}`,
		},
		{
			name:        "recovery unlock",
			messageType: MessageTypeRecoveryUnlock,
			payload:     RecoveryUnlockPayload{Envelope: json.RawMessage(`{"version":1,"ciphertext":"fixture"}`)},
			want:        `{"version":1,"type":"recovery_unlock","message_id":"msg-1","payload":{"envelope":{"version":1,"ciphertext":"fixture"}}}`,
		},
		{
			name:        "grant verification",
			messageType: MessageTypeGrantVerification,
			payload:     GrantVerificationPayload{Grant: "fixture.compact.jws"},
			want:        `{"version":1,"type":"grant_verification","message_id":"msg-1","payload":{"grant":"fixture.compact.jws"}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := NewEnvelope(test.messageType, "msg-1", test.payload)
			if err != nil {
				t.Fatalf("NewEnvelope: %v", err)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if got := string(encoded); got != test.want {
				t.Fatalf("wire JSON = %s, want %s", got, test.want)
			}

			decoded, err := DecodeEnvelope(encoded)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if decoded.Version != ProtocolVersion || decoded.Type != test.messageType || decoded.MessageID != "msg-1" {
				t.Fatalf("decoded metadata = %#v", decoded)
			}
		})
	}
}

func TestDecodeEnvelopeRejectsInvalidMetadataWithoutEchoingInput(t *testing.T) {
	t.Parallel()

	secretMarker := "test-only-sensitive-marker"
	tests := []string{
		`{"version":2,"type":"heartbeat","message_id":"` + secretMarker + `","payload":{}}`,
		`{"version":1,"type":"unknown","message_id":"` + secretMarker + `","payload":{}}`,
		`{"version":1,"type":"heartbeat","message_id":"","payload":{}}`,
		`{"version":1,"type":"heartbeat","message_id":"msg-1","payload":{},"unexpected":"` + secretMarker + `"}`,
		`{"version":1,"type":"heartbeat","message_id":"msg-1","payload":{}} {"extra":"` + secretMarker + `"}`,
	}
	for _, input := range tests {
		if _, err := DecodeEnvelope([]byte(input)); err == nil {
			t.Fatalf("DecodeEnvelope accepted %s", input)
		} else if strings.Contains(err.Error(), secretMarker) {
			t.Fatalf("error exposed input content: %v", err)
		}
	}
}

func TestDecodePayloadRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	envelope, err := DecodeEnvelope([]byte(`{"version":1,"type":"heartbeat","message_id":"msg-1","payload":{"device_id":"device-1","generation":7,"sent_at":1700000000,"extra":true}}`))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if _, err := DecodePayload[HeartbeatPayload](envelope); err == nil {
		t.Fatal("DecodePayload accepted an unknown field")
	}
}

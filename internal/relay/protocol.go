package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const ProtocolVersion uint16 = 1

var ErrInvalidEnvelope = errors.New("invalid relay envelope")

type MessageType string

const (
	MessageTypeVersionNegotiation MessageType = "version_negotiation"
	MessageTypeEnrollment         MessageType = "enrollment"
	MessageTypeHeartbeat          MessageType = "heartbeat"
	MessageTypeRequest            MessageType = "request"
	MessageTypeResponse           MessageType = "response"
	MessageTypeStreamChunk        MessageType = "stream_chunk"
	MessageTypeCancellation       MessageType = "cancellation"
	MessageTypeRecoveryUnlock     MessageType = "recovery_unlock"
	MessageTypeGrantVerification  MessageType = "grant_verification"
)

type Envelope struct {
	Version   uint16          `json:"version"`
	Type      MessageType     `json:"type"`
	MessageID string          `json:"message_id"`
	Payload   json.RawMessage `json:"payload"`
}

type VersionNegotiationPayload struct {
	SupportedVersions []uint16 `json:"supported_versions"`
}

type PublicKeyJWK struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	X       string `json:"x"`
	Y       string `json:"y"`
}

type EnrollmentPayload struct {
	DeviceID        string       `json:"device_id"`
	AccessSubject   string       `json:"access_subject"`
	BrowserID       string       `json:"browser_id"`
	Generation      uint64       `json:"generation"`
	DevicePublicKey PublicKeyJWK `json:"device_public_key"`
}

type HeartbeatPayload struct {
	DeviceID   string `json:"device_id"`
	Generation uint64 `json:"generation"`
	SentAt     int64  `json:"sent_at"`
}

type RequestPayload struct {
	RequestID string          `json:"request_id"`
	Method    string          `json:"method"`
	Arguments json.RawMessage `json:"arguments"`
}

type ResponseFailure struct {
	Code string `json:"code"`
}

type ResponsePayload struct {
	RequestID string           `json:"request_id"`
	Result    json.RawMessage  `json:"result,omitempty"`
	Failure   *ResponseFailure `json:"failure,omitempty"`
}

type StreamChunkPayload struct {
	RequestID string `json:"request_id"`
	Sequence  uint64 `json:"sequence"`
	Data      []byte `json:"data"`
	Final     bool   `json:"final"`
}

type CancellationPayload struct {
	RequestID string `json:"request_id"`
}

type RecoveryUnlockPayload struct {
	Envelope json.RawMessage `json:"envelope"`
}

type GrantVerificationPayload struct {
	Grant string `json:"grant"`
}

func NewEnvelope(messageType MessageType, messageID string, payload any) (Envelope, error) {
	if !validMessageType(messageType) || messageID == "" {
		return Envelope{}, ErrInvalidEnvelope
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	if len(encoded) == 0 || bytes.Equal(encoded, []byte("null")) {
		return Envelope{}, ErrInvalidEnvelope
	}
	return Envelope{
		Version:   ProtocolVersion,
		Type:      messageType,
		MessageID: messageID,
		Payload:   encoded,
	}, nil
}

func DecodeEnvelope(data []byte) (Envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	if envelope.Version != ProtocolVersion || !validMessageType(envelope.Type) || envelope.MessageID == "" || len(envelope.Payload) == 0 {
		return Envelope{}, ErrInvalidEnvelope
	}
	return envelope, nil
}

func DecodePayload[T any](envelope Envelope) (T, error) {
	var payload T
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, errors.New("invalid relay payload")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return payload, errors.New("invalid relay payload")
	}
	return payload, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidEnvelope
	}
	return nil
}

func validMessageType(messageType MessageType) bool {
	switch messageType {
	case MessageTypeVersionNegotiation,
		MessageTypeEnrollment,
		MessageTypeHeartbeat,
		MessageTypeRequest,
		MessageTypeResponse,
		MessageTypeStreamChunk,
		MessageTypeCancellation,
		MessageTypeRecoveryUnlock,
		MessageTypeGrantVerification:
		return true
	default:
		return false
	}
}

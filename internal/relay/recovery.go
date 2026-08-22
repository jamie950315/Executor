package relay

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

const RecoveryEnvelopeAlgorithm = "ECDH-P256+HKDF-SHA256+A256GCM"

const recoveryHKDFInfo = "executor/recovery-envelope/v1"

var (
	ErrInvalidRecoveryEnvelope = errors.New("invalid recovery envelope")
	ErrRecoveryContextMismatch = errors.New("recovery envelope context mismatch")
	ErrRecoveryKeyRejected     = errors.New("recovery key rejected")
)

type RecoveryContext struct {
	DeviceID      string `json:"device_id"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
	Generation    uint64 `json:"generation"`
}

type RecoveryEnvelope struct {
	Version            uint16       `json:"version"`
	Algorithm          string       `json:"algorithm"`
	DeviceID           string       `json:"device_id"`
	AccessSubject      string       `json:"access_subject"`
	BrowserID          string       `json:"browser_id"`
	Generation         uint64       `json:"generation"`
	EphemeralPublicKey PublicKeyJWK `json:"ephemeral_public_key"`
	Salt               string       `json:"salt"`
	Nonce              string       `json:"nonce"`
	Ciphertext         string       `json:"ciphertext"`
}

type recoveryEnvelopeWire RecoveryEnvelope

type recoveryAAD struct {
	Version       uint16 `json:"version"`
	Algorithm     string `json:"algorithm"`
	DeviceID      string `json:"device_id"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
	Generation    uint64 `json:"generation"`
}

func SealRecoveryEnvelope(devicePublicKey PublicKeyJWK, context RecoveryContext, recoveryKey string) (RecoveryEnvelope, error) {
	if recoveryKey == "" || !validRecoveryContext(context) {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	ephemeralPrivate := ephemeral.Bytes()
	defer clear(ephemeralPrivate)

	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	defer clear(salt)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	defer clear(nonce)
	return sealRecoveryEnvelopeWithMaterial(devicePublicKey, context, recoveryKey, ephemeralPrivate, salt, nonce)
}

func ParseRecoveryEnvelope(data []byte) (RecoveryEnvelope, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire recoveryEnvelopeWire
	if err := decoder.Decode(&wire); err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	if err := requireJSONEOF(decoder); err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	envelope := RecoveryEnvelope(wire)
	if err := validateRecoveryEnvelope(envelope); err != nil {
		return RecoveryEnvelope{}, err
	}
	return envelope, nil
}

func (envelope *RecoveryEnvelope) UnmarshalJSON(data []byte) error {
	parsed, err := ParseRecoveryEnvelope(data)
	if err != nil {
		return err
	}
	*envelope = parsed
	return nil
}

func VerifyRecoveryEnvelope(identity *DeviceIdentity, envelope RecoveryEnvelope, expected RecoveryContext, verifier func(string) bool) error {
	if !validPrivateKey(identity) || verifier == nil || !validRecoveryContext(expected) {
		return ErrInvalidRecoveryEnvelope
	}
	if err := validateRecoveryEnvelope(envelope); err != nil {
		return err
	}
	if envelope.context() != expected {
		return ErrRecoveryContextMismatch
	}

	ephemeralPublic, err := parseECDHPublicKey(envelope.EphemeralPublicKey)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	salt, err := decodeRecoveryField(envelope.Salt, 32)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(salt)
	nonce, err := decodeRecoveryField(envelope.Nonce, 12)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(nonce)
	ciphertext, err := decodeRecoveryFieldAtLeast(envelope.Ciphertext, 16)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(ciphertext)

	privateKey, err := identity.key.ECDH()
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	sharedSecret, err := privateKey.ECDH(ephemeralPublic)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(sharedSecret)
	key, err := recoveryEncryptionKey(sharedSecret, salt)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(key)
	aead, err := recoveryAEAD(key)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	aad := recoveryAdditionalData(envelope.context())
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	defer clear(plaintext)
	if !verifier(string(plaintext)) {
		return ErrRecoveryKeyRejected
	}
	return nil
}

func sealRecoveryEnvelopeWithMaterial(devicePublicKey PublicKeyJWK, context RecoveryContext, recoveryKey string, ephemeralPrivateBytes, salt, nonce []byte) (RecoveryEnvelope, error) {
	if recoveryKey == "" || !validRecoveryContext(context) || len(salt) != 32 || len(nonce) != 12 {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	devicePublic, err := parseECDHPublicKey(devicePublicKey)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	ephemeralPrivate, err := ecdh.P256().NewPrivateKey(ephemeralPrivateBytes)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	sharedSecret, err := ephemeralPrivate.ECDH(devicePublic)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	defer clear(sharedSecret)
	key, err := recoveryEncryptionKey(sharedSecret, salt)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	defer clear(key)
	aead, err := recoveryAEAD(key)
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	aad := recoveryAdditionalData(context)
	plaintext := []byte(recoveryKey)
	defer clear(plaintext)
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	defer clear(ciphertext)
	ephemeralJWK, err := publicJWKFromECDH(ephemeralPrivate.PublicKey())
	if err != nil {
		return RecoveryEnvelope{}, ErrInvalidRecoveryEnvelope
	}
	return RecoveryEnvelope{
		Version:            ProtocolVersion,
		Algorithm:          RecoveryEnvelopeAlgorithm,
		DeviceID:           context.DeviceID,
		AccessSubject:      context.AccessSubject,
		BrowserID:          context.BrowserID,
		Generation:         context.Generation,
		EphemeralPublicKey: ephemeralJWK,
		Salt:               base64.RawURLEncoding.EncodeToString(salt),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:         base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func (envelope RecoveryEnvelope) context() RecoveryContext {
	return RecoveryContext{
		DeviceID:      envelope.DeviceID,
		AccessSubject: envelope.AccessSubject,
		BrowserID:     envelope.BrowserID,
		Generation:    envelope.Generation,
	}
}

func validRecoveryContext(context RecoveryContext) bool {
	return context.DeviceID != "" && context.AccessSubject != "" && context.BrowserID != "" && context.Generation != 0
}

func validateRecoveryEnvelope(envelope RecoveryEnvelope) error {
	if envelope.Version != ProtocolVersion || envelope.Algorithm != RecoveryEnvelopeAlgorithm || !validRecoveryContext(envelope.context()) {
		return ErrInvalidRecoveryEnvelope
	}
	if _, err := ParsePublicKeyJWK(envelope.EphemeralPublicKey); err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	salt, err := decodeRecoveryField(envelope.Salt, 32)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	clear(salt)
	nonce, err := decodeRecoveryField(envelope.Nonce, 12)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	clear(nonce)
	ciphertext, err := decodeRecoveryFieldAtLeast(envelope.Ciphertext, 16)
	if err != nil {
		return ErrInvalidRecoveryEnvelope
	}
	clear(ciphertext)
	return nil
}

func recoveryAdditionalData(context RecoveryContext) []byte {
	encoded, _ := json.Marshal(recoveryAAD{
		Version:       ProtocolVersion,
		Algorithm:     RecoveryEnvelopeAlgorithm,
		DeviceID:      context.DeviceID,
		AccessSubject: context.AccessSubject,
		BrowserID:     context.BrowserID,
		Generation:    context.Generation,
	})
	return encoded
}

func recoveryEncryptionKey(sharedSecret, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, sharedSecret, salt, recoveryHKDFInfo, 32)
}

func recoveryAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parseECDHPublicKey(jwk PublicKeyJWK) (*ecdh.PublicKey, error) {
	publicKey, err := ParsePublicKeyJWK(jwk)
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	key, err := publicKey.ECDH()
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	return key, nil
}

func publicJWKFromECDH(publicKey *ecdh.PublicKey) (PublicKeyJWK, error) {
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey.Bytes())
	if x == nil || y == nil {
		return PublicKeyJWK{}, ErrInvalidDeviceKey
	}
	return PublicKeyJWK{
		KeyType: "EC",
		Curve:   "P-256",
		X:       encodeP256Field(x),
		Y:       encodeP256Field(y),
	}, nil
}

func decodeRecoveryField(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, ErrInvalidRecoveryEnvelope
	}
	return decoded, nil
}

func decodeRecoveryFieldAtLeast(value string, minimumSize int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minimumSize {
		return nil, ErrInvalidRecoveryEnvelope
	}
	return decoded, nil
}

package relay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

const SensitiveResultAlgorithm = "ECDH-P256+HKDF-SHA256+A256GCM"

const sensitiveResultHKDFInfo = "executor/sensitive-result/v1"

var (
	ErrInvalidSensitiveResultEnvelope = errors.New("invalid sensitive result envelope")
	ErrSensitiveResultContextMismatch = errors.New("sensitive result context mismatch")
)

type SensitiveResultContext struct {
	DeviceID      string `json:"device_id"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
	Generation    uint64 `json:"generation"`
	RequestID     string `json:"request_id"`
	Method        string `json:"method"`
}

type SensitiveResultEnvelope struct {
	Version            uint16       `json:"version"`
	Algorithm          string       `json:"algorithm"`
	DeviceID           string       `json:"device_id"`
	AccessSubject      string       `json:"access_subject"`
	BrowserID          string       `json:"browser_id"`
	Generation         uint64       `json:"generation"`
	RequestID          string       `json:"request_id"`
	Method             string       `json:"method"`
	EphemeralPublicKey PublicKeyJWK `json:"ephemeral_public_key"`
	Salt               string       `json:"salt"`
	Nonce              string       `json:"nonce"`
	Ciphertext         string       `json:"ciphertext"`
}

type sensitiveResultAAD struct {
	Version       uint16 `json:"version"`
	Algorithm     string `json:"algorithm"`
	DeviceID      string `json:"device_id"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
	Generation    uint64 `json:"generation"`
	RequestID     string `json:"request_id"`
	Method        string `json:"method"`
}

func SealSensitiveResultEnvelope(browserPublicKey PublicKeyJWK, context SensitiveResultContext, plaintext []byte) (SensitiveResultEnvelope, error) {
	if !validSensitiveResultContext(context) || len(plaintext) == 0 {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	peer, err := parseECDHPublicKey(browserPublicKey)
	if err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	shared, err := ephemeral.ECDH(peer)
	if err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(shared)
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(salt)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(nonce)
	aead, err := sensitiveResultAEAD(shared, salt)
	if err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, sensitiveResultAdditionalData(context))
	defer clear(ciphertext)
	ephemeralJWK, err := publicJWKFromECDH(ephemeral.PublicKey())
	if err != nil {
		return SensitiveResultEnvelope{}, ErrInvalidSensitiveResultEnvelope
	}
	return SensitiveResultEnvelope{
		Version: ProtocolVersion, Algorithm: SensitiveResultAlgorithm, DeviceID: context.DeviceID,
		AccessSubject: context.AccessSubject, BrowserID: context.BrowserID, Generation: context.Generation,
		RequestID: context.RequestID, Method: context.Method, EphemeralPublicKey: ephemeralJWK,
		Salt: base64.RawURLEncoding.EncodeToString(salt), Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func OpenSensitiveResultEnvelope(identity *DeviceIdentity, envelope SensitiveResultEnvelope, expected SensitiveResultContext) ([]byte, error) {
	if !validPrivateKey(identity) || !validSensitiveResultContext(expected) || !validSensitiveResultEnvelope(envelope) {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	if envelope.context() != expected {
		return nil, ErrSensitiveResultContextMismatch
	}
	peer, err := parseECDHPublicKey(envelope.EphemeralPublicKey)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	privateKey, err := identity.key.ECDH()
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	shared, err := privateKey.ECDH(peer)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(shared)
	salt, err := decodeRecoveryField(envelope.Salt, 32)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(salt)
	nonce, err := decodeRecoveryField(envelope.Nonce, 12)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(nonce)
	ciphertext, err := decodeRecoveryFieldAtLeast(envelope.Ciphertext, 16)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	defer clear(ciphertext)
	aead, err := sensitiveResultAEAD(shared, salt)
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, sensitiveResultAdditionalData(expected))
	if err != nil {
		return nil, ErrInvalidSensitiveResultEnvelope
	}
	return plaintext, nil
}

func (envelope SensitiveResultEnvelope) context() SensitiveResultContext {
	return SensitiveResultContext{
		DeviceID: envelope.DeviceID, AccessSubject: envelope.AccessSubject, BrowserID: envelope.BrowserID,
		Generation: envelope.Generation, RequestID: envelope.RequestID, Method: envelope.Method,
	}
}

func validSensitiveResultContext(context SensitiveResultContext) bool {
	return validAuthText(context.DeviceID, 256) && validAuthText(context.AccessSubject, 512) &&
		validAuthText(context.BrowserID, 256) && context.Generation > 0 &&
		validAuthText(context.RequestID, 256) && validAuthText(context.Method, 256)
}

func validSensitiveResultEnvelope(envelope SensitiveResultEnvelope) bool {
	if envelope.Version != ProtocolVersion || envelope.Algorithm != SensitiveResultAlgorithm || !validSensitiveResultContext(envelope.context()) {
		return false
	}
	if _, err := ParsePublicKeyJWK(envelope.EphemeralPublicKey); err != nil {
		return false
	}
	salt, saltErr := decodeRecoveryField(envelope.Salt, 32)
	nonce, nonceErr := decodeRecoveryField(envelope.Nonce, 12)
	ciphertext, cipherErr := decodeRecoveryFieldAtLeast(envelope.Ciphertext, 16)
	clear(salt)
	clear(nonce)
	clear(ciphertext)
	return saltErr == nil && nonceErr == nil && cipherErr == nil
}

func sensitiveResultAdditionalData(context SensitiveResultContext) []byte {
	encoded, _ := json.Marshal(sensitiveResultAAD{
		Version: ProtocolVersion, Algorithm: SensitiveResultAlgorithm, DeviceID: context.DeviceID,
		AccessSubject: context.AccessSubject, BrowserID: context.BrowserID, Generation: context.Generation,
		RequestID: context.RequestID, Method: context.Method,
	})
	return encoded
}

func sensitiveResultAEAD(shared, salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, shared, salt, sensitiveResultHKDFInfo, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

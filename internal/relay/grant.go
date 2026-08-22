package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

const (
	DeviceGrantType   = "executor-device-grant+jwt"
	MaxGrantLifetime  = 30 * 24 * time.Hour
	MaxGrantClockSkew = 2 * time.Minute
)

var (
	ErrInvalidDeviceGrant         = errors.New("invalid device grant")
	ErrDeviceGrantContextMismatch = errors.New("device grant context mismatch")
	ErrDeviceGrantExpired         = errors.New("device grant expired")
	ErrDeviceGrantNotYetValid     = errors.New("device grant not yet valid")
)

type GrantClaims struct {
	Version       uint16 `json:"version"`
	DeviceID      string `json:"device_id"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
	Generation    uint64 `json:"generation"`
	IssuedAt      int64  `json:"issued_at"`
	ExpiresAt     int64  `json:"expires_at"`
	JTI           string `json:"jti"`
}

type GrantExpectation struct {
	DeviceID      string
	AccessSubject string
	BrowserID     string
	Generation    uint64
	Now           time.Time
}

type deviceGrantHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	Version   uint16 `json:"version"`
}

func SignDeviceGrant(identity *DeviceIdentity, claims GrantClaims) (string, error) {
	if !validPrivateKey(identity) || !validGrantClaims(claims) {
		return "", ErrInvalidDeviceGrant
	}
	headerSegment, err := marshalGrantSegment(deviceGrantHeader{
		Algorithm: "ES256",
		Type:      DeviceGrantType,
		Version:   ProtocolVersion,
	})
	if err != nil {
		return "", ErrInvalidDeviceGrant
	}
	claimsSegment, err := marshalGrantSegment(claims)
	if err != nil {
		return "", ErrInvalidDeviceGrant
	}
	signingInput := headerSegment + "." + claimsSegment
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, identity.key, digest[:])
	if err != nil {
		return "", ErrInvalidDeviceGrant
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func VerifyDeviceGrant(publicKeyJWK PublicKeyJWK, token string, expected GrantExpectation) (GrantClaims, error) {
	if !validGrantExpectation(expected) || len(token) > 16*1024 {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	publicKey, err := ParsePublicKeyJWK(publicKeyJWK)
	if err != nil {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}

	var header deviceGrantHeader
	if err := decodeGrantSegment(parts[0], &header); err != nil {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	if header.Algorithm != "ES256" || header.Type != DeviceGrantType || header.Version != ProtocolVersion {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(publicKey, digest[:], r, s) {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}

	var claims GrantClaims
	if err := decodeGrantSegment(parts[1], &claims); err != nil || !validGrantClaims(claims) {
		return GrantClaims{}, ErrInvalidDeviceGrant
	}
	if claims.DeviceID != expected.DeviceID || claims.AccessSubject != expected.AccessSubject ||
		claims.BrowserID != expected.BrowserID || claims.Generation != expected.Generation {
		return GrantClaims{}, ErrDeviceGrantContextMismatch
	}
	if claims.ExpiresAt <= expected.Now.Unix() {
		return GrantClaims{}, ErrDeviceGrantExpired
	}
	if claims.IssuedAt > expected.Now.Add(MaxGrantClockSkew).Unix() {
		return GrantClaims{}, ErrDeviceGrantNotYetValid
	}
	return claims, nil
}

func validGrantClaims(claims GrantClaims) bool {
	if claims.Version != ProtocolVersion || strings.TrimSpace(claims.DeviceID) == "" ||
		strings.TrimSpace(claims.AccessSubject) == "" || strings.TrimSpace(claims.BrowserID) == "" ||
		claims.Generation == 0 || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt ||
		strings.TrimSpace(claims.JTI) == "" {
		return false
	}
	lifetimeSeconds := claims.ExpiresAt - claims.IssuedAt
	return lifetimeSeconds <= int64(MaxGrantLifetime/time.Second)
}

func validGrantExpectation(expected GrantExpectation) bool {
	return strings.TrimSpace(expected.DeviceID) != "" && strings.TrimSpace(expected.AccessSubject) != "" &&
		strings.TrimSpace(expected.BrowserID) != "" && expected.Generation != 0 && !expected.Now.IsZero()
}

func marshalGrantSegment(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidDeviceGrant
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeGrantSegment(segment string, destination any) error {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil || len(encoded) == 0 || len(encoded) > 8*1024 {
		return ErrInvalidDeviceGrant
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalidDeviceGrant
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ErrInvalidDeviceGrant
	}
	return nil
}

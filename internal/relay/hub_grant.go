package relay

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"time"
)

const HubGrantType = "executor-hub-grant+jwt"

var ErrInvalidHubGrant = errors.New("invalid or unauthorized Hub delegation")

// HubGrantClaims is a device-signed delegation, distinct from browser grants.
// Possession is not sufficient: execution must also verify proof of the Hub
// key and fetch current delegation/generation state before using these claims.
type HubGrantClaims struct {
	Version           uint16 `json:"version"`
	DeviceID          string `json:"device_id"`
	HubID             string `json:"hub_id"`
	HubKeyID          string `json:"hub_key_id"`
	Generation        uint64 `json:"generation"`
	DelegationVersion uint64 `json:"delegation_version"`
	IssuedAt          int64  `json:"issued_at"`
	ExpiresAt         int64  `json:"expires_at"`
	JTI               string `json:"jti"`
}

// Expectations must come from verified machine identity and local persisted
// delegation state, never fields supplied by an unverified relay caller.
type HubGrantExpectation struct {
	DeviceID, HubID, HubKeyID     string
	Generation, DelegationVersion uint64
	Now                           time.Time
}

func validHubClaims(c HubGrantClaims) bool {
	key, err := hex.DecodeString(c.HubKeyID)
	return err == nil && len(key) == 32 && c.HubKeyID == strings.ToLower(c.HubKeyID) &&
		len(c.DeviceID) <= 256 && len(c.HubID) <= 256 && len(c.JTI) <= 256 &&
		c.Version == ProtocolVersion && strings.TrimSpace(c.DeviceID) != "" && strings.TrimSpace(c.HubID) != "" &&
		c.Generation > 0 && c.DelegationVersion > 0 && c.IssuedAt > 0 && c.ExpiresAt > c.IssuedAt &&
		c.ExpiresAt-c.IssuedAt <= int64(MaxGrantLifetime/time.Second) && strings.TrimSpace(c.JTI) != ""
}

func SignHubGrant(identity *DeviceIdentity, claims HubGrantClaims) (string, error) {
	if !validPrivateKey(identity) || !validHubClaims(claims) {
		return "", ErrInvalidHubGrant
	}
	header, err := marshalGrantSegment(deviceGrantHeader{Algorithm: "ES256", Type: HubGrantType, Version: ProtocolVersion})
	if err != nil {
		return "", ErrInvalidHubGrant
	}
	body, err := marshalGrantSegment(claims)
	if err != nil {
		return "", ErrInvalidHubGrant
	}
	input := header + "." + body
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, identity.key, digest[:])
	if err != nil {
		return "", ErrInvalidHubGrant
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func VerifyHubGrant(key PublicKeyJWK, token string, expected HubGrantExpectation) (HubGrantClaims, error) {
	fail := func() (HubGrantClaims, error) { return HubGrantClaims{}, ErrInvalidHubGrant }
	if len(token) > 16*1024 || expected.Now.IsZero() {
		return fail()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fail()
	}
	var header deviceGrantHeader
	if decodeGrantSegment(parts[0], &header) != nil || header.Algorithm != "ES256" || header.Type != HubGrantType || header.Version != ProtocolVersion {
		return fail()
	}
	public, err := ParsePublicKeyJWK(key)
	if err != nil {
		return fail()
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return fail()
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return fail()
	}
	var claims HubGrantClaims
	if decodeGrantSegment(parts[1], &claims) != nil || !validHubClaims(claims) {
		return fail()
	}
	if claims.DeviceID != expected.DeviceID || claims.HubID != expected.HubID || claims.HubKeyID != expected.HubKeyID || claims.Generation != expected.Generation || claims.DelegationVersion != expected.DelegationVersion || claims.ExpiresAt <= expected.Now.Unix() || claims.IssuedAt > expected.Now.Add(MaxGrantClockSkew).Unix() {
		return fail()
	}
	return claims, nil
}

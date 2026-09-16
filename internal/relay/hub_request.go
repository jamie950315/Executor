package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

var ErrInvalidHubRequest = errors.New("invalid, expired or replayed Hub request")

// Input is base64-encoded by JSON transport so forwarding cannot normalize the
// signed JSON bytes. It contains the ordinary MCP arguments object.
type HubRequest struct {
	Version   uint16 `json:"version"`
	DeviceID  string `json:"device_id"`
	HubID     string `json:"hub_id"`
	CallerID  string `json:"caller_id"`
	RequestID string `json:"request_id"`
	Method    string `json:"method"`
	Input     []byte `json:"input"`
	Grant     string `json:"grant"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

type SignedHubRequest struct {
	Request   HubRequest `json:"request"`
	Signature string     `json:"signature"`
}

// ReplayConsumer must atomically persist the nonce before returning success.
// It must fail on duplicate, capacity exhaustion or storage error, and retain
// entries until expiry across restarts. Execution must not occur on an error.
type ReplayConsumer interface{ Consume(string, time.Time) error }

// HubKeyID is the hexadecimal SHA-256 of RFC 7638 canonical public JWK members.
func HubKeyID(key PublicKeyJWK) (string, error) {
	if _, err := ParsePublicKeyJWK(key); err != nil {
		return "", ErrInvalidHubRequest
	}
	canonical := struct {
		Crv string `json:"crv"`
		Kty string `json:"kty"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}{key.Curve, key.KeyType, key.X, key.Y}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", ErrInvalidHubRequest
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func hubRequestDigest(r HubRequest) ([32]byte, error) {
	var empty [32]byte
	for _, value := range []string{r.DeviceID, r.HubID, r.CallerID, r.RequestID, r.Method, r.Nonce} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return empty, ErrInvalidHubRequest
		}
	}
	input := bytes.TrimSpace(r.Input)
	if r.Version != ProtocolVersion || len(r.Input) > 80<<20 || len(input) == 0 || input[0] != '{' || !json.Valid(input) || len(r.Grant) == 0 || len(r.Grant) > 16*1024 || r.IssuedAt <= 0 || r.ExpiresAt <= r.IssuedAt || r.ExpiresAt-r.IssuedAt > 60 {
		return empty, ErrInvalidHubRequest
	}
	inputHash := sha256.Sum256(r.Input)
	grantHash := sha256.Sum256([]byte(r.Grant))
	// Hash the raw arguments and grant, not their potentially large contents.
	proof := struct {
		Domain    string `json:"domain"`
		Version   uint16 `json:"version"`
		DeviceID  string `json:"device_id"`
		HubID     string `json:"hub_id"`
		CallerID  string `json:"caller_id"`
		RequestID string `json:"request_id"`
		Method    string `json:"method"`
		InputHash string `json:"input_sha256"`
		GrantHash string `json:"grant_sha256"`
		IssuedAt  int64  `json:"issued_at"`
		ExpiresAt int64  `json:"expires_at"`
		Nonce     string `json:"nonce"`
	}{"executor-hub-request-v1", r.Version, r.DeviceID, r.HubID, r.CallerID, r.RequestID, r.Method, hex.EncodeToString(inputHash[:]), hex.EncodeToString(grantHash[:]), r.IssuedAt, r.ExpiresAt, r.Nonce}
	data, err := json.Marshal(proof)
	if err != nil {
		return empty, ErrInvalidHubRequest
	}
	return sha256.Sum256(data), nil
}

func SignHubRequest(hub *DeviceIdentity, request HubRequest) (SignedHubRequest, error) {
	if !validPrivateKey(hub) {
		return SignedHubRequest{}, ErrInvalidHubRequest
	}
	hash, err := hubRequestDigest(request)
	if err != nil {
		return SignedHubRequest{}, err
	}
	r, s, err := ecdsa.Sign(rand.Reader, hub.key, hash[:])
	if err != nil {
		return SignedHubRequest{}, ErrInvalidHubRequest
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	request.Input = append([]byte(nil), request.Input...)
	return SignedHubRequest{request, base64.RawURLEncoding.EncodeToString(signature)}, nil
}

func VerifyHubRequest(deviceKey, hubKey PublicKeyJWK, expected HubGrantExpectation, signed SignedHubRequest, replay ReplayConsumer) error {
	r := signed.Request
	hash, err := hubRequestDigest(r)
	if err != nil || replay == nil || expected.Now.IsZero() || r.DeviceID != expected.DeviceID || r.HubID != expected.HubID || r.ExpiresAt <= expected.Now.Unix() || r.IssuedAt > expected.Now.Add(5*time.Second).Unix() {
		return ErrInvalidHubRequest
	}
	keyID, err := HubKeyID(hubKey)
	if err != nil || keyID != expected.HubKeyID {
		return ErrInvalidHubRequest
	}
	if _, err := VerifyHubGrant(deviceKey, r.Grant, expected); err != nil {
		return ErrInvalidHubRequest
	}
	public, err := ParsePublicKeyJWK(hubKey)
	if err != nil {
		return ErrInvalidHubRequest
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(signed.Signature)
	if err != nil || len(signature) != 64 || !ecdsa.Verify(public, hash[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return ErrInvalidHubRequest
	}
	// Hash a structured key to avoid ambiguous concatenation of attacker-selected IDs.
	keyBytes, _ := json.Marshal([]string{r.DeviceID, r.HubID, keyID, r.Nonce})
	replayHash := sha256.Sum256(keyBytes)
	if err := replay.Consume(hex.EncodeToString(replayHash[:]), time.Unix(r.ExpiresAt, 0)); err != nil {
		return ErrInvalidHubRequest
	}
	return nil
}

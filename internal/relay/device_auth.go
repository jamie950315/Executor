package relay

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

var ErrInvalidDeviceAuthentication = errors.New("invalid device authentication")

type deviceChallenge struct {
	Version  uint16 `json:"version"`
	Purpose  string `json:"purpose"`
	DeviceID string `json:"device_id"`
	Nonce    string `json:"nonce"`
	IssuedAt int64  `json:"issued_at"`
}

type DeviceRefresh struct {
	DeviceID        string `json:"device_id"`
	Generation      uint64 `json:"generation"`
	Name            string `json:"name"`
	Platform        string `json:"platform"`
	Arch            string `json:"arch"`
	ExecutorVersion string `json:"executor_version"`
	MCPURL          string `json:"mcp_url"`
	IssuedAt        int64  `json:"issued_at"`
}

type canonicalDeviceRefresh struct {
	Version         uint16 `json:"version"`
	Purpose         string `json:"purpose"`
	DeviceID        string `json:"device_id"`
	Generation      uint64 `json:"generation"`
	Name            string `json:"name"`
	Platform        string `json:"platform"`
	Arch            string `json:"arch"`
	ExecutorVersion string `json:"executor_version"`
	MCPURL          string `json:"mcp_url"`
	IssuedAt        int64  `json:"issued_at"`
}

func CanonicalDeviceChallenge(deviceID, nonce string, issuedAt int64) ([]byte, error) {
	if !validAuthText(deviceID, 256) || !validAuthText(nonce, 256) || issuedAt <= 0 {
		return nil, ErrInvalidDeviceAuthentication
	}
	encoded, err := json.Marshal(deviceChallenge{
		Version: ProtocolVersion, Purpose: "executor-device-connect", DeviceID: deviceID, Nonce: nonce, IssuedAt: issuedAt,
	})
	if err != nil {
		return nil, ErrInvalidDeviceAuthentication
	}
	return encoded, nil
}

func SignDeviceChallenge(identity *DeviceIdentity, deviceID, nonce string, issuedAt int64) (string, error) {
	canonical, err := CanonicalDeviceChallenge(deviceID, nonce, issuedAt)
	if err != nil {
		return "", err
	}
	return signP1363(identity, canonical)
}

func VerifyDeviceChallenge(publicKey PublicKeyJWK, deviceID, nonce string, issuedAt int64, signature string) bool {
	canonical, err := CanonicalDeviceChallenge(deviceID, nonce, issuedAt)
	if err != nil {
		return false
	}
	return verifyP1363(publicKey, canonical, signature)
}

func CanonicalDeviceRefresh(refresh DeviceRefresh) ([]byte, error) {
	if !validAuthText(refresh.DeviceID, 256) || refresh.Generation == 0 ||
		!validAuthText(refresh.Name, 256) || !validAuthText(refresh.Platform, 64) ||
		!validAuthText(refresh.Arch, 64) || !validAuthText(refresh.ExecutorVersion, 64) ||
		len(refresh.MCPURL) > 2048 || refresh.IssuedAt <= 0 {
		return nil, ErrInvalidDeviceAuthentication
	}
	encoded, err := json.Marshal(canonicalDeviceRefresh{
		Version: ProtocolVersion, Purpose: "executor-device-refresh", DeviceID: refresh.DeviceID,
		Generation: refresh.Generation, Name: refresh.Name, Platform: refresh.Platform, Arch: refresh.Arch,
		ExecutorVersion: refresh.ExecutorVersion, MCPURL: refresh.MCPURL, IssuedAt: refresh.IssuedAt,
	})
	if err != nil {
		return nil, ErrInvalidDeviceAuthentication
	}
	return encoded, nil
}

func SignDeviceRefresh(identity *DeviceIdentity, refresh DeviceRefresh) (string, error) {
	canonical, err := CanonicalDeviceRefresh(refresh)
	if err != nil {
		return "", err
	}
	return signP1363(identity, canonical)
}

func VerifyDeviceRefresh(publicKey PublicKeyJWK, refresh DeviceRefresh, signature string) bool {
	canonical, err := CanonicalDeviceRefresh(refresh)
	if err != nil {
		return false
	}
	return verifyP1363(publicKey, canonical, signature)
}

func signP1363(identity *DeviceIdentity, value []byte) (string, error) {
	if !validPrivateKey(identity) || len(value) == 0 {
		return "", ErrInvalidDeviceAuthentication
	}
	digest := sha256.Sum256(value)
	r, s, err := ecdsa.Sign(rand.Reader, identity.key, digest[:])
	if err != nil {
		return "", ErrInvalidDeviceAuthentication
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

func verifyP1363(publicKeyJWK PublicKeyJWK, value []byte, signatureText string) bool {
	publicKey, err := ParsePublicKeyJWK(publicKeyJWK)
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(signatureText)
	if err != nil || len(signature) != 64 {
		return false
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	digest := sha256.Sum256(value)
	return ecdsa.Verify(publicKey, digest[:], r, s)
}

func validAuthText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum
}

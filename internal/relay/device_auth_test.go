package relay

import (
	"encoding/base64"
	"testing"
)

func TestCanonicalDeviceChallengeMatchesWorkerByteForByteAndSignsP1363(t *testing.T) {
	t.Parallel()
	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalDeviceChallenge("device-vector-1", "nonce-vector-1", 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"purpose":"executor-device-connect","device_id":"device-vector-1","nonce":"nonce-vector-1","issued_at":1700000000}`
	if string(canonical) != want {
		t.Fatalf("challenge = %s, want %s", canonical, want)
	}
	signature, err := SignDeviceChallenge(identity, "device-vector-1", "nonce-vector-1", 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || len(decoded) != 64 {
		t.Fatalf("signature = %q, bytes=%d err=%v", signature, len(decoded), err)
	}
	if !VerifyDeviceChallenge(identity.PublicJWK(), "device-vector-1", "nonce-vector-1", 1_700_000_000, signature) {
		t.Fatal("valid device challenge signature was rejected")
	}
	if VerifyDeviceChallenge(identity.PublicJWK(), "device-vector-2", "nonce-vector-1", 1_700_000_000, signature) {
		t.Fatal("signature verified for the wrong device")
	}
}

func TestDeviceRefreshCanonicalFieldsAndSignatureBindMetadata(t *testing.T) {
	t.Parallel()
	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	refresh := DeviceRefresh{
		DeviceID: "device-vector-1", Generation: 8, Name: "Owner Mac", Platform: "darwin",
		Arch: "arm64", ExecutorVersion: "dev", MCPURL: "https://executor.example.test/mcp", IssuedAt: 1_700_000_001,
	}
	canonical, err := CanonicalDeviceRefresh(refresh)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"purpose":"executor-device-refresh","device_id":"device-vector-1","generation":8,"name":"Owner Mac","platform":"darwin","arch":"arm64","executor_version":"dev","mcp_url":"https://executor.example.test/mcp","issued_at":1700000001}`
	if string(canonical) != want {
		t.Fatalf("refresh = %s, want %s", canonical, want)
	}
	signature, err := SignDeviceRefresh(identity, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyDeviceRefresh(identity.PublicJWK(), refresh, signature) {
		t.Fatal("valid refresh signature was rejected")
	}
	refresh.Generation--
	if VerifyDeviceRefresh(identity.PublicJWK(), refresh, signature) {
		t.Fatal("refresh signature did not bind credential generation")
	}
}

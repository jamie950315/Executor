package relayclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestOwnerCanDelegateAndRevokeHubWithoutRecoveryKeyStorage(t *testing.T) {
	path, cfg, values := relayFixture(t)
	if err := relay.WithHubStateLock(cfg.StateDir, func() error { return nil }); err != nil {
		t.Fatalf("state lock preflight: %v", err)
	}
	now := time.Unix(1700000000, 0)
	hub, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := relay.HubKeyID(hub.PublicJWK())
	recorder := &recordingDispatcher{}
	adapter, err := NewAdapter(AdapterOptions{ConfigPath: path, Now: func() time.Time { return now }, DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return recorder }})
	if err != nil {
		t.Fatal(err)
	}
	ownerGrant := signGrantForTest(t, values, cfg, "owner", "browser", now)
	input := map[string]any{"hub_id": "pi5", "public_key": hub.PublicJWK(), "expires_in_seconds": 3600}
	args := callArgumentsForTest(t, ownerGrant, "owner", "browser", input)
	result, err := adapter.HandleRequest(context.Background(), "delegate", "hub.delegate", args)
	if err != nil {
		t.Fatalf("owner delegation failed: %v", err)
	}
	var output struct {
		Grant   string `json:"grant"`
		Version uint64 `json:"delegation_version"`
	}
	if json.Unmarshal(result.Payload, &output) != nil || output.Version != 1 {
		t.Fatal("invalid delegation result")
	}
	stored, err := os.ReadFile(filepath.Join(cfg.StateDir, "hub-delegations.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{ownerGrant, output.Grant, values.RecoveryKey} {
		if secret != "" && strings.Contains(string(stored), secret) {
			t.Fatal("delegation state leaked credential")
		}
	}
	device, _ := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if _, err := relay.VerifyHubGrant(device.PublicJWK(), output.Grant, relay.HubGrantExpectation{DeviceID: cfg.UnifiedDashboard.DeviceID, HubID: "pi5", HubKeyID: keyID, Generation: values.Generation, DelegationVersion: 1, Now: now}); err != nil {
		t.Fatal(err)
	}
	proof, err := relay.SignHubRequest(hub, relay.HubRequest{Version: 1, DeviceID: cfg.UnifiedDashboard.DeviceID, HubID: "pi5", CallerID: "caller", RequestID: "call-1", Method: "filesystem_read", Input: []byte(`{"path":"fixture"}`), Grant: output.Grant, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce-1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(proof)
	if _, err := adapter.HandleRequest(context.Background(), "call-1", "hub.call", raw); err != nil {
		t.Fatal(err)
	}
	selfDelegate := proof.Request
	selfDelegate.RequestID = "self-delegate"
	selfDelegate.Nonce = "nonce-self"
	selfDelegate.Method = "hub.delegate"
	selfProof, err := relay.SignHubRequest(hub, selfDelegate)
	if err != nil {
		t.Fatal(err)
	}
	selfRaw, _ := json.Marshal(selfProof)
	if _, err := adapter.HandleRequest(context.Background(), "self-delegate", "hub.call", selfRaw); err == nil || len(recorder.calls) != 1 {
		t.Fatal("Hub delegated itself using its machine grant")
	}
	revoked, err := adapter.HandleRequest(context.Background(), "revoke", "hub.revoke", callArgumentsForTest(t, ownerGrant, "owner", "browser", map[string]any{"hub_id": "pi5"}))
	if err != nil || len(revoked.Payload) == 0 {
		t.Fatal("owner revocation failed")
	}
	proof.Request.RequestID = "call-2"
	proof.Request.Nonce = "nonce-2"
	proof, err = relay.SignHubRequest(hub, proof.Request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(proof)
	if _, err := adapter.HandleRequest(context.Background(), "call-2", "hub.call", raw); err == nil || len(recorder.calls) != 1 {
		t.Fatal("revoked Hub executed")
	}
	result, err = adapter.HandleRequest(context.Background(), "delegate-again", "hub.delegate", args)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(result.Payload, &output) != nil || output.Version != 3 {
		t.Fatal("delegation version did not increase monotonically")
	}
}

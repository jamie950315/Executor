package relayclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestHubAdapterRequiresLocalApprovalAndPersistsReplay(t *testing.T) {
	path, cfg, values := relayFixture(t)
	now := time.Unix(1700000000, 0)
	device, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := relay.HubKeyID(hub.PublicJWK())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := relay.SignHubGrant(device, relay.HubGrantClaims{Version: 1, DeviceID: cfg.UnifiedDashboard.DeviceID, HubID: "pi5", HubKeyID: keyID, Generation: values.Generation, DelegationVersion: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: "grant"})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := relay.SignHubRequest(hub, relay.HubRequest{Version: 1, DeviceID: cfg.UnifiedDashboard.DeviceID, HubID: "pi5", CallerID: "caller-1", RequestID: "request-1", Method: "filesystem_write", Input: []byte(`{"path":"fixture","content":"ok"}`), Grant: grant, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce-1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &recordingDispatcher{}
	options := AdapterOptions{ConfigPath: path, Now: func() time.Time { return now }, DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher }}
	adapter, err := NewAdapter(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-1", "hub.call", raw); err == nil || len(dispatcher.calls) != 0 {
		t.Fatal("unapproved Hub executed")
	}
	approval := HubApprovalFile{Version: 1, DeviceID: cfg.UnifiedDashboard.DeviceID, Hubs: []HubApproval{{HubID: "pi5", PublicKey: hub.PublicJWK(), DelegationVersion: 1, Enabled: true}}}
	approvalBytes, _ := json.Marshal(approval)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "hub-delegations.json"), approvalBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "wrong-request-id", "hub.call", raw); err == nil || len(dispatcher.calls) != 0 {
		t.Fatal("mismatched outer request ID executed")
	}
	marker := filepath.Join(cfg.StateDir, "disabled")
	if err := os.WriteFile(marker, []byte("disabled"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-1", "hub.call", raw); err == nil || len(dispatcher.calls) != 0 {
		t.Fatal("disabled device executed Hub call")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.HandleRequest(context.Background(), "request-1", "hub.call", raw)
	if err != nil || len(result.Payload) == 0 || len(dispatcher.calls) != 1 {
		t.Fatalf("authorized Hub failed: %v", err)
	}
	if dispatcher.calls[0].Name != "filesystem_write" || dispatcher.calls[0].SessionID == "caller-1" {
		t.Fatal("caller namespace not isolated")
	}
	adapter, err = NewAdapter(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-1", "hub.call", raw); err == nil || len(dispatcher.calls) != 1 {
		t.Fatal("replay executed after adapter restart")
	}
	proof.Request.RequestID = "request-2"
	proof.Request.Nonce = "nonce-2"
	proof, err = relay.SignHubRequest(hub, proof.Request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(proof)
	approval.Hubs[0].Enabled = false
	approvalBytes, _ = json.Marshal(approval)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "hub-delegations.json"), approvalBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-2", "hub.call", raw); err == nil || len(dispatcher.calls) != 1 {
		t.Fatal("revoked Hub executed")
	}
}

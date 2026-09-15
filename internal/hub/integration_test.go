package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/daemon"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/hub"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/relayclient"
	"github.com/jamie950315/executor/internal/secrets"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestSignedHubHTTPRoutesToTwoIsolatedNativeHelpers(t *testing.T) {
	identity, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	hubKeyID, err := relay.HubKeyID(identity.PublicJWK())
	if err != nil {
		t.Fatal(err)
	}
	adapters := map[string]*relayclient.Adapter{}
	grants := map[string]hub.Delegation{}
	paths := map[string]string{}
	devices := []hub.Device{}
	for _, id := range []string{"device-a", "device-b"} {
		state, err := os.MkdirTemp("", "hub-native-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(state) })
		cfg := config.Default(state)
		cfg.Domain = "fixture.example.test"
		cfg.UnifiedDashboard.DeviceID = id
		if runtime.GOOS == "windows" {
			cfg.DesktopEndpoint = `\\.\pipe\` + filepath.Base(state) + "-desktop"
		}
		configPath := filepath.Join(state, "config.json")
		if err := config.Save(configPath, cfg); err != nil {
			t.Fatal(err)
		}
		values, err := secrets.Create(state)
		if err != nil {
			t.Fatal(err)
		}
		deviceIdentity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
		if err != nil {
			t.Fatal(err)
		}
		approval := relayclient.HubApprovalFile{Version: 1, DeviceID: id, Hubs: []relayclient.HubApproval{{HubID: "pi5", PublicKey: identity.PublicJWK(), DelegationVersion: 1, Enabled: true}}}
		encoded, _ := json.Marshal(approval)
		if err := os.WriteFile(filepath.Join(state, "hub-delegations.json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		grant, err := relay.SignHubGrant(deviceIdentity, relay.HubGrantClaims{Version: 1, DeviceID: id, HubID: "pi5", HubKeyID: hubKeyID, Generation: values.Generation, DelegationVersion: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: id + "-grant"})
		if err != nil {
			t.Fatal(err)
		}
		grants[id] = hub.Delegation{DeviceKey: deviceIdentity.PublicJWK(), Grant: grant, Generation: values.Generation, Version: 1}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- daemon.RunDesktop(ctx, configPath) }()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("fixture helper did not stop")
			}
		})
		client := ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey))
		deadline := time.Now().Add(3 * time.Second)
		for {
			var sessions []terminal.SessionInfo
			err = client.Call(context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &sessions)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("fixture IPC unavailable")
			}
			time.Sleep(10 * time.Millisecond)
		}
		adapters[id], err = relayclient.NewAdapter(relayclient.AdapterOptions{ConfigPath: configPath})
		if err != nil {
			t.Fatal(err)
		}
		paths[id] = filepath.Join(state, "fixture.txt")
		devices = append(devices, hub.Device{ID: id, Online: true, Authorized: true})
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-machine-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/hub/devices" {
			json.NewEncoder(w).Encode(map[string]any{"devices": devices})
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/hub/devices/"), "/call")
		adapter := adapters[id]
		if adapter == nil {
			http.NotFound(w, r)
			return
		}
		var proof relay.SignedHubRequest
		if json.NewDecoder(r.Body).Decode(&proof) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		raw, _ := json.Marshal(proof)
		result, err := adapter.HandleRequest(r.Context(), proof.Request.RequestID, "hub.call", raw)
		if err != nil {
			http.Error(w, "rejected", 403)
			return
		}
		w.Write(result.Payload)
	}))
	defer gateway.Close()
	transport, err := hub.NewDashboardRelay(gateway.URL, "isolated-machine-token", gateway.Client())
	if err != nil {
		t.Fatal(err)
	}
	signed, err := hub.NewSignedRelay(transport, "pi5", identity, func(_ context.Context, id string) (hub.Delegation, error) { return grants[id], nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := hub.New(signed)
	for _, id := range []string{"device-a", "device-b"} {
		content := "獨立裝置 " + id + "\n"
		_, err := router.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", SessionID: "test-caller", Arguments: map[string]any{"deviceId": id, "privilege": "owner", "action": "write_file", "path": paths[id], "content": content}})
		if err != nil {
			t.Fatalf("native write %s: %v", id, err)
		}
		result, err := router.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", SessionID: "test-caller", Arguments: map[string]any{"deviceId": id, "privilege": "owner", "action": "read_file", "path": paths[id]}})
		if err != nil || result.(map[string]any)["content"] != content {
			t.Fatalf("native read %s failed: %v", id, err)
		}
		bytes, err := os.ReadFile(paths[id])
		if err != nil || string(bytes) != content {
			t.Fatal("independent filesystem verification failed")
		}
	}
}

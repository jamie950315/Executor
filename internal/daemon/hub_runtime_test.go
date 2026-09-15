package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jamie950315/executor/internal/agent"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/mcp"
)

func TestHubCallerScopeSeparatesOAuthClients(t *testing.T) {
	a := agent.Actor{Subject: "owner", ClientID: "client-a", Method: "oauth"}
	b := agent.Actor{Subject: "owner", ClientID: "client-b", Method: "oauth"}
	if hubCallerScope("session", a) == hubCallerScope("session", b) {
		t.Fatal("OAuth clients share Hub caller scope")
	}
	if hubCallerScope("session", a) == hubCallerScope("other-session", a) {
		t.Fatal("MCP sessions share Hub caller scope")
	}
}

func TestHubRuntimeDoesNotFallBackWhenConfigurationMissing(t *testing.T) {
	_, cfg, values := daemonFixture(t)
	cfg.HubEnabled = true
	if _, err := newAuditedDispatcher(cfg, values); err == nil {
		t.Fatal("missing Hub config fell back to local dispatch")
	}
}

func TestHubRuntimeUsesMachineDirectoryWithoutLocalHelpers(t *testing.T) {
	path, cfg, values := daemonFixture(t)
	cfg.HubEnabled = true
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hub/devices" || r.Header.Get("Authorization") != "Bearer fixture-machine-token" {
			t.Error("unexpected Hub request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"devices":[]}`))
	}))
	defer server.Close()
	manifest, _ := json.Marshal(map[string]any{"version": 1, "hub_id": "pi5", "dashboard_url": server.URL, "machine_token_file": "hub-machine.token"})
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "hub.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "hub-machine.token"), []byte("fixture-machine-token"), 0600); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newAuditedDispatcher(cfg, values)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher(context.Background(), mcp.ToolCall{Name: "devices_list", SessionID: "fixture-caller", Arguments: map[string]any{}})
	if err != nil || result == nil {
		t.Fatalf("Hub directory failed: %v", err)
	}
	tools := runtimeMCPConfig(cfg, dispatcher).Tools
	if len(tools) != len(mcp.BuiltinTools())+1 || tools[0].Name != "devices_list" {
		t.Fatal("Hub runtime schema is not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startDaemon(t, func() error { return RunAgent(ctx, path) })
	defer func() { cancel(); assertDaemonStopped(t, done) }()
	metadata := waitForHTTP(t, http.MethodGet, "http://"+cfg.AgentAddress+"/.well-known/oauth-protected-resource", nil, nil)
	metadata.Body.Close()
	if metadata.StatusCode != http.StatusOK {
		t.Fatal("Hub OAuth metadata unavailable")
	}
	scoped := waitForHTTP(t, http.MethodGet, "http://"+cfg.AgentAddress+"/.well-known/oauth-protected-resource/mcp", nil, nil)
	scoped.Body.Close()
	if scoped.StatusCode != http.StatusOK {
		t.Fatalf("MCP-scoped metadata routed incorrectly: %d", scoped.StatusCode)
	}
	unauthorized := waitForHTTP(t, http.MethodGet, "http://"+cfg.AgentAddress+"/mcp", nil, nil)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatal("Hub HTTP endpoint lost OAuth protection")
	}
}

package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAccountsAndZones(t *testing.T) {
	t.Parallel()

	var sawAccounts, sawZones bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts":
			sawAccounts = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": "acct-1", "name": "Primary"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			sawZones = true
			if got := r.URL.Query().Get("account.id"); got != "acct-1" {
				t.Fatalf("zone filter = %q, want acct-1", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": "zone-1", "name": "example.com"},
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token")

	accounts, err := client.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acct-1" {
		t.Fatalf("unexpected accounts: %#v", accounts)
	}

	zones, err := client.ListZones(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(zones) != 1 || zones[0].Name != "example.com" {
		t.Fatalf("unexpected zones: %#v", zones)
	}
	if !sawAccounts || !sawZones {
		t.Fatalf("missing requests: accounts=%v zones=%v", sawAccounts, sawZones)
	}
}

func TestApplyReusesExistingResourcesAndRedactsSensitiveLogs(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	var postCount int
	tokenPath := filepath.Join(t.TempDir(), "cloudflared.token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer top-secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": "tunnel-1", "name": "executor"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  "server-token",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"config": map[string]any{
						"ingress": []map[string]any{
							{"hostname": "executor.example.com", "service": "http://127.0.0.1:8787", "originRequest": map[string]any{}},
							{"service": "http_status:404"},
						},
					},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode config payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload["config"]})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			if got := r.URL.Query().Get("name"); got != "executor.example.com" {
				t.Fatalf("dns name filter = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]any{
					{
						"id":      "dns-1",
						"type":    "CNAME",
						"name":    "executor.example.com",
						"content": "tunnel-1.cfargotunnel.com",
						"proxied": true,
					},
				},
			})
		case r.Method == http.MethodPost:
			postCount++
			t.Fatalf("unexpected create request: %s", r.URL.String())
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token", WithLogger(logf(&logs)))
	result, err := client.Apply(context.Background(), DeploymentRequest{
		AccountID:             "acct-1",
		ZoneID:                "zone-1",
		TunnelName:            "executor",
		Hostname:              "executor.example.com",
		LocalServiceURL:       "http://localhost:8787",
		TokenFilePath:         tokenPath,
		CloudflaredBinaryPath: "/usr/local/bin/cloudflared",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.CreatedTunnel || result.CreatedDNS {
		t.Fatalf("expected idempotent reuse, got %#v", result)
	}
	if result.CloudflaredConfig != "" {
		t.Fatalf("remote-managed tunnel should not emit local cloudflared config: %q", result.CloudflaredConfig)
	}
	if result.TokenFilePath != tokenPath {
		t.Fatalf("token file path = %q, want %q", result.TokenFilePath, tokenPath)
	}
	if !strings.Contains(result.CloudflaredRunCommand, "--token-file "+tokenPath) {
		t.Fatalf("run command missing token-file: %s", result.CloudflaredRunCommand)
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("ReadFile token: %v", err)
	}
	if strings.TrimSpace(string(tokenBytes)) != "server-token" {
		t.Fatalf("token file contents = %q", string(tokenBytes))
	}
	if info, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("Stat token: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o, want 600", info.Mode().Perm())
	}
	if strings.Contains(logs.String(), "top-secret-token") || strings.Contains(logs.String(), "server-token") {
		t.Fatalf("sensitive material leaked to logs: %s", logs.String())
	}
	if postCount != 0 {
		t.Fatalf("unexpected create operations: %d", postCount)
	}
}

func TestApplyRollsBackTunnelWhenConfigUpdateFails(t *testing.T) {
	t.Parallel()

	var deletedTunnel bool
	tokenPath := filepath.Join(t.TempDir(), "cloudflared.token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":    "tunnel-1",
					"name":  "executor",
					"token": "issued-token",
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"message": "upstream failed"}},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1":
			deletedTunnel = true
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token")
	_, err := client.Apply(context.Background(), DeploymentRequest{
		AccountID:             "acct-1",
		ZoneID:                "zone-1",
		TunnelName:            "executor",
		Hostname:              "executor.example.com",
		LocalServiceURL:       "http://127.0.0.1:8787",
		TokenFilePath:         tokenPath,
		CloudflaredBinaryPath: "/usr/local/bin/cloudflared",
	})
	if err == nil {
		t.Fatal("Apply succeeded, want config failure")
	}
	if !deletedTunnel {
		t.Fatal("expected rollback to delete the created tunnel")
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file should not exist after rollback, err=%v", err)
	}
}

func TestApplyRollsBackTokenFileWhenDNSUpdateFails(t *testing.T) {
	t.Parallel()

	var deletedTunnel bool
	tokenPath := filepath.Join(t.TempDir(), "cloudflared.token")
	if err := os.WriteFile(tokenPath, []byte("previous-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":    "tunnel-1",
					"name":  "executor",
					"token": "issued-token",
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"message": "dns lookup failed"}},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1":
			deletedTunnel = true
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token")
	_, err := client.Apply(context.Background(), DeploymentRequest{
		AccountID:             "acct-1",
		ZoneID:                "zone-1",
		TunnelName:            "executor",
		Hostname:              "executor.example.com",
		LocalServiceURL:       "http://127.0.0.1:8787",
		TokenFilePath:         tokenPath,
		CloudflaredBinaryPath: "/usr/local/bin/cloudflared",
	})
	if err == nil {
		t.Fatal("Apply succeeded, want DNS failure")
	}
	if !deletedTunnel {
		t.Fatal("expected rollback to delete the created tunnel")
	}
	if got, err := os.ReadFile(tokenPath); err != nil || string(got) != "previous-token\n" {
		t.Fatalf("token file should be restored on rollback, content=%q err=%v", got, err)
	}
}

func TestApplyRestoresPreviousTunnelConfigWhenDNSLookupFails(t *testing.T) {
	t.Parallel()

	var configPuts []map[string]any
	tokenPath := filepath.Join(t.TempDir(), "cloudflared.token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  []map[string]any{{"id": "tunnel-1", "name": "executor"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": "server-token"})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"config": map[string]any{
						"ingress": []map[string]any{
							{"hostname": "old.example.com", "service": "http://127.0.0.1:8080", "originRequest": map[string]any{}},
							{"service": "http_status:404"},
						},
					},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode config payload: %v", err)
			}
			configPuts = append(configPuts, payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"message": "dns lookup failed"}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token")
	_, err := client.Apply(context.Background(), DeploymentRequest{
		AccountID:             "acct-1",
		ZoneID:                "zone-1",
		TunnelName:            "executor",
		Hostname:              "executor.example.com",
		LocalServiceURL:       "http://127.0.0.1:8787",
		TokenFilePath:         tokenPath,
		CloudflaredBinaryPath: "/usr/local/bin/cloudflared",
	})
	if err == nil {
		t.Fatal("Apply succeeded, want DNS lookup failure")
	}
	if len(configPuts) != 2 {
		t.Fatalf("expected apply + rollback config writes, got %d", len(configPuts))
	}
	rollbackCfg := configPuts[1]["config"].(map[string]any)
	rollbackIngress := rollbackCfg["ingress"].([]any)
	first := rollbackIngress[0].(map[string]any)
	if first["hostname"] != "old.example.com" {
		t.Fatalf("rollback config did not restore previous ingress: %#v", rollbackCfg)
	}
}

func TestApplyRestoresPreviousDNSRecordWhenLaterStepFails(t *testing.T) {
	t.Parallel()

	var configPuts []map[string]any
	var dnsPatches []map[string]any
	tokenPath := filepath.Join(t.TempDir(), "cloudflared.token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  []map[string]any{{"id": "tunnel-1", "name": "executor"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": "server-token"})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"config": map[string]any{
						"ingress": []map[string]any{
							{"hostname": "old.example.com", "service": "http://127.0.0.1:8080", "originRequest": map[string]any{}},
							{"service": "http_status:404"},
						},
					},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode config payload: %v", err)
			}
			configPuts = append(configPuts, payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": "dns-1", "type": "CNAME", "name": "executor.example.com", "content": "legacy.cfargotunnel.com", "proxied": false},
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-1/dns_records/dns-1":
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode dns payload: %v", err)
			}
			dnsPatches = append(dnsPatches, payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": payload})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "top-secret-token")
	_, err := client.Apply(context.Background(), DeploymentRequest{
		AccountID:             "acct-1",
		ZoneID:                "zone-1",
		TunnelName:            "executor",
		Hostname:              "executor.example.com",
		LocalServiceURL:       "http://127.0.0.1:8787",
		TokenFilePath:         tokenPath,
		CloudflaredBinaryPath: "",
	})
	if err == nil {
		t.Fatal("Apply succeeded, want finalization failure")
	}
	if len(dnsPatches) != 2 {
		t.Fatalf("expected dns update + rollback patch, got %d", len(dnsPatches))
	}
	if dnsPatches[1]["content"] != "legacy.cfargotunnel.com" || dnsPatches[1]["proxied"] != false {
		t.Fatalf("rollback did not restore previous DNS record: %#v", dnsPatches[1])
	}
	if len(configPuts) != 2 {
		t.Fatalf("expected config rollback after late failure, got %d writes", len(configPuts))
	}
}

type testLogger struct {
	buf *bytes.Buffer
}

func logf(buf *bytes.Buffer) *testLogger {
	return &testLogger{buf: buf}
}

func (l *testLogger) Printf(format string, args ...any) {
	l.buf.WriteString(strings.TrimSpace(format))
	l.buf.WriteByte('\n')
}

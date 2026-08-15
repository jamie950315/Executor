package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestSetupCreatesConfigAndSecretsWithDashboardKeyBootstrapURL(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	result, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Domain != "executor.example.com" {
		t.Fatalf("domain = %q", result.Domain)
	}
	if result.RecoveryKey == "" {
		t.Fatal("expected one-time recovery key")
	}
	if !strings.HasPrefix(result.Dashboard, "http://127.0.0.1:8788/?token=") {
		t.Fatalf("dashboard bootstrap URL = %q", result.Dashboard)
	}
	if strings.Contains(result.RecoveryKey, "127.0.0.1") {
		t.Fatalf("recovery key unexpectedly contains a dashboard bootstrap URL: %q", result.RecoveryKey)
	}
	loaded, err := secrets.Load(stateDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(result.Dashboard, loaded.DashboardKey) {
		t.Fatalf("dashboard bootstrap URL should contain dashboard key, got %q", result.Dashboard)
	}
}

func TestRuntimeOperationsUseIndependentController(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	fake := &fakeControl{killResult: control.Result{RecoveryKey: "recovery", URLSecret: "url-secret"}}
	b.loadControl = func(string) (controlRuntime, error) { return fake, nil }
	result, err := b.Kill(context.Background())
	if err != nil || result.RecoveryKey != "recovery" || result.URLSecret != "url-secret" {
		t.Fatalf("Kill result=%#v err=%v", result, err)
	}
	if err := b.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if fake.kills != 1 || fake.resumes != 1 {
		t.Fatalf("controller calls: %#v", fake)
	}
}

func TestDoctorReportsOfflineRuntime(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.AgentAddress = "127.0.0.1:0"
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save isolated config: %v", err)
	}

	result, err := b.Doctor(context.Background(), false)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if result.Healthy {
		t.Fatalf("doctor should be unhealthy when runtime is offline: %#v", result)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "agent" {
			found = true
			if check.OK || !strings.Contains(check.Detail, "offline") {
				t.Fatalf("runtime check = %#v, want offline", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing agent check: %#v", result.Checks)
	}
}

type fakeControl struct {
	killResult control.Result
	killErr    error
	resumeErr  error
	kills      int
	resumes    int
}

func (f *fakeControl) Kill(context.Context) (control.Result, error) {
	f.kills++
	return f.killResult, f.killErr
}

func (f *fakeControl) Resume(context.Context) error {
	f.resumes++
	return f.resumeErr
}

func TestConfigPathUsesStateDir(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	if got, want := b.configPath(), filepath.Join(stateDir, "config.json"); got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
}

func TestBackendHonorsInstallerManagedPathsFromEnvironment(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "custom", "executor.json")
	tokenPath := filepath.Join(t.TempDir(), "tunnel", "executor.token")
	t.Setenv("EXECUTOR_CONFIG_PATH", configPath)
	t.Setenv("CLOUDFLARED_TOKEN_PATH", tokenPath)

	b := newBackend(t.TempDir())
	if got := b.configPath(); got != configPath {
		t.Fatalf("configPath = %q, want installer path %q", got, configPath)
	}
	if got := b.cloudflaredTokenPath(); got != tokenPath {
		t.Fatalf("cloudflaredTokenPath = %q, want installer path %q", got, tokenPath)
	}
}

func TestDefaultStateDirUsesStableInstalledLocation(t *testing.T) {
	t.Setenv("EXECUTOR_STATE_DIR", "")
	if runtime.GOOS == "windows" {
		programData := filepath.Join(t.TempDir(), "ProgramData")
		t.Setenv("ProgramData", programData)
		if got, want := defaultStateDir(), filepath.Join(programData, "Executor"); got != want {
			t.Fatalf("defaultStateDir = %q, want %q", got, want)
		}
		return
	}
	if got, want := defaultStateDir(), "/var/lib/executor"; got != want {
		t.Fatalf("defaultStateDir = %q, want %q", got, want)
	}
}

func TestUnavailableErrorWrapsUnderlyingCause(t *testing.T) {
	t.Parallel()

	err := unavailable("runtime controller", errors.New("not linked"))
	if err == nil || !strings.Contains(err.Error(), "runtime controller unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRotateReturnsRecoveryKeyAndURLSecret(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	fake := &fakeControl{killResult: control.Result{RecoveryKey: "safe-recovery", URLSecret: "safe-url"}}
	b.loadControl = func(string) (controlRuntime, error) { return fake, nil }
	result, err := b.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.RecoveryKey == "" || result.URLSecret == "" {
		t.Fatalf("Rotate returned incomplete credentials: %#v", result)
	}
	if fake.kills != 1 || fake.resumes != 1 {
		t.Fatalf("Rotate did not restart services safely: %#v", fake)
	}
}

func TestSetupConfiguresCloudflareFromSecureTokenFile(t *testing.T) {
	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer top-secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"id": "acct-1", "name": "Primary"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			if got := r.URL.Query().Get("account.id"); got != "acct-1" {
				t.Fatalf("account filter = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{
				{"id": "zone-1", "name": "example.com"},
				{"id": "zone-2", "name": "prod.example.com"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "tunnel-1", "name": "executor", "token": "issued-token"}})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-2/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-2/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "dns-1", "type": "CNAME", "name": "executor.prod.example.com", "content": "tunnel-1.cfargotunnel.com", "proxied": true}})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": "issued-token"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	t.Setenv("EXECUTOR_CLOUDFLARE_API_BASE_URL", server.URL)
	b := newBackend(stateDir)
	result, err := b.Setup(context.Background(), setupOptions{
		Domain:              "executor.prod.example.com",
		CloudflareTokenFile: tokenPath,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Domain != "executor.prod.example.com" {
		t.Fatalf("domain = %q", result.Domain)
	}
	cfg, err := config.Load(filepath.Join(stateDir, "config.json"))
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if cfg.Cloudflare.ZoneID != "zone-2" || cfg.Cloudflare.TunnelID != "tunnel-1" || cfg.Cloudflare.DNSRecordID != "dns-1" {
		t.Fatalf("unexpected Cloudflare metadata: %#v", cfg.Cloudflare)
	}
	if cfg.Cloudflare.TokenFilePath == "" {
		t.Fatalf("missing token file path: %#v", cfg.Cloudflare)
	}
	if strings.Contains(readFile(t, filepath.Join(stateDir, "config.json")), "top-secret-token") {
		t.Fatal("config should not persist API token")
	}
}

func TestSetupRejectsInsecureCloudflareTokenFilePermissions(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newBackend(stateDir)
	if _, err := b.Setup(context.Background(), setupOptions{
		Domain:              "executor.example.com",
		CloudflareTokenFile: tokenPath,
	}); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("err = %v, want 0600 validation", err)
	}
}

func TestSetupDoesNotConsumeOneTimeRecoveryKeyBeforeCloudflareSucceeds(t *testing.T) {
	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"temporary failure"}]}`))
	}))
	defer server.Close()
	t.Setenv("EXECUTOR_CLOUDFLARE_API_BASE_URL", server.URL)

	_, err := newBackend(stateDir).Setup(context.Background(), setupOptions{
		Domain: "executor.example.com", CloudflareTokenFile: tokenPath,
	})
	if err == nil {
		t.Fatal("Setup succeeded despite Cloudflare failure")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "secrets.json")); !os.IsNotExist(statErr) {
		t.Fatalf("one-time secrets were consumed before Cloudflare succeeded: %v", statErr)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

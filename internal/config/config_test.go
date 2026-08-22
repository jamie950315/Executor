package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadMigratesVersionOneToVersionTwoWithStableDashboardDeviceID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := map[string]any{
		"version": 1, "state_dir": dir, "agent_address": "127.0.0.1:8787",
		"dashboard_address": "127.0.0.1:8788", "broker_endpoint": filepath.Join(dir, "broker.sock"),
		"desktop_endpoint": filepath.Join(dir, "desktop.sock"), "audit_retention_hours": 168,
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy config: %v", err)
	}
	if first.Version != 2 || first.UnifiedDashboard.DeviceID == "" || first.UnifiedDashboard.Enrolled || first.UnifiedDashboard.URL != "" {
		t.Fatalf("migrated config = %#v", first)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatalf("reload migrated config: %v", err)
	}
	if second.UnifiedDashboard.DeviceID != first.UnifiedDashboard.DeviceID {
		t.Fatalf("device ID changed across loads: %q != %q", second.UnifiedDashboard.DeviceID, first.UnifiedDashboard.DeviceID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) || !strings.Contains(string(data), first.UnifiedDashboard.DeviceID) {
		t.Fatalf("migration was not durably saved: %s", data)
	}
}

func TestUnifiedDashboardMetadataPersistsWithoutEnrollmentCredential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default(dir)
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.URL != cfg.UnifiedDashboard.URL || !loaded.UnifiedDashboard.Enrolled || loaded.UnifiedDashboard.DeviceID == "" {
		t.Fatalf("unified dashboard metadata = %#v", loaded.UnifiedDashboard)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"enrollment_token", "access_token", "relay_token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("config contains credential field %q: %s", forbidden, data)
		}
	}
}

func TestSaveLoadRoundTripUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	want := Default(dir)
	want.Domain = "executor.example.com"

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Domain != want.Domain || got.AgentAddress != "127.0.0.1:8787" || got.DashboardAddress != "127.0.0.1:8788" {
		t.Fatalf("unexpected round trip: %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("config mode = %o, want 600", gotMode)
		}
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted unsupported version")
	}
}

func TestDefaultEndpointsUseNamedPipesOnWindowsAndSocketsOnUnix(t *testing.T) {
	t.Parallel()
	broker, desktop := defaultEndpoints("windows", `C:\ProgramData\Executor`)
	if broker != `\\.\pipe\executor-broker` || desktop != `\\.\pipe\executor-desktop` {
		t.Fatalf("windows endpoints = %q, %q", broker, desktop)
	}
	broker, desktop = defaultEndpoints("darwin", "/var/lib/executor")
	if broker != filepath.Join("/var/lib/executor", "broker.sock") || desktop != filepath.Join("/var/lib/executor", "desktop.sock") {
		t.Fatalf("unix endpoints = %q, %q", broker, desktop)
	}
}

func TestSaveLoadRoundTripPersistsCloudflareMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	want := Default(dir)
	want.Domain = "executor.example.com"
	want.Cloudflare = CloudflareMetadata{
		AccountID:     "acct-1",
		ZoneID:        "zone-1",
		TunnelID:      "tunnel-1",
		TunnelName:    "executor",
		DNSRecordID:   "dns-1",
		TokenFilePath: filepath.Join(dir, "cloudflared", "executor.token"),
		Hostname:      "executor.example.com",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Cloudflare.TunnelID != "tunnel-1" || got.Cloudflare.DNSRecordID != "dns-1" {
		t.Fatalf("unexpected Cloudflare metadata: %#v", got.Cloudflare)
	}
	if !got.Cloudflare.Complete() {
		t.Fatalf("expected complete Cloudflare metadata: %#v", got.Cloudflare)
	}
}

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
	if broker != "/var/lib/executor/broker.sock" || desktop != "/var/lib/executor/desktop.sock" {
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

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

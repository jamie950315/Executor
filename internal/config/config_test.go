package config

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConcurrentProcessesMigrateOnceWithOneDeviceID(t *testing.T) {
	if os.Getenv("EXECUTOR_CONFIG_MIGRATION_HELPER") == "1" {
		startPath := os.Getenv("EXECUTOR_CONFIG_MIGRATION_START")
		for {
			if _, err := os.Stat(startPath); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
		cfg, err := Load(os.Getenv("EXECUTOR_CONFIG_MIGRATION_PATH"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("EXECUTOR_CONFIG_MIGRATION_RESULT"), []byte(cfg.UnifiedDashboard.DeviceID), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

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
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	startPath := filepath.Join(dir, "start")
	const processCount = 16
	commands := make([]*exec.Cmd, 0, processCount)
	outputs := make([]bytes.Buffer, processCount)
	results := make([]string, 0, processCount)
	for index := 0; index < processCount; index++ {
		resultPath := filepath.Join(dir, "result-"+string(rune('a'+index)))
		command := exec.Command(executable, "-test.run=^TestConcurrentProcessesMigrateOnceWithOneDeviceID$")
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		command.Env = append(os.Environ(),
			"EXECUTOR_CONFIG_MIGRATION_HELPER=1",
			"EXECUTOR_CONFIG_MIGRATION_START="+startPath,
			"EXECUTOR_CONFIG_MIGRATION_PATH="+path,
			"EXECUTOR_CONFIG_MIGRATION_RESULT="+resultPath,
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
		results = append(results, resultPath)
	}
	if err := os.WriteFile(startPath, []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("migration helper failed: %v: %s", err, outputs[index].String())
		}
	}
	deviceID := ""
	for _, resultPath := range results {
		data, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatal(err)
		}
		if deviceID == "" {
			deviceID = string(data)
		} else if string(data) != deviceID {
			t.Fatalf("concurrent migration device IDs differ: %q != %q", data, deviceID)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentVersion || loaded.UnifiedDashboard.DeviceID != deviceID {
		t.Fatalf("persisted migration = %#v, helper device ID = %q", loaded, deviceID)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".config.json.*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("migration left temporary files %v: %v", matches, err)
	}
}

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
	cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.URL != cfg.UnifiedDashboard.URL || !loaded.UnifiedDashboard.Enrolled ||
		loaded.UnifiedDashboard.EnrollmentCleanupFingerprint != cfg.UnifiedDashboard.EnrollmentCleanupFingerprint ||
		loaded.UnifiedDashboard.DeviceID == "" {
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

func TestLoadCanonicalizesDashboardOriginAndRemovesLegacyCleanupBoolean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default(dir)
	cfg.UnifiedDashboard.URL = "https://DASHBOARD.EXAMPLE.test:443/"
	cfg.UnifiedDashboard.Enrolled = true
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	dashboard := document["unified_dashboard"].(map[string]any)
	dashboard["enrollment_cleanup_pending"] = true
	encoded, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.UnifiedDashboard.URL != "https://dashboard.example.test" ||
		loaded.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		t.Fatalf("canonicalized dashboard metadata = %#v", loaded.UnifiedDashboard)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "DASHBOARD.EXAMPLE") ||
		strings.Contains(string(persisted), ":443") ||
		strings.Contains(string(persisted), "enrollment_cleanup_pending") {
		t.Fatalf("load did not durably canonicalize legacy metadata: %s", persisted)
	}
}

func TestCanonicalDashboardOriginDistinguishesOnlyGenuineOriginChanges(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "https://HOST.example:443", want: "https://host.example"},
		{input: "https://HOST.example:8443/", want: "https://host.example:8443"},
		{input: "https://[2001:DB8::1]:443", want: "https://[2001:db8::1]"},
	} {
		got, err := CanonicalDashboardOrigin(test.input)
		if err != nil {
			t.Fatalf("CanonicalDashboardOrigin(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("CanonicalDashboardOrigin(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	first, _ := CanonicalDashboardOrigin("https://HOST.example:443")
	second, _ := CanonicalDashboardOrigin("https://host.example")
	different, _ := CanonicalDashboardOrigin("https://host.example:8443")
	if first != second || first == different {
		t.Fatalf("canonical origins = %q, %q, %q", first, second, different)
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

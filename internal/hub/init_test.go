package hub

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestHubInitIsIsolatedAndIdempotent(t *testing.T) {
	options := InitOptions{StateDir: filepath.Join(t.TempDir(), "hub"), HubID: "pi5", Domain: "hub.example.test", DashboardURL: "https://dashboard.example.test", ListenAddress: "127.0.0.1:28787"}
	created, err := Initialize(options)
	if err != nil {
		t.Fatal(err)
	}
	if created.Existing || created.RecoveryKey == "" {
		t.Fatal("new Hub did not return one-time owner credential")
	}
	cfg, err := config.Load(created.ConfigPath)
	if err != nil || !cfg.HubEnabled {
		t.Fatal("Hub runtime config missing")
	}
	before, err := secrets.Load(options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Initialize(options)
	if err != nil {
		t.Fatal(err)
	}
	after, err := secrets.Load(options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Existing || again.RecoveryKey != "" || before.OAuthKey != after.OAuthKey || before.RecoveryKeyHash != after.RecoveryKeyHash {
		t.Fatal("repeat init changed credentials")
	}
	public, err := json.Marshal(created.Registration)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	json.Unmarshal(public, &record)
	if len(record) != 3 || record["hub_id"] != "pi5" || record["token_hash"] == nil {
		t.Fatal("public registration shape mismatch")
	}
	token, err := os.ReadFile(filepath.Join(options.StateDir, "hub-machine.token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, bytes.TrimSpace(token)) || bytes.Contains(public, []byte(created.RecoveryKey)) {
		t.Fatal("private token exposed as registration")
	}
	options.Domain = "other.example.test"
	if _, err := Initialize(options); err == nil {
		t.Fatal("existing Hub silently retargeted")
	}
}

func TestHubInitRejectsExistingDeviceStateAndUnsafeInputs(t *testing.T) {
	parent := t.TempDir()
	device := filepath.Join(parent, "device")
	if err := os.Mkdir(device, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(device, "config.json"), config.Default(device)); err != nil {
		t.Fatal(err)
	}
	options := InitOptions{StateDir: device, HubID: "pi5", Domain: "hub.example.test", DashboardURL: "https://dashboard.example.test", ListenAddress: "127.0.0.1:28787"}
	if _, err := Initialize(options); err == nil {
		t.Fatal("device state converted into Hub")
	}
	options.StateDir = filepath.Join(parent, "new")
	options.DashboardURL = "http://public.example.test"
	if _, err := Initialize(options); err == nil {
		t.Fatal("plaintext remote origin accepted")
	}
	if _, err := os.Stat(options.StateDir); !os.IsNotExist(err) {
		t.Fatal("invalid init created state")
	}
}

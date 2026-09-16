package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/daemon"
)

func TestRelayDeviceCLIStaysPrivateAndPreservesCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "relay")
	args := []string{"relay-device", "init", "--state-dir", dir}
	var out, stderr bytes.Buffer
	handled, code := runRelayDeviceCommand(args, &out, &stderr)
	if !handled || code != 0 || !strings.Contains(out.String(), "Recovery key (shown once):") {
		t.Fatalf("relay init failed: %s", stderr.String())
	}
	out.Reset()
	_, code = runRelayDeviceCommand(args, &out, &stderr)
	if code != 0 || strings.Contains(out.String(), "Recovery key (shown once):") {
		t.Fatal("repeat changed credentials")
	}
	path := filepath.Join(dir, "config.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Domain = "accidental.example.test"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := daemon.RunAgent(context.Background(), path); err == nil || !strings.Contains(err.Error(), "relay-only") {
		t.Fatal("relay-only mode exposed a public Agent")
	}
}

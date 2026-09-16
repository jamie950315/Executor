package hub

import (
	"github.com/jamie950315/executor/internal/config"
	"path/filepath"
	"testing"
)

func TestRelayDeviceInitRequiresNoPublicEndpoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "device")
	result, err := InitializeRelayDevice(dir, "127.0.0.1:29788")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "" || cfg.HubEnabled || result.RecoveryKey == "" || result.DeviceID != cfg.UnifiedDashboard.DeviceID {
		t.Fatal("relay-only device initialization invalid")
	}
	again, err := InitializeRelayDevice(dir, "127.0.0.1:29788")
	if err != nil || !again.Existing || again.RecoveryKey != "" {
		t.Fatal("repeat changed relay-only credentials")
	}
	if _, err := InitializeRelayDevice(dir, "0.0.0.0:29788"); err == nil {
		t.Fatal("non-loopback rescue listener accepted")
	}
}

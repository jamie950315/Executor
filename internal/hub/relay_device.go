package hub

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/secrets"
)

type RelayDeviceInitResult struct {
	ConfigPath, DeviceID string
	Existing             bool
	RecoveryKey          string `json:"-"`
}

// InitializeRelayDevice provisions a private relay/helper state, without a
// public MCP hostname, Cloudflare tunnel, or OpenAI tunnel client.
func InitializeRelayDevice(directory, listen string) (result RelayDeviceInitResult, err error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || filepath.Dir(directory) == directory {
		return result, errors.New("dedicated absolute relay state is required")
	}
	host, port, err := net.SplitHostPort(listen)
	number, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || ip == nil || !ip.IsLoopback() || number < 1 || number > 65535 {
		return result, errors.New("relay rescue listener must be loopback")
	}
	result.ConfigPath = filepath.Join(directory, "config.json")
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() {
			return result, errors.New("relay state must be a directory")
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			return result, err
		}
		defer root.Close()
		cfgBytes, err := readProtectedHubFile(root, "config.json", 65536)
		if err != nil {
			return result, err
		}
		var cfg config.Config
		if json.Unmarshal(cfgBytes, &cfg) != nil || cfg.Version != config.CurrentVersion || cfg.UnifiedDashboard.DeviceID == "" || !cfg.RelayOnly || cfg.HubEnabled || cfg.Domain != "" || cfg.StateDir != directory || cfg.DashboardAddress != listen {
			return result, errors.New("existing state is not this relay-only device")
		}
		if _, err := secrets.Load(directory); err != nil {
			return result, err
		}
		result.Existing = true
		result.DeviceID = cfg.UnifiedDashboard.DeviceID
		return result, nil
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return result, err
	}
	values, err := secrets.Create(directory)
	result.RecoveryKey = values.RecoveryKey
	if err != nil {
		return result, err
	}
	cfg := config.Default(directory)
	cfg.RelayOnly = true
	cfg.DashboardAddress = listen
	if err := config.Save(result.ConfigPath, cfg); err != nil {
		return result, err
	}
	result.DeviceID = cfg.UnifiedDashboard.DeviceID
	return result, nil
}

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const CurrentVersion = 1

type Config struct {
	Version          int    `json:"version"`
	StateDir         string `json:"state_dir"`
	Domain           string `json:"domain,omitempty"`
	AgentAddress     string `json:"agent_address"`
	DashboardAddress string `json:"dashboard_address"`
	BrokerEndpoint   string `json:"broker_endpoint"`
	DesktopEndpoint  string `json:"desktop_endpoint"`
	AuditRetentionH  int    `json:"audit_retention_hours"`
	URLSecretEnabled bool   `json:"url_secret_enabled,omitempty"`
}

func Default(stateDir string) Config {
	brokerEndpoint, desktopEndpoint := defaultEndpoints(runtime.GOOS, stateDir)
	return Config{
		Version:          CurrentVersion,
		StateDir:         stateDir,
		AgentAddress:     "127.0.0.1:8787",
		DashboardAddress: "127.0.0.1:8788",
		BrokerEndpoint:   brokerEndpoint,
		DesktopEndpoint:  desktopEndpoint,
		AuditRetentionH:  7 * 24,
	}
}

func defaultEndpoints(goos, stateDir string) (string, string) {
	if goos == "windows" {
		return `\\.\pipe\executor-broker`, `\\.\pipe\executor-desktop`
	}
	return filepath.Join(stateDir, "broker.sock"), filepath.Join(stateDir, "desktop.sock")
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Version != CurrentVersion {
		return Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	if cfg.Version != CurrentVersion {
		return errors.New("cannot save unsupported config version")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

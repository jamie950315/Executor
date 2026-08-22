package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const CurrentVersion = 2

type Config struct {
	Version          int                      `json:"version"`
	StateDir         string                   `json:"state_dir"`
	Domain           string                   `json:"domain,omitempty"`
	AgentAddress     string                   `json:"agent_address"`
	DashboardAddress string                   `json:"dashboard_address"`
	BrokerEndpoint   string                   `json:"broker_endpoint"`
	DesktopEndpoint  string                   `json:"desktop_endpoint"`
	AuditRetentionH  int                      `json:"audit_retention_hours"`
	URLSecretEnabled bool                     `json:"url_secret_enabled,omitempty"`
	Cloudflare       CloudflareMetadata       `json:"cloudflare,omitempty"`
	UnifiedDashboard UnifiedDashboardMetadata `json:"unified_dashboard"`
}

type UnifiedDashboardMetadata struct {
	URL      string `json:"url,omitempty"`
	DeviceID string `json:"device_id"`
	Enrolled bool   `json:"enrolled,omitempty"`
}

type CloudflareMetadata struct {
	AccountID     string `json:"account_id,omitempty"`
	ZoneID        string `json:"zone_id,omitempty"`
	TunnelID      string `json:"tunnel_id,omitempty"`
	TunnelName    string `json:"tunnel_name,omitempty"`
	DNSRecordID   string `json:"dns_record_id,omitempty"`
	TokenFilePath string `json:"token_file_path,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
}

func (m CloudflareMetadata) Complete() bool {
	return m.AccountID != "" &&
		m.ZoneID != "" &&
		m.TunnelID != "" &&
		m.TunnelName != "" &&
		m.DNSRecordID != "" &&
		m.TokenFilePath != "" &&
		m.Hostname != ""
}

func Default(stateDir string) Config {
	brokerEndpoint, desktopEndpoint := defaultEndpoints(runtime.GOOS, stateDir)
	deviceID, _ := randomDeviceID()
	return Config{
		Version:          CurrentVersion,
		StateDir:         stateDir,
		AgentAddress:     "127.0.0.1:8787",
		DashboardAddress: "127.0.0.1:8788",
		BrokerEndpoint:   brokerEndpoint,
		DesktopEndpoint:  desktopEndpoint,
		AuditRetentionH:  7 * 24,
		UnifiedDashboard: UnifiedDashboardMetadata{DeviceID: deviceID},
	}
}

func defaultEndpoints(goos, stateDir string) (string, string) {
	if goos == "windows" {
		return `\\.\pipe\executor-broker`, `\\.\pipe\executor-desktop`
	}
	return filepath.Join(stateDir, "broker.sock"), filepath.Join(stateDir, "desktop.sock")
}

func Load(path string) (Config, error) {
	release, err := acquireConfigLock(path)
	if err != nil {
		return Config{}, err
	}
	defer release()
	return loadUnlocked(path)
}

func loadUnlocked(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Version == 1 {
		if err := migrateV1ToV2(&cfg); err != nil {
			return Config{}, err
		}
		if err := saveUnlocked(path, cfg); err != nil {
			return Config{}, fmt.Errorf("save migrated config: %w", err)
		}
	} else if cfg.Version != CurrentVersion {
		return Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if err := validateUnifiedDashboard(cfg.UnifiedDashboard); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := acquireConfigLock(path)
	if err != nil {
		return err
	}
	defer release()
	return saveUnlocked(path, cfg)
}

func saveUnlocked(path string, cfg Config) error {
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	if cfg.Version != CurrentVersion {
		return errors.New("cannot save unsupported config version")
	}
	if cfg.UnifiedDashboard.DeviceID == "" {
		deviceID, err := randomDeviceID()
		if err != nil {
			return err
		}
		cfg.UnifiedDashboard.DeviceID = deviceID
	}
	if err := validateUnifiedDashboard(cfg.UnifiedDashboard); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	existing, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmp)
	}()
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return err
	}
	if existing != nil {
		if err := preserveFileOwnership(tmpFile, existing); err != nil {
			return err
		}
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	closed = true
	if err := replaceFileDurable(tmp, path); err != nil {
		return err
	}
	return nil
}

func migrateV1ToV2(cfg *Config) error {
	if cfg == nil || cfg.Version != 1 {
		return errors.New("invalid config migration")
	}
	deviceID, err := randomDeviceID()
	if err != nil {
		return err
	}
	cfg.Version = CurrentVersion
	cfg.UnifiedDashboard = UnifiedDashboardMetadata{DeviceID: deviceID}
	return nil
}

func randomDeviceID() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate dashboard device identity")
	}
	return "device-" + base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func validateUnifiedDashboard(metadata UnifiedDashboardMetadata) error {
	if strings.TrimSpace(metadata.DeviceID) == "" || len(metadata.DeviceID) > 256 {
		return errors.New("invalid unified dashboard metadata")
	}
	if metadata.URL == "" {
		if metadata.Enrolled {
			return errors.New("invalid unified dashboard metadata")
		}
		return nil
	}
	parsed, err := url.Parse(metadata.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("invalid unified dashboard metadata")
	}
	return nil
}

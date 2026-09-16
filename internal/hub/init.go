package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

type InitOptions struct {
	StateDir, HubID, Domain, DashboardURL, ListenAddress string
	ResourceAliases                                      []string
}
type PublicRegistration struct {
	HubID     string             `json:"hub_id"`
	PublicKey relay.PublicKeyJWK `json:"public_key"`
	TokenHash string             `json:"token_hash"`
}
type InitResult struct {
	ConfigPath   string
	Registration PublicRegistration
	Existing     bool
	RecoveryKey  string `json:"-"`
}

// Initialize creates only dedicated Hub state. It installs no services, opens
// no public ports, and makes no remote API requests. On a failure after secret
// generation, the returned RecoveryKey must still be delivered to the owner.
func Initialize(options InitOptions) (result InitResult, err error) {
	options.Domain = strings.ToLower(options.Domain)
	if !filepath.IsAbs(options.StateDir) || filepath.Clean(options.StateDir) != options.StateDir || filepath.Dir(options.StateDir) == options.StateDir || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(options.HubID) {
		return result, errors.New("explicit dedicated Hub state and valid Hub ID are required")
	}
	parsed, parseErr := url.Parse("https://" + options.Domain)
	if parseErr != nil || parsed.Host != options.Domain || parsed.Hostname() != options.Domain || len(options.Domain) > 253 || !strings.Contains(options.Domain, ".") {
		return result, errors.New("a canonical Hub domain is required")
	}
	for _, label := range strings.Split(options.Domain, ".") {
		if !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(label) {
			return result, errors.New("invalid Hub domain")
		}
	}
	host, port, parseErr := net.SplitHostPort(options.ListenAddress)
	number, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if parseErr != nil || portErr != nil || ip == nil || !ip.IsLoopback() || number < 1 || number > 65535 {
		return result, errors.New("Hub listener must be an explicit loopback IP and port")
	}
	transport, parseErr := NewDashboardRelay(options.DashboardURL, "validation-placeholder", nil)
	if parseErr != nil {
		return result, parseErr
	}
	options.DashboardURL = transport.origin
	if err := config.ValidateOAuthResourceAliases(options.ResourceAliases); err != nil {
		return result, err
	}
	if info, statErr := os.Lstat(options.StateDir); statErr == nil {
		if !info.IsDir() {
			return result, errors.New("existing Hub state is not a regular directory")
		}
		cfg, manifest, registration, loadErr := inspectHubState(options.StateDir)
		if loadErr != nil {
			return result, loadErr
		}
		if cfg.Domain != options.Domain || cfg.AgentAddress != options.ListenAddress || manifest.HubID != options.HubID || manifest.DashboardURL != options.DashboardURL || !equalAliases(cfg.OAuthResourceAliases, options.ResourceAliases) {
			return result, errors.New("existing Hub configuration differs; no changes made")
		}
		return InitResult{ConfigPath: filepath.Join(options.StateDir, "config.json"), Registration: registration, Existing: true}, nil
	} else if !os.IsNotExist(statErr) {
		return result, errors.New("Hub state unavailable")
	}
	if err := os.Mkdir(options.StateDir, 0700); err != nil {
		return result, errors.New("cannot create dedicated Hub state")
	}
	values, err := secrets.Create(options.StateDir)
	result.RecoveryKey = values.RecoveryKey
	if err != nil {
		return result, errors.New("Hub secret initialization failed; partial state retained")
	}
	root, err := os.OpenRoot(options.StateDir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return result, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	if err := writeHubNewFile(root, "hub-machine.token", []byte(token)); err != nil {
		return result, err
	}
	manifest := RuntimeConfig{Version: 1, HubID: options.HubID, DashboardURL: options.DashboardURL, MachineTokenFile: "hub-machine.token"}
	encoded, _ := json.Marshal(manifest)
	if err := writeHubNewFile(root, "hub.json", encoded); err != nil {
		return result, err
	}
	cfg := config.Default(options.StateDir)
	cfg.HubEnabled = true
	cfg.Domain = options.Domain
	cfg.AgentAddress = options.ListenAddress
	cfg.OAuthResourceAliases = append([]string(nil), options.ResourceAliases...)
	result.ConfigPath = filepath.Join(options.StateDir, "config.json")
	if err := config.Save(result.ConfigPath, cfg); err != nil {
		return result, err
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256([]byte(token))
	result.Registration = PublicRegistration{HubID: options.HubID, PublicKey: identity.PublicJWK(), TokenHash: hex.EncodeToString(hash[:])}
	return result, nil
}

func ReadRegistration(stateDir string) (PublicRegistration, error) {
	_, _, registration, err := inspectHubState(stateDir)
	return registration, err
}

func inspectHubState(stateDir string) (config.Config, RuntimeConfig, PublicRegistration, error) {
	var cfg config.Config
	var manifest RuntimeConfig
	var registration PublicRegistration
	fail := errors.New("existing state is not a complete protected Hub; no changes made")
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return cfg, manifest, registration, fail
	}
	defer root.Close()
	cfgBytes, err := readProtectedHubFile(root, "config.json", 65536)
	if err != nil {
		return cfg, manifest, registration, fail
	}
	if json.Unmarshal(cfgBytes, &cfg) != nil || cfg.Version != config.CurrentVersion || !cfg.HubEnabled || cfg.StateDir != stateDir {
		return cfg, manifest, registration, fail
	}
	manifestBytes, err := readProtectedHubFile(root, "hub.json", 65536)
	if err != nil || json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Version != 1 {
		return cfg, manifest, registration, fail
	}
	if !validStateReference(manifest.MachineTokenFile) {
		return cfg, manifest, registration, fail
	}
	token, err := readProtectedHubFile(root, manifest.MachineTokenFile, 4096)
	if err != nil {
		return cfg, manifest, registration, fail
	}
	values, err := secrets.Load(stateDir)
	if err != nil {
		return cfg, manifest, registration, fail
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return cfg, manifest, registration, fail
	}
	if _, err := LoadRouter(stateDir, identity); err != nil {
		return cfg, manifest, registration, fail
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(string(token))))
	return cfg, manifest, PublicRegistration{manifest.HubID, identity.PublicJWK(), hex.EncodeToString(hash[:])}, nil
}

func writeHubNewFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_SYNC, 0600)
	if err != nil {
		return errors.New("Hub state write failed; partial state retained")
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return errors.New("Hub state write failed; partial state retained")
	}
	return file.Sync()
}
func equalAliases(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

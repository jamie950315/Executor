package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/cli"
	"github.com/jamie950315/executor/internal/cloudflare"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/doctor"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/secrets"
)

type backend struct {
	stateDir    string
	loadControl func(string) (controlRuntime, error)
}

type controlRuntime interface {
	Kill(context.Context) (control.Result, error)
	Resume(context.Context) error
}

type setupOptions = cli.SetupOptions

func newBackend(stateDir string) *backend {
	return &backend{stateDir: stateDir, loadControl: func(path string) (controlRuntime, error) { return control.Load(path) }}
}

func defaultStateDir() string {
	if stateDir := os.Getenv("EXECUTOR_STATE_DIR"); stateDir != "" {
		return stateDir
	}
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return filepath.Join(programData, "Executor")
	}
	return "/var/lib/executor"
}

func (b *backend) Setup(ctx context.Context, options cli.SetupOptions) (cli.SetupResult, error) {
	configPath := b.configPath()
	secretsPath := b.stateDir
	result := cli.SetupResult{Domain: options.Domain, MCPURL: mcpURL(options.Domain)}
	var cfg config.Config

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		cfg = config.Default(b.stateDir)
		cfg.Domain = options.Domain
		if err := config.Save(configPath, cfg); err != nil {
			return cli.SetupResult{}, err
		}
	} else if err == nil {
		var loadErr error
		cfg, loadErr = config.Load(configPath)
		if loadErr != nil {
			return cli.SetupResult{}, loadErr
		}
		if cfg.Domain == "" && options.Domain != "" {
			cfg.Domain = options.Domain
			if err := config.Save(configPath, cfg); err != nil {
				return cli.SetupResult{}, err
			}
		}
		result.Domain = cfg.Domain
		result.MCPURL = mcpURL(cfg.Domain)
	} else {
		return cli.SetupResult{}, err
	}

	if err := b.setupCloudflare(ctx, &cfg, options); err != nil {
		return cli.SetupResult{}, err
	}
	if err := config.Save(configPath, cfg); err != nil {
		return cli.SetupResult{}, err
	}
	created, err := secrets.Create(secretsPath)
	if err == nil {
		result.RecoveryKey = created.RecoveryKey
		result.Dashboard = dashboardURL(cfg.DashboardAddress, created.DashboardKey)
	} else if !strings.Contains(err.Error(), "already exist") {
		return cli.SetupResult{}, err
	}
	return result, nil
}

func (b *backend) Status(ctx context.Context) (cli.Status, error) {
	cfg, err := config.Load(b.configPath())
	if err != nil {
		return cli.Status{}, err
	}
	values, err := secrets.Load(b.stateDir)
	if err != nil {
		return cli.Status{}, err
	}
	status := cli.Status{State: "degraded", Domain: cfg.Domain, MCPURL: mcpURL(cfg.Domain)}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "disabled")); err == nil {
		status.State = "disabled"
	}
	status.Broker = probeIPC(ctx, cfg.BrokerEndpoint, values.BrokerIPCKey)
	status.Desktop = probeIPC(ctx, cfg.DesktopEndpoint, values.DesktopIPCKey)
	status.Agent = probeTCP(ctx, cfg.AgentAddress)
	if cfg.Cloudflare.Complete() {
		if _, err := os.Stat(cfg.Cloudflare.TokenFilePath); err == nil {
			status.Tunnel = "configured"
		} else {
			status.Tunnel = "token-missing"
		}
	} else {
		status.Tunnel = "not-configured"
	}
	if status.State != "disabled" && status.Agent == "online" && status.Broker == "online" {
		status.State = "armed"
	}
	return status, nil
}

func (b *backend) Kill(ctx context.Context) (cli.RotateResult, error) {
	controller, err := b.loadControl(b.configPath())
	if err != nil {
		return cli.RotateResult{}, err
	}
	result, err := controller.Kill(ctx)
	return cli.RotateResult{RecoveryKey: result.RecoveryKey, URLSecret: result.URLSecret, Dashboard: result.Dashboard}, err
}

func (b *backend) Resume(ctx context.Context) error {
	controller, err := b.loadControl(b.configPath())
	if err != nil {
		return err
	}
	return controller.Resume(ctx)
}

func (b *backend) Rotate(ctx context.Context) (cli.RotateResult, error) {
	rotated, err := b.Kill(ctx)
	if err != nil {
		return rotated, err
	}
	if err := b.Resume(ctx); err != nil {
		return rotated, err
	}
	return rotated, nil
}

func (b *backend) Doctor(ctx context.Context, _ bool) (cli.DoctorResult, error) {
	status, statusErr := b.Status(ctx)
	checkers := []doctor.Checker{
		fileCheck{name: "config", path: b.configPath()},
		fileCheck{name: "secrets", path: filepath.Join(b.stateDir, "secrets.json")},
		staticFailureCheck{name: "agent", err: onlineError(status.Agent, statusErr)},
		staticFailureCheck{name: "broker", err: onlineError(status.Broker, statusErr)},
		staticFailureCheck{name: "desktop", err: onlineError(status.Desktop, nil)},
		staticFailureCheck{name: "tunnel", err: configuredError(status.Tunnel)},
	}
	result := doctor.Run(ctx, checkers)
	out := cli.DoctorResult{Healthy: result.Healthy, Checks: make([]cli.Check, 0, len(result.Checks))}
	for _, check := range result.Checks {
		detail := check.Detail
		if check.Error != "" {
			detail = check.Error
		}
		out.Checks = append(out.Checks, cli.Check{Name: check.Name, OK: check.OK, Detail: detail})
	}
	return out, nil
}

func (b *backend) EnableURLSecret(ctx context.Context) (cli.RotateResult, error) {
	rotated, err := b.Kill(ctx)
	if err != nil {
		return rotated, err
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		return rotated, err
	}
	rotated.Endpoint = "https://" + cfg.Domain + "/" + rotated.URLSecret + "/mcp"
	cfg.URLSecretEnabled = true
	if err := config.Save(b.configPath(), cfg); err != nil {
		return rotated, err
	}
	if err := b.Resume(ctx); err != nil {
		return rotated, err
	}
	return rotated, nil
}

func (b *backend) configPath() string {
	if path := strings.TrimSpace(os.Getenv("EXECUTOR_CONFIG_PATH")); path != "" {
		return path
	}
	return filepath.Join(b.stateDir, "config.json")
}

func (b *backend) cloudflaredTokenPath() string {
	if path := strings.TrimSpace(os.Getenv("CLOUDFLARED_TOKEN_PATH")); path != "" {
		return path
	}
	return defaultManagedCloudflaredTokenPath(b.stateDir)
}

func mcpURL(domain string) string {
	if domain == "" {
		return ""
	}
	return "https://" + domain + "/mcp"
}

func dashboardURL(address, token string) string {
	if address == "" || token == "" {
		return ""
	}
	return "http://" + address + "/?token=" + token
}

func (b *backend) setupCloudflare(ctx context.Context, cfg *config.Config, options cli.SetupOptions) error {
	if options.CloudflareTokenFile == "" {
		return nil
	}

	apiToken, err := readCloudflareAPITokenFile(options.CloudflareTokenFile)
	if err != nil {
		return err
	}

	client := cloudflare.NewClient(cloudflareAPIBaseURL(), apiToken, cloudflare.WithHTTPClient(http.DefaultClient))
	accountID, err := selectCloudflareAccount(ctx, client, options.CloudflareAccountID, cfg.Cloudflare.AccountID)
	if err != nil {
		return err
	}
	zoneID, hostname, err := selectCloudflareZone(ctx, client, accountID, cfg.Domain, options.CloudflareZoneID, cfg.Cloudflare.ZoneID)
	if err != nil {
		return err
	}
	tunnelName := strings.TrimSpace(options.CloudflareTunnelName)
	if tunnelName == "" {
		tunnelName = cfg.Cloudflare.TunnelName
	}
	if tunnelName == "" {
		tunnelName = "executor"
	}
	tokenFilePath := cfg.Cloudflare.TokenFilePath
	if tokenFilePath == "" {
		tokenFilePath = b.cloudflaredTokenPath()
	}

	result, err := client.Apply(ctx, cloudflare.DeploymentRequest{
		AccountID:             accountID,
		ZoneID:                zoneID,
		TunnelName:            tunnelName,
		Hostname:              hostname,
		LocalServiceURL:       "http://" + cfg.AgentAddress,
		TokenFilePath:         tokenFilePath,
		CloudflaredBinaryPath: "cloudflared",
	})
	if err != nil {
		return err
	}

	cfg.Cloudflare = config.CloudflareMetadata{
		AccountID:     accountID,
		ZoneID:        zoneID,
		TunnelID:      result.Tunnel.ID,
		TunnelName:    result.Tunnel.Name,
		DNSRecordID:   result.DNS.ID,
		TokenFilePath: result.TokenFilePath,
		Hostname:      hostname,
	}
	return nil
}

func readCloudflareAPITokenFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("cloudflare token file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("cloudflare token file must be a regular file: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != fs.FileMode(0o600) {
		return "", fmt.Errorf("cloudflare token file must have mode 0600: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("cloudflare token file is empty")
	}
	return token, nil
}

func cloudflareAPIBaseURL() string {
	if baseURL := strings.TrimSpace(os.Getenv("EXECUTOR_CLOUDFLARE_API_BASE_URL")); baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

func defaultManagedCloudflaredTokenPath(stateDir string) string {
	if path := strings.TrimSpace(os.Getenv("EXECUTOR_CLOUDFLARED_TOKEN_PATH")); path != "" {
		return path
	}
	if path := strings.TrimSpace(os.Getenv("CLOUDFLARED_TOKEN_PATH")); path != "" {
		return path
	}
	return filepath.Join(stateDir, "cloudflared", "executor.token")
}

func selectCloudflareAccount(ctx context.Context, client *cloudflare.Client, explicitID, existingID string) (string, error) {
	if explicitID = strings.TrimSpace(explicitID); explicitID != "" {
		return explicitID, nil
	}
	if existingID = strings.TrimSpace(existingID); existingID != "" {
		return existingID, nil
	}
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) != 1 {
		return "", fmt.Errorf("cloudflare account selection requires exactly one account, found %d", len(accounts))
	}
	return accounts[0].ID, nil
}

func selectCloudflareZone(ctx context.Context, client *cloudflare.Client, accountID, hostname, explicitID, existingID string) (string, string, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", "", errors.New("domain is required for cloudflare setup")
	}
	if explicitID = strings.TrimSpace(explicitID); explicitID != "" {
		return explicitID, hostname, nil
	}
	if existingID = strings.TrimSpace(existingID); existingID != "" {
		return existingID, hostname, nil
	}
	zones, err := client.ListZones(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	type match struct {
		id   string
		name string
	}
	var matches []match
	for _, zone := range zones {
		if hostname == zone.Name || strings.HasSuffix(hostname, "."+zone.Name) {
			matches = append(matches, match{id: zone.ID, name: zone.Name})
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no Cloudflare zone matches hostname %s", hostname)
	}
	sort.Slice(matches, func(i, j int) bool { return len(matches[i].name) > len(matches[j].name) })
	return matches[0].id, hostname, nil
}

type fileCheck struct {
	name string
	path string
}

func (f fileCheck) Name() string { return f.name }

func (f fileCheck) Run(context.Context) (string, error) {
	info, err := os.Stat(f.path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("present (%s, %s)", info.Mode().Perm(), time.Unix(0, info.ModTime().UnixNano()).UTC().Format(time.RFC3339)), nil
}

type staticFailureCheck struct {
	name string
	err  error
}

func (s staticFailureCheck) Name() string { return s.name }

func (s staticFailureCheck) Run(context.Context) (string, error) {
	if s.err == nil {
		return "", nil
	}
	return s.err.Error(), s.err
}

func unavailable(component string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%s unavailable: %w", component, cause)
	}
	return fmt.Errorf("%s unavailable", component)
}

func probeIPC(ctx context.Context, endpoint, key string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var status desktop.RPCDeviceStatus
	if err := ipc.NewRPCClient(endpoint, []byte(key)).Call(probeCtx, desktop.RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		return "offline"
	}
	return "online"
}

func probeTCP(ctx context.Context, address string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", address)
	if err != nil {
		return "offline"
	}
	_ = connection.Close()
	return "online"
}

func onlineError(state string, cause error) error {
	if cause != nil {
		return cause
	}
	if state != "online" {
		return fmt.Errorf("runtime is %s", state)
	}
	return nil
}

func configuredError(state string) error {
	if state != "configured" {
		return fmt.Errorf("tunnel is %s", state)
	}
	return nil
}

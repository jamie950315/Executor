package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

type EnrollOptions struct {
	ConfigPath      string
	TokenFile       string
	HTTPClient      *http.Client
	ExecutorVersion string
}

type enrollmentRequest struct {
	DeviceID   string             `json:"device_id"`
	Name       string             `json:"name"`
	Platform   string             `json:"platform"`
	Arch       string             `json:"arch"`
	Version    string             `json:"version"`
	MCPURL     string             `json:"mcp_url"`
	PublicJWK  relay.PublicKeyJWK `json:"public_jwk"`
	Generation uint64             `json:"generation"`
}

func Enroll(ctx context.Context, options EnrollOptions) error {
	if ctx == nil || options.ConfigPath == "" || options.TokenFile == "" || strings.TrimSpace(options.ExecutorVersion) == "" {
		return errors.New("invalid dashboard enrollment options")
	}
	cfg, err := config.Load(options.ConfigPath)
	if err != nil || cfg.UnifiedDashboard.URL == "" || cfg.UnifiedDashboard.DeviceID == "" {
		return errors.New("dashboard enrollment is not configured")
	}
	values, err := secrets.Load(cfg.StateDir)
	if err != nil {
		return errors.New("dashboard enrollment credentials unavailable")
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return errors.New("dashboard enrollment credentials unavailable")
	}
	token, err := readEnrollmentToken(options.TokenFile)
	if err != nil {
		return err
	}
	defer clear(token)
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "Executor Device"
	}
	body, err := json.Marshal(enrollmentRequest{
		DeviceID: cfg.UnifiedDashboard.DeviceID, Name: hostname, Platform: runtime.GOOS, Arch: runtime.GOARCH,
		Version: options.ExecutorVersion, MCPURL: configuredMCPURL(cfg.Domain), PublicJWK: identity.PublicJWK(),
		Generation: values.Generation,
	})
	if err != nil {
		return errors.New("dashboard enrollment request failed")
	}
	endpoint, err := enrollmentEndpoint(cfg.UnifiedDashboard.URL)
	if err != nil {
		return errors.New("dashboard enrollment is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("dashboard enrollment request failed")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("dashboard enrollment request failed")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return errors.New("dashboard enrollment rejected")
	}
	if err := os.Remove(options.TokenFile); err != nil {
		return errors.New("dashboard enrollment token cleanup failed")
	}
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(options.ConfigPath, cfg); err != nil {
		return errors.New("dashboard enrollment state save failed")
	}
	return nil
}

func readEnrollmentToken(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != fs.FileMode(0o600) {
		return nil, errors.New("dashboard enrollment token file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("dashboard enrollment token file is invalid")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 513))
	if err != nil || len(data) > 512 {
		clear(data)
		return nil, errors.New("dashboard enrollment token file is invalid")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.IndexFunc(trimmed, func(value rune) bool { return value <= ' ' || value == 127 }) >= 0 {
		clear(data)
		return nil, errors.New("dashboard enrollment token file is invalid")
	}
	token := append([]byte(nil), trimmed...)
	clear(data)
	return token, nil
}

func enrollmentEndpoint(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid dashboard endpoint")
	}
	parsed.Path = "/api/device/enroll"
	return parsed.String(), nil
}

func configuredMCPURL(domain string) string {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" {
		return ""
	}
	return "https://" + domain + "/mcp"
}

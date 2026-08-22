package relayclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

type enrollmentOperations struct {
	SaveConfig  func(string, config.Config) error
	RemoveToken func(string) error
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
	return enrollWithOperations(ctx, options, enrollmentOperations{
		SaveConfig: config.Save, RemoveToken: os.Remove,
	})
}

func enrollWithOperations(ctx context.Context, options EnrollOptions, operations enrollmentOperations) error {
	if ctx == nil || options.ConfigPath == "" || options.TokenFile == "" || strings.TrimSpace(options.ExecutorVersion) == "" {
		return errors.New("invalid dashboard enrollment options")
	}
	if operations.SaveConfig == nil || operations.RemoveToken == nil {
		return errors.New("invalid dashboard enrollment options")
	}
	cfg, err := config.Load(options.ConfigPath)
	if err != nil || cfg.UnifiedDashboard.URL == "" || cfg.UnifiedDashboard.DeviceID == "" {
		return errors.New("dashboard enrollment is not configured")
	}
	if cfg.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		if _, err := os.Stat(options.TokenFile); errors.Is(err, os.ErrNotExist) {
			cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = ""
			if err := operations.SaveConfig(options.ConfigPath, cfg); err != nil {
				return errors.New("dashboard enrollment state save failed")
			}
			return nil
		} else if err != nil {
			return errors.New("dashboard enrollment token file is invalid")
		}
	}
	values, err := secrets.Load(cfg.StateDir)
	if err != nil {
		return errors.New("dashboard enrollment credentials unavailable")
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return errors.New("dashboard enrollment credentials unavailable")
	}
	var token []byte
	if cfg.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		token, err = readEnrollmentToken(options.TokenFile)
		if err != nil {
			return err
		}
		fingerprint, err := enrollmentCleanupFingerprint(identity, token)
		if err != nil {
			clear(token)
			return errors.New("dashboard enrollment credentials unavailable")
		}
		if hmac.Equal([]byte(fingerprint), []byte(cfg.UnifiedDashboard.EnrollmentCleanupFingerprint)) {
			clear(token)
			if err := cleanupEnrollmentToken(options.TokenFile, operations.RemoveToken); err != nil {
				return err
			}
			cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = ""
			if err := operations.SaveConfig(options.ConfigPath, cfg); err != nil {
				return errors.New("dashboard enrollment state save failed")
			}
			return nil
		}
		cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = ""
		if err := operations.SaveConfig(options.ConfigPath, cfg); err != nil {
			clear(token)
			return errors.New("dashboard enrollment state save failed")
		}
	}
	endpoint, err := enrollmentEndpoint(cfg.UnifiedDashboard.URL)
	if err != nil {
		clear(token)
		return errors.New("dashboard enrollment is not configured")
	}
	origin, err := dashboardOrigin(cfg.UnifiedDashboard.URL)
	if err != nil {
		clear(token)
		return errors.New("dashboard enrollment is not configured")
	}
	if token == nil {
		token, err = readEnrollmentToken(options.TokenFile)
	}
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("dashboard enrollment request failed")
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
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
	fingerprint, err := enrollmentCleanupFingerprint(identity, token)
	if err != nil {
		return errors.New("dashboard enrollment credentials unavailable")
	}
	cfg.UnifiedDashboard.Enrolled = true
	cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = fingerprint
	if err := operations.SaveConfig(options.ConfigPath, cfg); err != nil {
		return errors.New("dashboard enrollment state save failed")
	}
	if err := cleanupEnrollmentToken(options.TokenFile, operations.RemoveToken); err != nil {
		return err
	}
	cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = ""
	if err := operations.SaveConfig(options.ConfigPath, cfg); err != nil {
		return errors.New("dashboard enrollment state save failed")
	}
	return nil
}

func cleanupEnrollmentToken(path string, remove func(string) error) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("dashboard enrollment token cleanup failed")
	}
	if !info.Mode().IsRegular() {
		return errors.New("dashboard enrollment token cleanup failed")
	}
	if err := remove(path); err != nil {
		return errors.New("dashboard enrollment token cleanup failed")
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

func dashboardOrigin(base string) (string, error) {
	return config.CanonicalDashboardOrigin(base)
}

func enrollmentCleanupFingerprint(identity *relay.DeviceIdentity, token []byte) (string, error) {
	key, err := identity.MarshalPrivateJWK()
	if err != nil {
		return "", err
	}
	defer clear(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(token)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func configuredMCPURL(domain string) string {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" {
		return ""
	}
	return "https://" + domain + "/mcp"
}

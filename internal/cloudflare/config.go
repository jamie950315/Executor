package cloudflare

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func BuildCloudflaredConfig(tunnelID, credentialsPath string, rules []IngressRule) (string, error) {
	if tunnelID == "" {
		return "", errors.New("tunnel ID is required")
	}
	if credentialsPath == "" {
		return "", errors.New("credentials path is required")
	}
	ingress, err := buildIngressPayload(rules)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("tunnel: " + tunnelID + "\n")
	b.WriteString("credentials-file: " + credentialsPath + "\n")
	b.WriteString("ingress:\n")
	for _, rule := range ingress {
		if hostname, ok := rule["hostname"].(string); ok && hostname != "" {
			b.WriteString(fmt.Sprintf("  - hostname: %s\n", hostname))
			b.WriteString(fmt.Sprintf("    service: %s\n", rule["service"]))
			continue
		}
		b.WriteString(fmt.Sprintf("  - service: %s\n", rule["service"]))
	}
	return b.String(), nil
}

func BuildCloudflaredRunCommand(binaryPath, tokenFilePath string) (string, error) {
	if binaryPath == "" {
		return "", errors.New("cloudflared binary path is required")
	}
	if tokenFilePath == "" {
		return "", errors.New("token file path is required")
	}
	return fmt.Sprintf("%s tunnel run --token-file %s", binaryPath, tokenFilePath), nil
}

func WriteTunnelTokenFile(path, token string) error {
	if path == "" {
		return errors.New("token file path is required")
	}
	if token == "" {
		return errors.New("tunnel token is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
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

func buildIngressPayload(rules []IngressRule) ([]map[string]any, error) {
	if len(rules) == 0 {
		return nil, errors.New("at least one ingress rule is required")
	}
	out := make([]map[string]any, 0, len(rules)+1)
	for _, rule := range rules {
		if rule.Hostname == "" {
			return nil, errors.New("hostname is required")
		}
		service, err := normalizeLoopbackURL(rule.Service)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"hostname":      rule.Hostname,
			"service":       service,
			"originRequest": map[string]any{},
		})
	}
	out = append(out, map[string]any{"service": "http_status:404"})
	return out, nil
}

func normalizeLoopbackURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("service URL is required")
	}
	if strings.HasPrefix(raw, "http_status:") {
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("service URL must include scheme and host: %s", raw)
	}
	host := parsed.Hostname()
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("service URL must stay on loopback: %s", raw)
	}
	port := parsed.Port()
	if port != "" {
		parsed.Host = net.JoinHostPort("127.0.0.1", port)
	} else {
		parsed.Host = "127.0.0.1"
	}
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	default:
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
}

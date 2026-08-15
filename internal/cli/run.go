package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

type SetupOptions struct {
	Domain               string
	CloudflareTokenFile  string
	CloudflareAccountID  string
	CloudflareZoneID     string
	CloudflareTunnelName string
}

type SetupResult struct {
	Domain      string `json:"domain"`
	MCPURL      string `json:"mcp_url"`
	Dashboard   string `json:"dashboard,omitempty"`
	RecoveryKey string `json:"recovery_key"`
}

type Status struct {
	State     string `json:"state"`
	Domain    string `json:"domain"`
	MCPURL    string `json:"mcp_url,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Broker    string `json:"broker,omitempty"`
	Desktop   string `json:"desktop,omitempty"`
	Dashboard string `json:"dashboard,omitempty"`
	Tunnel    string `json:"tunnel,omitempty"`
}

type RotateResult struct {
	URLSecret   string `json:"url_secret"`
	RecoveryKey string `json:"recovery_key"`
	Dashboard   string `json:"dashboard,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type DoctorResult struct {
	Healthy bool    `json:"healthy"`
	Checks  []Check `json:"checks"`
}

type Backend interface {
	Setup(context.Context, SetupOptions) (SetupResult, error)
	Status(context.Context) (Status, error)
	Kill(context.Context) (RotateResult, error)
	Resume(context.Context) error
	Rotate(context.Context) (RotateResult, error)
	Doctor(context.Context, bool) (DoctorResult, error)
	EnableURLSecret(context.Context) (RotateResult, error)
}

func Run(ctx context.Context, args []string, backend Backend, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "setup":
		set := flag.NewFlagSet("setup", flag.ContinueOnError)
		set.SetOutput(stderr)
		domain := set.String("domain", "", "public Executor hostname")
		cloudflareTokenFile := set.String("cloudflare-token-file", "", "path to a mode-600 Cloudflare API token file")
		cloudflareAccountID := set.String("cloudflare-account-id", "", "explicit Cloudflare account ID")
		cloudflareZoneID := set.String("cloudflare-zone-id", "", "explicit Cloudflare zone ID")
		cloudflareTunnelName := set.String("cloudflare-tunnel-name", "", "Cloudflare named tunnel name")
		if err := set.Parse(args[1:]); err != nil {
			return 2
		}
		result, err := backend.Setup(ctx, SetupOptions{
			Domain:               *domain,
			CloudflareTokenFile:  *cloudflareTokenFile,
			CloudflareAccountID:  *cloudflareAccountID,
			CloudflareZoneID:     *cloudflareZoneID,
			CloudflareTunnelName: *cloudflareTunnelName,
		})
		if err != nil {
			return printError(stderr, err)
		}
		fmt.Fprintf(stdout, "Executor is configured.\nDomain: %s\nMCP (Streamable HTTP): %s\nLocal stdio: executor stdio\nRecovery key (shown once): %s\n", result.Domain, result.MCPURL, result.RecoveryKey)
		if result.Dashboard != "" {
			fmt.Fprintf(stdout, "Dashboard: %s\n", result.Dashboard)
		}
		return 0
	case "status":
		set := flag.NewFlagSet("status", flag.ContinueOnError)
		set.SetOutput(stderr)
		asJSON := set.Bool("json", false, "print JSON")
		if err := set.Parse(args[1:]); err != nil {
			return 2
		}
		status, err := backend.Status(ctx)
		if err != nil {
			return printError(stderr, err)
		}
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(status)
		} else {
			fmt.Fprintf(stdout, "Executor: %s\nDomain: %s\n", status.State, status.Domain)
		}
		return 0
	case "kill":
		result, err := backend.Kill(ctx)
		if err != nil {
			if result.RecoveryKey != "" || result.URLSecret != "" {
				fmt.Fprintf(stdout, "Executor kill partially completed.\nNew recovery key (shown once): %s\nNew URL secret (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.RecoveryKey, result.URLSecret, result.Dashboard)
			}
			return printError(stderr, err)
		}
		fmt.Fprintf(stdout, "Executor kill completed; credentials were revoked and rotated.\nNew recovery key (shown once): %s\nNew URL secret (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.RecoveryKey, result.URLSecret, result.Dashboard)
		return 0
	case "resume":
		if err := backend.Resume(ctx); err != nil {
			return printError(stderr, err)
		}
		fmt.Fprintln(stdout, "Executor resume completed; reconnect clients with new credentials.")
		return 0
	case "rotate":
		result, err := backend.Rotate(ctx)
		if err != nil {
			if result.RecoveryKey != "" || result.URLSecret != "" || result.Dashboard != "" {
				fmt.Fprintf(stdout, "Credential rotation partially completed.\nNew recovery key (shown once): %s\nNew URL secret (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.RecoveryKey, result.URLSecret, result.Dashboard)
			}
			return printError(stderr, err)
		}
		fmt.Fprintf(stdout, "Credentials rotated.\nNew recovery key (shown once): %s\nNew URL secret (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.RecoveryKey, result.URLSecret, result.Dashboard)
		return 0
	case "doctor":
		set := flag.NewFlagSet("doctor", flag.ContinueOnError)
		set.SetOutput(stderr)
		full := set.Bool("full", false, "run end-to-end checks")
		asJSON := set.Bool("json", false, "print JSON")
		if err := set.Parse(args[1:]); err != nil {
			return 2
		}
		result, err := backend.Doctor(ctx, *full)
		if err != nil {
			return printError(stderr, err)
		}
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(result)
		} else {
			for _, check := range result.Checks {
				mark := "FAIL"
				if check.OK {
					mark = "OK"
				}
				fmt.Fprintf(stdout, "[%s] %s %s\n", mark, check.Name, check.Detail)
			}
		}
		if !result.Healthy {
			return 1
		}
		return 0
	case "auth":
		if len(args) == 2 && args[1] == "enable-url-secret" {
			result, err := backend.EnableURLSecret(ctx)
			if err != nil {
				if result.RecoveryKey != "" || result.URLSecret != "" || result.Dashboard != "" {
					fmt.Fprintf(stdout, "URL-secret activation partially completed.\nNew recovery key (shown once): %s\nNew URL secret (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.RecoveryKey, result.URLSecret, result.Dashboard)
				}
				return printError(stderr, err)
			}
			fmt.Fprintf(stdout, "WARNING: URL-secret compatibility mode is enabled.\nEndpoint (shown once): %s\nNew recovery key (shown once): %s\nNew Dashboard URL (shown once): %s\n", result.Endpoint, result.RecoveryKey, result.Dashboard)
			return 0
		}
		usage(stderr)
		return 2
	default:
		usage(stderr)
		return 2
	}
}

func printError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "Executor error: %v\n", err)
	return 1
}

func usage(w io.Writer) {
	fmt.Fprintln(w, strings.TrimSpace(`Executor — sovereign machine control

Usage:
  executor setup --domain <hostname> [--cloudflare-token-file <path>] [--cloudflare-account-id <id>] [--cloudflare-zone-id <id>] [--cloudflare-tunnel-name <name>]
  executor status [--json]
  executor doctor [--full] [--json]
  executor kill | resume | rotate
  executor auth enable-url-secret`))
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/cli"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/doctor"
	"github.com/jamie950315/executor/internal/secrets"
)

type backend struct {
	stateDir string
}

type setupOptions = cli.SetupOptions

func newBackend(stateDir string) *backend {
	return &backend{stateDir: stateDir}
}

func defaultStateDir() string {
	if stateDir := os.Getenv("EXECUTOR_STATE_DIR"); stateDir != "" {
		return stateDir
	}
	return filepath.Join(".", "executor-state")
}

func (b *backend) Setup(_ context.Context, options cli.SetupOptions) (cli.SetupResult, error) {
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

	created, err := secrets.Create(secretsPath)
	if err == nil {
		result.RecoveryKey = created.RecoveryKey
		result.Dashboard = dashboardURL(cfg.DashboardAddress, created.DashboardKey)
		return result, nil
	}
	if !strings.Contains(err.Error(), "already exist") {
		return cli.SetupResult{}, err
	}
	return result, nil
}

func (b *backend) Status(_ context.Context) (cli.Status, error) {
	return cli.Status{}, unavailable("runtime controller", nil)
}

func (b *backend) Kill(context.Context) error   { return unavailable("runtime controller", nil) }
func (b *backend) Resume(context.Context) error { return unavailable("runtime controller", nil) }

func (b *backend) Rotate(_ context.Context) (cli.RotateResult, error) {
	rotated, err := secrets.Rotate(b.stateDir)
	if err != nil {
		return cli.RotateResult{}, err
	}
	return cli.RotateResult{URLSecret: rotated.URLSecret, RecoveryKey: rotated.RecoveryKey}, nil
}

func (b *backend) Doctor(ctx context.Context, _ bool) (cli.DoctorResult, error) {
	checkers := []doctor.Checker{
		fileCheck{name: "config", path: b.configPath()},
		fileCheck{name: "secrets", path: filepath.Join(b.stateDir, "secrets.json")},
		staticFailureCheck{name: "runtime", err: unavailable("runtime controller", nil)},
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

func (b *backend) EnableURLSecret(context.Context) (string, error) {
	return "", errors.New("enable-url-secret requires a runtime controller and is not available in the packaging build")
}

func (b *backend) configPath() string {
	return filepath.Join(b.stateDir, "config.json")
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

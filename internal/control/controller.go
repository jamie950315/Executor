// Package control implements the independent Executor Kill Switch.
package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/oauth"
	"github.com/jamie950315/executor/internal/secrets"
)

const (
	disabledMarkerName = "disabled"
	oauthStateFilename = "oauth-state.json"
)

// Service identifies a managed Executor component.
type Service string

const (
	Agent       Service = "agent"
	Broker      Service = "broker"
	Desktop     Service = "desktop"
	Cloudflared Service = "cloudflared"
)

// ServiceManager manages the host service wrappers. It is deliberately
// injectable so lifecycle behavior can be tested without controlling services.
type ServiceManager interface {
	Stop(context.Context, Service) error
	Start(context.Context, Service) error
}

type terminalKiller interface {
	KillAll(context.Context, Service) error
}

type readinessChecker interface {
	Check(context.Context, config.Config, secrets.Values) error
}

// Result carries the replacement secrets which must be shown exactly once by
// the independently-run Kill executable.
type Result struct {
	RecoveryKey string
	URLSecret   string
	Dashboard   string
}

// Controller owns independent Kill and Resume operations.
type Controller struct {
	config    config.Config
	secrets   secrets.Values
	services  ServiceManager
	killer    terminalKiller
	readiness readinessChecker
}

// Load reads the runtime config and secret store, then creates a controller
// using the current platform's service manager.
func Load(configPath string) (*Controller, error) {
	return LoadWithServices(configPath, newSystemServiceManager())
}

// LoadWithServices is Load with an explicitly supplied service manager.
func LoadWithServices(configPath string, services ServiceManager) (*Controller, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.StateDir == "" {
		return nil, errors.New("state directory is required")
	}
	values, err := secrets.Load(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}
	if services == nil {
		return nil, errors.New("service manager is required")
	}
	controller := NewController(cfg, values, services, newIPCTerminalKiller(cfg, values))
	controller.readiness = runtimeReadiness{}
	return controller, nil
}

// NewController constructs a controller from already-loaded configuration.
// It is useful to embedders and keeps service and IPC effects injectable.
func NewController(cfg config.Config, values secrets.Values, services ServiceManager, killer terminalKiller) *Controller {
	return &Controller{config: cfg, secrets: values, services: services, killer: killer, readiness: noOpReadiness{}}
}

// Kill quiesces remote access, tears down active execution, revokes persisted
// OAuth grants, replaces every secret, and finally stops the agent. Every step
// is attempted even when earlier steps fail.
func (c *Controller) Kill(ctx context.Context) (Result, error) {
	var result Result
	var errs []error
	steps := []func(context.Context) error{
		c.writeDisabledMarker,
		func(ctx context.Context) error { return c.services.Stop(ctx, Cloudflared) },
		func(ctx context.Context) error { return c.killSessions(ctx, Broker) },
		func(ctx context.Context) error { return c.killSessions(ctx, Desktop) },
		func(ctx context.Context) error { return c.services.Stop(ctx, Desktop) },
		func(ctx context.Context) error { return c.services.Stop(ctx, Broker) },
		c.revokeOAuth,
		func(context.Context) error {
			values, err := secrets.Rotate(c.config.StateDir)
			if err == nil {
				result = Result{
					RecoveryKey: values.RecoveryKey,
					URLSecret:   values.URLSecret,
					Dashboard:   dashboardBootstrapURL(c.config.DashboardAddress, values.DashboardKey),
				}
			}
			return err
		},
		func(ctx context.Context) error { return c.services.Stop(ctx, Agent) },
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

func dashboardBootstrapURL(address, key string) string {
	if address == "" || key == "" {
		return ""
	}
	return "http://" + address + "/?token=" + key
}

// Resume starts local components before restoring the external tunnel. A
// failure leaves the disabled marker in place and stops the startup sequence.
func (c *Controller) Resume(ctx context.Context) error {
	for _, service := range []Service{Broker, Desktop, Agent} {
		if err := c.services.Start(ctx, service); err != nil {
			return err
		}
	}
	values, err := secrets.Load(c.config.StateDir)
	if err != nil {
		return err
	}
	if err := c.readiness.Check(ctx, c.config, values); err != nil {
		return fmt.Errorf("services did not load rotated credentials: %w", err)
	}
	if err := c.services.Start(ctx, Cloudflared); err != nil {
		return err
	}
	if err := os.Remove(c.markerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type noOpReadiness struct{}

func (noOpReadiness) Check(context.Context, config.Config, secrets.Values) error { return nil }

type runtimeReadiness struct{}

func (runtimeReadiness) Check(ctx context.Context, cfg config.Config, values secrets.Values) error {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var last error
	for {
		if err := checkRuntimeOnce(waitCtx, cfg, values); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-waitCtx.Done():
			return errors.Join(last, waitCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func checkRuntimeOnce(ctx context.Context, cfg config.Config, values secrets.Values) error {
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var status desktop.RPCDeviceStatus
	if err := ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)).Call(probeCtx, desktop.RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		return fmt.Errorf("broker readiness: %w", err)
	}
	if err := ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)).Call(probeCtx, desktop.RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		return fmt.Errorf("desktop readiness: %w", err)
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+cfg.AgentAddress+"/.executor/health", nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Executor-Health-Key", values.DashboardKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("agent readiness: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("agent readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Controller) writeDisabledMarker(context.Context) error {
	if err := os.MkdirAll(c.config.StateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.markerPath(), []byte("quiesced\n"), 0o600)
}

func (c *Controller) markerPath() string { return filepath.Join(c.config.StateDir, disabledMarkerName) }

func (c *Controller) killSessions(ctx context.Context, component Service) error {
	if c.killer == nil {
		return errors.New("terminal IPC client is required")
	}
	return c.killer.KillAll(ctx, component)
}

func (c *Controller) revokeOAuth(context.Context) error {
	statePath := filepath.Join(c.config.StateDir, oauthStateFilename)
	if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	core, err := newOAuthCore(c.config, c.secrets)
	if err != nil {
		return err
	}
	if err := core.LoadState(statePath); err != nil {
		return err
	}
	core.RevokeAll()
	return core.SaveState(statePath)
}

func newOAuthCore(cfg config.Config, values secrets.Values) (*oauth.Core, error) {
	domain := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if domain == "" || strings.Contains(domain, "://") {
		return nil, errors.New("domain is required for OAuth")
	}
	resource := "https://" + domain
	return oauth.NewCore(oauth.Config{
		Issuer: resource, Resource: resource, Audience: resource,
		AuthorizationPath: "/oauth/authorize", TokenPath: "/oauth/token", RegistrationPath: "/oauth/register",
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: 5 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour,
		SigningKey: []byte(values.OAuthKey),
	})
}

type ipcTerminalKiller struct {
	broker  *ipc.RPCClient
	desktop *ipc.RPCClient
}

func newIPCTerminalKiller(cfg config.Config, values secrets.Values) *ipcTerminalKiller {
	return &ipcTerminalKiller{
		broker:  ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)),
		desktop: ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)),
	}
}

func (k *ipcTerminalKiller) KillAll(ctx context.Context, component Service) error {
	switch component {
	case Broker:
		return k.broker.Call(ctx, desktop.RPCMethodTerminalKillAll, struct{}{}, nil)
	case Desktop:
		return k.desktop.Call(ctx, desktop.RPCMethodTerminalKillAll, struct{}{}, nil)
	default:
		return fmt.Errorf("terminal kill is unsupported for %s", component)
	}
}

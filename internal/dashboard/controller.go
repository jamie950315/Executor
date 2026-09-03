package dashboard

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/secrets"
)

type lifecycleController interface {
	Kill(context.Context) (control.Result, error)
	Resume(context.Context) error
}

// RuntimeController reports local reachability and delegates destructive
// operations to the independent control package.
type RuntimeController struct {
	config    config.Config
	control   lifecycleController
	broker    *ipc.RPCClient
	desktop   *ipc.RPCClient
	statePath string
}

func NewRuntimeController(cfg config.Config, values secrets.Values, lifecycle lifecycleController) *RuntimeController {
	return &RuntimeController{
		config:    cfg,
		control:   lifecycle,
		broker:    ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)),
		desktop:   ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)),
		statePath: cfg.StateDir,
	}
}

func (c *RuntimeController) Snapshot(ctx context.Context) (Snapshot, error) {
	snapshot := Snapshot{
		State:   c.state(),
		Domain:  c.config.Domain,
		MCPURL:  mcpURL(c.config.Domain),
		Agent:   reachAgent(ctx, c.config.AgentAddress),
		Broker:  reachIPC(ctx, c.broker),
		Desktop: reachIPC(ctx, c.desktop),
		Tunnel:  "not monitored",
	}
	if snapshot.State == "disabled" {
		snapshot.Tunnel = "disabled"
	}
	return snapshot, nil
}

func (c *RuntimeController) Kill(ctx context.Context) (KillResult, error) {
	if c.control == nil {
		return KillResult{}, errors.New("control controller is required")
	}
	result, err := c.control.Kill(ctx)
	return KillResult{RecoveryKey: result.RecoveryKey, URLSecret: result.URLSecret, Dashboard: result.Dashboard}, err
}

func (c *RuntimeController) Resume(ctx context.Context) error {
	if c.control == nil {
		return errors.New("control controller is required")
	}
	return c.control.Resume(ctx)
}

func (c *RuntimeController) Permissions(ctx context.Context, request bool) (permissionmodel.Report, error) {
	var report permissionmodel.Report
	err := c.desktop.Call(ctx, desktop.RPCMethodDesktopPermissions, desktop.RPCDesktopPermissionsParams{Request: request}, &report)
	if err != nil {
		return permissionmodel.Report{}, err
	}
	return permissionmodel.ValidateReport(report)
}

func (c *RuntimeController) Rotate(ctx context.Context) (KillResult, error) {
	result, err := c.Kill(ctx)
	if err != nil {
		return result, err
	}
	if err := c.Resume(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func (c *RuntimeController) state() string {
	if _, err := os.Stat(filepath.Join(c.statePath, "disabled")); err == nil {
		return "disabled"
	}
	return "armed"
}

func mcpURL(domain string) string {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" {
		return ""
	}
	return "https://" + domain + "/mcp"
}

func reachAgent(ctx context.Context, address string) string {
	if address == "" {
		return "unreachable"
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", address)
	if err != nil {
		return "unreachable"
	}
	_ = connection.Close()
	return "reachable"
}

func reachIPC(ctx context.Context, client *ipc.RPCClient) string {
	if client == nil {
		return "unreachable"
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var status desktop.RPCDeviceStatus
	if err := client.Call(probeCtx, desktop.RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		return "unreachable"
	}
	return "reachable"
}

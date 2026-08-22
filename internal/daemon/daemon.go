// Package daemon wires Executor runtime components into long-lived processes.
package daemon

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/agent"
	"github.com/jamie950315/executor/internal/audit"
	"github.com/jamie950315/executor/internal/broker"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/dashboard"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/dispatch"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/oauth"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/relayclient"
	"github.com/jamie950315/executor/internal/secrets"
	"github.com/jamie950315/executor/internal/terminal"
)

const (
	oauthStateFilename = "oauth-state.json"
	serverVersion      = "dev"
)

// RunBroker serves the privileged terminal and filesystem IPC endpoint until
// ctx is canceled.
func RunBroker(ctx context.Context, configPath string) error {
	cfg, values, err := loadRuntime(configPath)
	if err != nil {
		return err
	}

	manager := terminal.NewManager()
	defer manager.KillAll()

	server := broker.NewAdminRPCServer(
		cfg.BrokerEndpoint,
		[]byte(values.BrokerIPCKey),
		manager,
		filesystem.NewLocalService(),
	)
	return server.Serve(ctx)
}

// RunDesktop serves the active user's terminal, filesystem, and desktop IPC
// endpoint until ctx is canceled.
func RunDesktop(ctx context.Context, configPath string) error {
	cfg, values, err := loadRuntime(configPath)
	if err != nil {
		return err
	}

	manager := terminal.NewManager()
	defer manager.KillAll()

	server := desktop.NewHelperRPCServer(
		cfg.DesktopEndpoint,
		[]byte(values.DesktopIPCKey),
		manager,
		filesystem.NewLocalService(),
		desktop.NewController(),
	)
	return server.Serve(ctx)
}

// RunAgent serves OAuth discovery and the OAuth-protected streamable MCP
// endpoint until ctx is canceled.
func RunAgent(ctx context.Context, configPath string) error {
	cfg, values, err := loadRuntime(configPath)
	if err != nil {
		return err
	}
	if cfg.AgentAddress == "" {
		return errors.New("agent address is required")
	}
	if !isLoopbackAddress(cfg.AgentAddress) {
		return errors.New("agent address must bind to loopback")
	}

	resource, err := publicResource(cfg.Domain)
	if err != nil {
		return err
	}
	core, err := newOAuthCore(resource, values)
	if err != nil {
		return err
	}
	statePath := filepath.Join(cfg.StateDir, oauthStateFilename)
	if err := core.LoadState(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load OAuth state: %w", err)
	}

	dispatcher, err := newAuditedDispatcher(cfg, values)
	if err != nil {
		return err
	}
	mcpServer := mcp.NewServer(mcp.ServerConfig{
		ServerName:    "Executor",
		ServerVersion: serverVersion,
		Dispatcher:    dispatcher,
	})

	var verifyURLSecret agent.URLSecretVerifier
	if cfg.URLSecretEnabled {
		verifyURLSecret = values.VerifyURLSecret
	}
	protectedMCP := agent.ProtectMCP(
		core,
		resource,
		verifyURLSecret,
		http.HandlerFunc(mcpServer.HandleStreamableHTTP),
	)
	oauthHandler := agent.NewOAuthHandler(
		core,
		resource,
		values.VerifyRecoveryKey,
		agent.WithOAuthStatePath(statePath),
		agent.WithOAuthEventRecorder(newOAuthAuditRecorder(cfg)),
	)

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/.executor/health" {
			if request.Method != http.MethodGet {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			candidate := request.Header.Get("X-Executor-Health-Key")
			if len(candidate) != len(values.DashboardKey) || subtle.ConstantTimeCompare([]byte(candidate), []byte(values.DashboardKey)) != 1 {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("X-Executor-Health", "ok")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if disabled(filepath.Join(cfg.StateDir, "disabled")) {
			http.Error(writer, "Executor is disabled", http.StatusServiceUnavailable)
			return
		}
		if request.URL.Path == "/mcp" || strings.HasSuffix(request.URL.Path, "/mcp") {
			protectedMCP.ServeHTTP(writer, request)
			return
		}
		oauthHandler.ServeHTTP(writer, request)
	})

	listener, err := net.Listen("tcp", cfg.AgentAddress)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler}
	return serveHTTP(ctx, server, listener)
}

// RunDashboard serves the owner-only local control dashboard until ctx is
// canceled. The dashboard listener is intentionally restricted to loopback.
func RunDashboard(ctx context.Context, configPath string) error {
	return runDashboard(ctx, configPath, func(options relayclient.ClientOptions) (dashboardRelay, error) {
		return relayclient.NewClient(options)
	})
}

type dashboardRelay interface {
	Run(context.Context) error
}

type dashboardRelayFactory func(relayclient.ClientOptions) (dashboardRelay, error)

func runDashboard(ctx context.Context, configPath string, relayFactory dashboardRelayFactory) error {
	cfg, _, err := loadRuntime(configPath)
	if err != nil {
		return err
	}
	if cfg.DashboardAddress == "" {
		return errors.New("dashboard address is required")
	}
	if !isLoopbackAddress(cfg.DashboardAddress) {
		return errors.New("dashboard address must bind to loopback")
	}

	handler := dashboard.NewHandlerWithTokenProvider(
		&reloadingDashboardController{configPath: configPath},
		func() (string, error) {
			current, err := secrets.Load(cfg.StateDir)
			if err != nil {
				return "", err
			}
			return current.DashboardKey, nil
		},
	)
	listener, err := net.Listen("tcp", cfg.DashboardAddress)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler}
	if relayFactory == nil {
		_ = listener.Close()
		return errors.New("dashboard relay factory is required")
	}
	relayRuntime, err := relayFactory(relayclient.ClientOptions{
		ConfigPath: configPath, ExecutorVersion: serverVersion,
	})
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("create dashboard relay: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpDone := make(chan error, 1)
	relayDone := make(chan error, 1)
	go func() { httpDone <- serveHTTP(runCtx, server, listener) }()
	go func() { relayDone <- relayRuntime.Run(runCtx) }()
	relayStopped := false
	for {
		select {
		case httpErr := <-httpDone:
			cancel()
			if !relayStopped {
				<-relayDone
			}
			return httpErr
		case <-relayDone:
			// Relay failures must not take down the independent loopback rescue
			// surface. Client.Run normally reconnects internally; a fatal relay
			// return leaves the local Dashboard serving until service shutdown.
			relayStopped = true
		case <-ctx.Done():
			cancel()
			httpErr := <-httpDone
			if !relayStopped {
				<-relayDone
			}
			return httpErr
		}
	}
}

type reloadingDashboardController struct {
	configPath string
}

func (c *reloadingDashboardController) current() (*dashboard.RuntimeController, error) {
	cfg, values, err := loadRuntime(c.configPath)
	if err != nil {
		return nil, err
	}
	lifecycle, err := control.Load(c.configPath)
	if err != nil {
		return nil, err
	}
	return dashboard.NewRuntimeController(cfg, values, lifecycle), nil
}

func (c *reloadingDashboardController) Snapshot(ctx context.Context) (dashboard.Snapshot, error) {
	current, err := c.current()
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	return current.Snapshot(ctx)
}

func (c *reloadingDashboardController) Kill(ctx context.Context) (dashboard.KillResult, error) {
	current, err := c.current()
	if err != nil {
		return dashboard.KillResult{}, err
	}
	return current.Kill(ctx)
}

func (c *reloadingDashboardController) Resume(ctx context.Context) error {
	current, err := c.current()
	if err != nil {
		return err
	}
	return current.Resume(ctx)
}

func (c *reloadingDashboardController) Rotate(ctx context.Context) error {
	current, err := c.current()
	if err != nil {
		return err
	}
	return current.Rotate(ctx)
}

func (c *reloadingDashboardController) Permissions(ctx context.Context, request bool) (permissionmodel.Report, error) {
	current, err := c.current()
	if err != nil {
		return permissionmodel.Report{}, err
	}
	return current.Permissions(ctx, request)
}

func disabled(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

// RunStdio serves the local MCP transport without OAuth. It dispatches through
// the same broker and desktop IPC clients used by RunAgent.
func RunStdio(ctx context.Context, configPath string, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("stdio input and output are required")
	}
	cfg, values, err := loadRuntime(configPath)
	if err != nil {
		return err
	}

	dispatcher, err := newAuditedDispatcher(cfg, values)
	if err != nil {
		return err
	}
	handler := mcp.NewStdioHandler(mcp.NewServer(mcp.ServerConfig{
		ServerName:    "Executor",
		ServerVersion: serverVersion,
		Dispatcher:    dispatcher,
	}))

	done := make(chan error, 1)
	go func() { done <- handler.Serve(ctx, in, out) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if closer, ok := in.(io.Closer); ok {
			_ = closer.Close()
			<-done
		}
		return nil
	}
}

func loadRuntime(configPath string) (config.Config, secrets.Values, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, secrets.Values{}, fmt.Errorf("load config: %w", err)
	}
	if cfg.StateDir == "" {
		return config.Config{}, secrets.Values{}, errors.New("state directory is required")
	}
	values, err := secrets.Load(cfg.StateDir)
	if err != nil {
		return config.Config{}, secrets.Values{}, fmt.Errorf("load secrets: %w", err)
	}
	return cfg, values, nil
}

func newDispatcher(cfg config.Config, values secrets.Values) *dispatch.MCP {
	return dispatch.NewMCP(
		ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)),
		ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)),
	)
}

func newAuditedDispatcher(cfg config.Config, values secrets.Values) (mcp.Dispatcher, error) {
	store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Duration(cfg.AuditRetentionH)*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	if err := store.Prune(time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("prune audit store: %w", err)
	}
	dispatcher := newDispatcher(cfg, values)
	return func(ctx context.Context, call mcp.ToolCall) (any, error) {
		actorName := "local-stdio"
		if actor, ok := agent.ActorFromContext(ctx); ok {
			actorName = actor.Subject
		}
		event := audit.Event{
			Actor:     actorName,
			Tool:      call.Name,
			SessionID: call.SessionID,
			Identity:  stringArgument(call.Arguments, "privilege"),
			CWD:       stringArgument(call.Arguments, "cwd"),
			Outcome:   "attempted",
		}
		if err := store.Append(event); err != nil {
			return nil, fmt.Errorf("append audit event: %w", err)
		}
		result, dispatchErr := dispatcher.Dispatch(ctx, call)
		if dispatchErr != nil {
			event.Outcome = "failed"
		} else {
			event.Outcome = "succeeded"
		}
		_ = store.Append(event)
		return result, dispatchErr
	}, nil
}

func newOAuthAuditRecorder(cfg config.Config) agent.OAuthEventRecorder {
	store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Duration(cfg.AuditRetentionH)*time.Hour)
	if err != nil {
		return nil
	}
	return func(event agent.OAuthEvent) {
		detail := "grant_type=" + event.GrantType
		if event.Code != "" {
			detail = event.Code
			if event.Reason != "" {
				detail += ": " + event.Reason
			}
		}
		_ = store.Append(audit.Event{
			Actor:    "remote-oauth",
			Tool:     "oauth_" + event.Stage,
			Identity: event.AuthMethod,
			Outcome:  event.Outcome,
			Detail:   detail,
		})
	}
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return value
}

func publicResource(domain string) (string, error) {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" {
		return "", errors.New("domain is required for OAuth")
	}
	if strings.Contains(domain, "://") {
		return "", errors.New("domain must be a hostname")
	}
	return "https://" + domain, nil
}

func newOAuthCore(resource string, values secrets.Values) (*oauth.Core, error) {
	return oauth.NewCore(oauth.Config{
		Issuer:               resource,
		Resource:             resource,
		Audience:             resource,
		AuthorizationPath:    "/oauth/authorize",
		TokenPath:            "/oauth/token",
		RegistrationPath:     "/oauth/register",
		AccessTokenTTL:       time.Hour,
		AuthorizationCodeTTL: 5 * time.Minute,
		RefreshTokenTTL:      30 * 24 * time.Hour,
		SigningKey:           []byte(values.OAuthKey),
	})
}

func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-serveDone:
		}
	}()

	err := server.Serve(listener)
	close(serveDone)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

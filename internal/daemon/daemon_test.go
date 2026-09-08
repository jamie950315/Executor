package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/audit"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/dashboard"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/oauth"
	"github.com/jamie950315/executor/internal/relayclient"
	"github.com/jamie950315/executor/internal/secrets"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestRunBrokerServesAdminTerminalRPCAndStopsOnCancel(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunBroker(ctx, configPath) })

	client := ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey))
	var sessions []terminal.SessionInfo
	waitFor(t, func() error {
		return client.Call(context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &sessions)
	})
	if len(sessions) != 0 {
		t.Fatalf("initial admin sessions = %d, want 0", len(sessions))
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunDesktopServesOwnerTerminalAndFilesystemRPCAndStopsOnCancel(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunDesktop(ctx, configPath) })

	client := ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey))
	var sessions []terminal.SessionInfo
	waitFor(t, func() error {
		return client.Call(context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &sessions)
	})
	if len(sessions) != 0 {
		t.Fatalf("initial owner sessions = %d, want 0", len(sessions))
	}

	path := filepath.Join(cfg.StateDir, "owner.txt")
	if err := client.Call(context.Background(), desktop.RPCMethodFilesystemWrite, desktop.RPCFilesystemWriteParams{Path: path, Data: []byte("owner"), Perm: 0o600}, nil); err != nil {
		t.Fatalf("owner filesystem write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read owner file: %v", err)
	}
	if got := string(data); got != "owner" {
		t.Fatalf("owner filesystem content = %q, want %q", got, "owner")
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunAgentExposesOAuthAndRequiresOAuthWhenURLSecretDisabled(t *testing.T) {
	configPath, cfg, _ := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	baseURL := "http://" + cfg.AgentAddress

	response := waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-protected-resource", nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("protected-resource metadata status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var metadata map[string]any
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode protected-resource metadata: %v", err)
	}
	if got := metadata["resource"]; got != "https://executor.example.test" {
		t.Fatalf("protected-resource metadata resource = %v, want https://executor.example.test", got)
	}

	response = postMCP(t, baseURL+"/mcp", "", initializeRequest("1"))
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	response = postMCP(t, baseURL+"/not-enabled/mcp", "", initializeRequest("2"))
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled URL-secret MCP status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunAgentAuditsOAuthTokenFailureWithoutCredentialMaterial(t *testing.T) {
	configPath, cfg, _ := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	baseURL := "http://" + cfg.AgentAddress
	waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-authorization-server", nil, nil).Body.Close()

	form := url.Values{
		"grant_type":            {"authorization_code"},
		"client_id":             {"sensitive-client-id"},
		"code":                  {"sensitive-authorization-code"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {"sensitive-client-assertion"},
	}
	response, err := http.PostForm(baseURL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid client assertion status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("OAuth token audit events = %#v, want attempted and failed", events)
	}
	attempted, failed := events[len(events)-2], events[len(events)-1]
	if attempted.Tool != "oauth_token" || attempted.Actor != "remote-oauth" || attempted.Identity != "private_key_jwt" || attempted.Outcome != "attempted" || attempted.Detail != "grant_type=authorization_code" {
		t.Fatalf("unexpected OAuth token attempt event: %#v", attempted)
	}
	if failed.Tool != "oauth_token" || failed.Actor != "remote-oauth" || failed.Identity != "private_key_jwt" || failed.Outcome != "failed" || failed.Detail != "invalid_client: invalid client assertion format" {
		t.Fatalf("unexpected OAuth token failure event: %#v", failed)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-client-id", "sensitive-authorization-code", "sensitive-client-assertion"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("OAuth audit contains credential material %q: %s", secret, encoded)
		}
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunAgentRejectsNonLoopbackOriginBinding(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := daemonFixture(t)
	cfg.AgentAddress = "0.0.0.0:8787"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("RunAgent rejected binding without an error")
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		assertDaemonStopped(t, errCh)
		t.Fatal("RunAgent accepted a non-loopback origin binding")
	}
}

func TestRunDashboardRejectsNonLoopbackBinding(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := daemonFixture(t)
	cfg.DashboardAddress = "0.0.0.0:8788"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	err := RunDashboard(context.Background(), configPath)
	if err == nil {
		t.Fatal("RunDashboard accepted a non-loopback binding")
	}
}

func TestRunDashboardServesLoopbackStatusWithoutSecretsAndStopsOnCancel(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunDashboard(ctx, configPath) })
	baseURL := "http://" + cfg.DashboardAddress

	ready := waitForHTTP(t, http.MethodGet, baseURL+"/", nil, nil)
	ready.Body.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bootstrap, err := client.Get(baseURL + "/?token=" + values.DashboardKey)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.StatusCode != http.StatusSeeOther {
		bootstrap.Body.Close()
		t.Fatalf("dashboard bootstrap status = %d, want %d", bootstrap.StatusCode, http.StatusSeeOther)
	}
	cookies := bootstrap.Cookies()
	bootstrap.Body.Close()
	if len(cookies) != 1 {
		t.Fatalf("dashboard bootstrap cookies = %#v", cookies)
	}

	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookies[0])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var status map[string]any
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(values.DashboardKey)) || bytes.Contains(encoded, []byte(values.RecoveryKey)) || bytes.Contains(encoded, []byte(values.URLSecret)) {
		t.Fatalf("dashboard status exposed secret material: %s", encoded)
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunDashboardReloadsIPCKeysAfterCredentialRotation(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	brokerCtx, cancelBroker := context.WithCancel(context.Background())
	desktopCtx, cancelDesktop := context.WithCancel(context.Background())
	brokerErr := startDaemon(t, func() error { return RunBroker(brokerCtx, configPath) })
	desktopErr := startDaemon(t, func() error { return RunDesktop(desktopCtx, configPath) })
	dashboardCtx, cancelDashboard := context.WithCancel(context.Background())
	dashboardErr := startDaemon(t, func() error { return RunDashboard(dashboardCtx, configPath) })
	t.Cleanup(func() {
		cancelBroker()
		cancelDesktop()
		cancelDashboard()
	})

	baseURL := "http://" + cfg.DashboardAddress
	waitFor(t, func() error {
		snapshot, err := authenticatedDashboardSnapshot(baseURL, values.DashboardKey)
		if err != nil {
			return err
		}
		if snapshot.Broker != "reachable" || snapshot.Desktop != "reachable" {
			return errors.New("initial IPC services are not reachable")
		}
		return nil
	})

	cancelBroker()
	cancelDesktop()
	assertDaemonStopped(t, brokerErr)
	assertDaemonStopped(t, desktopErr)
	rotated, err := secrets.Rotate(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	brokerCtx, cancelBroker = context.WithCancel(context.Background())
	desktopCtx, cancelDesktop = context.WithCancel(context.Background())
	brokerErr = startDaemon(t, func() error { return RunBroker(brokerCtx, configPath) })
	desktopErr = startDaemon(t, func() error { return RunDesktop(desktopCtx, configPath) })

	waitFor(t, func() error {
		snapshot, err := authenticatedDashboardSnapshot(baseURL, rotated.DashboardKey)
		if err != nil {
			return err
		}
		if snapshot.Broker != "reachable" || snapshot.Desktop != "reachable" {
			return errors.New("rotated IPC services are not reachable through the existing Dashboard")
		}
		return nil
	})

	cancelBroker()
	cancelDesktop()
	cancelDashboard()
	assertDaemonStopped(t, brokerErr)
	assertDaemonStopped(t, desktopErr)
	assertDaemonStopped(t, dashboardErr)
}

func authenticatedDashboardSnapshot(baseURL, token string) (dashboard.Snapshot, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bootstrap, err := client.Get(baseURL + "/?token=" + token)
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	cookies := bootstrap.Cookies()
	_ = bootstrap.Body.Close()
	if bootstrap.StatusCode != http.StatusSeeOther || len(cookies) != 1 {
		return dashboard.Snapshot{}, errors.New("dashboard bootstrap failed")
	}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/status", nil)
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	request.AddCookie(cookies[0])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dashboard.Snapshot{}, errors.New("dashboard status failed")
	}
	var snapshot dashboard.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return dashboard.Snapshot{}, err
	}
	return snapshot, nil
}

type fakeDashboardRelay struct {
	started chan struct{}
	stopped chan struct{}
}

type failingDashboardRelay struct {
	release chan struct{}
}

func (r *failingDashboardRelay) Run(context.Context) error {
	<-r.release
	return errors.New("relay unavailable")
}

func (r *fakeDashboardRelay) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.stopped)
	return nil
}

func (r *fakeDashboardRelay) Status() dashboard.RelayStatus {
	return dashboard.RelayStatus{State: "connected", UpdatedAt: time.Now().UTC()}
}

func (r *failingDashboardRelay) Status() dashboard.RelayStatus {
	return dashboard.RelayStatus{State: "disconnected", UpdatedAt: time.Now().UTC()}
}

func TestRunDashboardOwnsConfiguredRelayAndStopsItWithHTTPRuntime(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	relayRuntime := &fakeDashboardRelay{started: make(chan struct{}), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error {
		return runDashboard(ctx, configPath, func(relayclient.ClientOptions) (dashboardRelay, error) {
			return relayRuntime, nil
		})
	})
	select {
	case <-relayRuntime.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("configured dashboard relay did not start")
	}
	response := waitForHTTP(t, http.MethodGet, "http://"+cfg.DashboardAddress+"/", nil, nil)
	response.Body.Close()
	relayHeaders := make(http.Header)
	relayHeaders.Set("X-Executor-Health-Key", values.DashboardKey)
	relayResponse := waitForHTTP(t, http.MethodGet, "http://"+cfg.DashboardAddress+"/.executor/relay-status", nil, relayHeaders)
	relayBody, err := io.ReadAll(relayResponse.Body)
	relayResponse.Body.Close()
	if err != nil || relayResponse.StatusCode != http.StatusOK || !strings.Contains(string(relayBody), `"state":"connected"`) {
		cancel()
		t.Fatalf("runtime relay status = %d %q, %v", relayResponse.StatusCode, relayBody, err)
	}
	cancel()
	assertDaemonStopped(t, errCh)
	select {
	case <-relayRuntime.stopped:
	case <-time.After(time.Second):
		t.Fatal("dashboard relay did not stop with RunDashboard context")
	}
}

func TestRunDashboardStartsRelayWatcherBeforeEnrollment(t *testing.T) {
	configPath, cfg, _ := daemonFixture(t)
	relayRuntime := &fakeDashboardRelay{started: make(chan struct{}), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error {
		return runDashboard(ctx, configPath, func(relayclient.ClientOptions) (dashboardRelay, error) {
			return relayRuntime, nil
		})
	})
	select {
	case <-relayRuntime.started:
	case <-time.After(200 * time.Millisecond):
		cancel()
		assertDaemonStopped(t, errCh)
		t.Fatal("dashboard did not start the enrollment-aware relay watcher")
	}
	response := waitForHTTP(t, http.MethodGet, "http://"+cfg.DashboardAddress+"/", nil, nil)
	response.Body.Close()
	cancel()
	assertDaemonStopped(t, errCh)
	select {
	case <-relayRuntime.stopped:
	case <-time.After(time.Second):
		t.Fatal("dashboard relay watcher did not stop")
	}
}

func TestRunDashboardKeepsLocalRescueAvailableAfterRelayFailure(t *testing.T) {
	diagnostics := captureDiagnostics(t)
	configPath, cfg, _ := daemonFixture(t)
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	relayRuntime := &failingDashboardRelay{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error {
		return runDashboard(ctx, configPath, func(relayclient.ClientOptions) (dashboardRelay, error) {
			return relayRuntime, nil
		})
	})
	baseURL := "http://" + cfg.DashboardAddress
	response := waitForHTTP(t, http.MethodGet, baseURL+"/", nil, nil)
	response.Body.Close()
	close(relayRuntime.release)
	time.Sleep(20 * time.Millisecond)
	response = waitForHTTP(t, http.MethodGet, baseURL+"/", nil, nil)
	response.Body.Close()
	cancel()
	assertDaemonStopped(t, errCh)
	if !strings.Contains(diagnostics.String(), "dashboard_relay_stopped") {
		t.Fatal("fatal relay failure left no service diagnostic")
	}
}

func TestRunAgentQuiescesImmediatelyWhenDisabledMarkerAppears(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	baseURL := "http://" + cfg.AgentAddress
	response := waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-protected-resource", nil, nil)
	response.Body.Close()
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response = waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-protected-resource", nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("quiesced status = %d, want 503", response.StatusCode)
	}
	healthHeader := make(http.Header)
	healthHeader.Set("X-Executor-Health-Key", values.DashboardKey)
	response = waitForHTTP(t, http.MethodGet, baseURL+"/.executor/health", nil, healthHeader)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("authenticated health status = %d, want 204", response.StatusCode)
	}
	if got := response.Header.Get("X-Executor-Health"); got != "ok" {
		t.Fatalf("authenticated health marker = %q, want ok", got)
	}
	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunAgentAcceptsExplicitlyEnabledURLSecretAndDispatchesToDesktop(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	cfg.URLSecretEnabled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save enabled config: %v", err)
	}

	brokerCtx, cancelBroker := context.WithCancel(context.Background())
	desktopCtx, cancelDesktop := context.WithCancel(context.Background())
	brokerErr := startDaemon(t, func() error { return RunBroker(brokerCtx, configPath) })
	desktopErr := startDaemon(t, func() error { return RunDesktop(desktopCtx, configPath) })
	defer func() {
		cancelBroker()
		cancelDesktop()
		assertDaemonStopped(t, brokerErr)
		assertDaemonStopped(t, desktopErr)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	baseURL := "http://" + cfg.AgentAddress
	response := waitForMCP(t, baseURL+"/"+values.URLSecret+"/mcp", "", initializeRequest("1"))
	var initialize struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	decodeHTTPJSON(t, response, &initialize)
	if initialize.Result.SessionID == "" {
		t.Fatal("URL-secret initialize response missing session ID")
	}

	response = postMCP(t, baseURL+"/"+values.URLSecret+"/mcp", initialize.Result.SessionID, toolCallRequest("2", "terminal_sessions", map[string]any{"action": "list", "privilege": "owner"}))
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("URL-secret desktop dispatch status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var call struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&call); err != nil {
		t.Fatalf("decode URL-secret desktop dispatch: %v", err)
	}
	if call.Result.IsError {
		t.Fatal("URL-secret desktop dispatch returned an MCP tool error")
	}

	cancel()
	assertDaemonStopped(t, errCh)
}

func TestRunAgentRestoresAndPersistsOAuthState(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	resource := "https://executor.example.test"
	seed, err := oauth.NewCore(oauth.Config{
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
	if err != nil {
		t.Fatalf("create OAuth state seed: %v", err)
	}
	stored, err := seed.RegisterClient(oauth.DynamicClientRegistrationRequest{
		ClientName:   "Stored Client",
		RedirectURIs: []string{"https://client.example.test/callback"},
		Scopes:       []string{"executor.full"},
	})
	if err != nil {
		t.Fatalf("register stored client: %v", err)
	}
	statePath := filepath.Join(cfg.StateDir, "oauth-state.json")
	if err := seed.SaveState(statePath); err != nil {
		t.Fatalf("save OAuth state seed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunAgent(ctx, configPath) })
	baseURL := "http://" + cfg.AgentAddress
	response := waitForHTTP(t, http.MethodGet, baseURL+"/.well-known/oauth-authorization-server", nil, nil)
	response.Body.Close()

	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {stored.ClientID},
		"redirect_uri":          {"https://client.example.test/callback"},
		"scope":                 {"executor.full"},
		"code_challenge":        {"test-challenge"},
		"code_challenge_method": {"S256"},
	}
	response = waitForHTTP(t, http.MethodGet, baseURL+"/oauth/authorize?"+query.Encode(), nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restored client authorization status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	registration, err := json.Marshal(oauth.DynamicClientRegistrationRequest{
		ClientName:   "Persisted Client",
		RedirectURIs: []string{"https://chatgpt.com/connector/oauth/persisted"},
		Scopes:       []string{"executor.full"},
	})
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}
	response = postJSON(t, baseURL+"/oauth/register", registration)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("registration status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	var persisted oauth.ClientRegistration
	if err := json.NewDecoder(response.Body).Decode(&persisted); err != nil {
		t.Fatalf("decode persisted registration: %v", err)
	}

	cancel()
	assertDaemonStopped(t, errCh)

	restored, err := oauth.NewCore(oauth.Config{
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
	if err != nil {
		t.Fatalf("create restored OAuth core: %v", err)
	}
	if err := restored.LoadState(statePath); err != nil {
		t.Fatalf("load persisted OAuth state: %v", err)
	}
	if err := restored.ValidateClient(oauth.ClientValidationRequest{ClientID: persisted.ClientID, RedirectURI: "https://chatgpt.com/connector/oauth/persisted"}); err != nil {
		t.Fatalf("persisted OAuth client unavailable after restart: %v", err)
	}
}

func TestRunStdioDispatchesWithoutOAuth(t *testing.T) {
	configPath, cfg, values := daemonFixture(t)
	brokerCtx, cancelBroker := context.WithCancel(context.Background())
	desktopCtx, cancelDesktop := context.WithCancel(context.Background())
	brokerErr := startDaemon(t, func() error { return RunBroker(brokerCtx, configPath) })
	desktopErr := startDaemon(t, func() error { return RunDesktop(desktopCtx, configPath) })
	defer func() {
		cancelBroker()
		cancelDesktop()
		assertDaemonStopped(t, brokerErr)
		assertDaemonStopped(t, desktopErr)
	}()
	var sessions []terminal.SessionInfo
	waitFor(t, func() error {
		return ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)).Call(
			context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &sessions,
		)
	})
	waitFor(t, func() error {
		return ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)).Call(
			context.Background(), desktop.RPCMethodTerminalList, struct{}{}, &sessions,
		)
	})

	var input bytes.Buffer
	writeMCPFrame(t, &input, initializeRequest("1"))
	writeMCPFrame(t, &input, toolCallRequest("2", "terminal_sessions", map[string]any{"action": "list", "privilege": "owner"}))
	var output bytes.Buffer
	if err := RunStdio(context.Background(), configPath, &input, &output); err != nil {
		t.Fatalf("RunStdio() error = %v", err)
	}

	reader := bufio.NewReader(&output)
	var initialize struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	decodeMCPFrame(t, reader, &initialize)
	if initialize.Result.SessionID == "" {
		t.Fatal("stdio initialize response missing session ID")
	}
	var call struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeMCPFrame(t, reader, &call)
	if call.Error != nil {
		t.Fatalf("stdio dispatcher returned a JSON-RPC error: %s", call.Error.Message)
	}
	if call.Result.IsError {
		t.Fatal("stdio dispatcher returned an MCP tool error")
	}
	store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Tool != "terminal_sessions" || events[len(events)-1].Identity != "owner" || events[len(events)-1].Outcome != "succeeded" {
		t.Fatalf("missing metadata-only audit event: %#v", events)
	}
	if events[len(events)-1].Detail != "" {
		t.Fatalf("audit event should not contain command arguments or output: %#v", events[len(events)-1])
	}
}

func TestRunStdioStopsOnContextCancellation(t *testing.T) {
	configPath, _, _ := daemonFixture(t)
	reader, _ := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := startDaemon(t, func() error { return RunStdio(ctx, configPath, reader, io.Discard) })

	cancel()
	assertDaemonStopped(t, errCh)
}

func daemonFixture(t *testing.T) (string, config.Config, secrets.Values) {
	t.Helper()
	stateDir := t.TempDir()
	endpointDir, err := os.MkdirTemp("", "executor-daemon-")
	if err != nil {
		t.Fatalf("create IPC endpoint directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(endpointDir) })
	cfg := config.Default(stateDir)
	cfg.Domain = "executor.example.test"
	cfg.AgentAddress = availableTCPAddress(t)
	cfg.DashboardAddress = availableTCPAddress(t)
	if runtime.GOOS == "windows" {
		name := filepath.Base(endpointDir)
		cfg.BrokerEndpoint = `\\.\pipe\` + name + "-broker"
		cfg.DesktopEndpoint = `\\.\pipe\` + name + "-desktop"
	} else {
		cfg.BrokerEndpoint = filepath.Join(endpointDir, "broker.sock")
		cfg.DesktopEndpoint = filepath.Join(endpointDir, "desktop.sock")
	}
	configPath := filepath.Join(stateDir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatalf("create secrets: %v", err)
	}
	return configPath, cfg, values
}

func availableTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP address: %v", err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func startDaemon(t *testing.T, run func() error) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- run() }()
	return errCh
}

func assertDaemonStopped(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon returned error after cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
}

func waitFor(t *testing.T, action func() error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := action(); err == nil {
			return
		} else {
			last = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon did not become ready: %v", last)
}

func waitForHTTP(t *testing.T, method, url string, body io.Reader, header http.Header) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(method, url, body)
		if err != nil {
			t.Fatalf("new HTTP request: %v", err)
		}
		request.Header = header.Clone()
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			return response
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent did not become ready: %v", last)
	return nil
}

func waitForMCP(t *testing.T, url, sessionID string, payload map[string]any) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := postMCPRequest(url, sessionID, payload)
		if err == nil {
			return response
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent MCP did not become ready: %v", last)
	return nil
}

func postMCP(t *testing.T, url, sessionID string, payload map[string]any) *http.Response {
	t.Helper()
	response, err := postMCPRequest(url, sessionID, payload)
	if err != nil {
		t.Fatalf("MCP request: %v", err)
	}
	return response
}

func postMCPRequest(url, sessionID string, payload map[string]any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		request.Header.Set(mcp.SessionHeader, sessionID)
	}
	return http.DefaultClient.Do(request)
}

func postJSON(t *testing.T, url string, payload []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new JSON request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("JSON request: %v", err)
	}
	return response
}

func initializeRequest(id string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "initialize"}
}

func toolCallRequest(id, name string, arguments map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}}
}

func decodeHTTPJSON(t *testing.T, response *http.Response, dst any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatalf("decode HTTP response: %v", err)
	}
}

func writeMCPFrame(t *testing.T, writer io.Writer, request map[string]any) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal MCP request: %v", err)
	}
	if err := mcp.WriteFrame(writer, payload); err != nil {
		t.Fatalf("write MCP frame: %v", err)
	}
}

func decodeMCPFrame(t *testing.T, reader *bufio.Reader, dst any) {
	t.Helper()
	payload, err := mcp.ReadFrame(reader)
	if err != nil {
		t.Fatalf("read MCP frame: %v", err)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatalf("decode MCP frame: %v", err)
	}
}

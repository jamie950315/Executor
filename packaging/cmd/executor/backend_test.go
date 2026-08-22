package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/cli"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/ipc"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestDashboardEnrollmentSerializesOriginConfigurationWithRelayWorkflow(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := secrets.Create(stateDir); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "config.json")
	if err := config.Save(configPath, config.Default(stateDir)); err != nil {
		t.Fatal(err)
	}
	firstTokenPath := filepath.Join(t.TempDir(), "first-enrollment.token")
	secondTokenPath := filepath.Join(t.TempDir(), "second-enrollment.token")
	if err := os.WriteFile(firstTokenPath, []byte("test-only-first-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondTokenPath, []byte("test-only-second-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	const firstURL = "https://first-dashboard.example.test"
	const secondURL = "https://second-dashboard.example.test"
	firstRequest := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstPosts atomic.Int32
	var secondPosts atomic.Int32
	b := newBackend(stateDir)
	b.remoteHTTPClient = &http.Client{Transport: backendRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "first-dashboard.example.test":
			if firstPosts.Add(1) == 1 {
				close(firstRequest)
			}
			<-releaseFirst
		case "second-dashboard.example.test":
			secondPosts.Add(1)
		default:
			return nil, errors.New("unexpected Dashboard host")
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	type enrollmentOutcome struct {
		result cli.DashboardEnrollResult
		err    error
	}
	firstResult := make(chan enrollmentOutcome, 1)
	go func() {
		result, err := b.EnrollDashboard(context.Background(), cli.DashboardEnrollOptions{
			URL: firstURL, TokenFile: firstTokenPath,
		})
		firstResult <- enrollmentOutcome{result: result, err: err}
	}()
	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		close(releaseFirst)
		t.Fatal("first Dashboard enrollment did not reach the request boundary")
	}
	secondResult := make(chan enrollmentOutcome, 1)
	go func() {
		result, err := b.EnrollDashboard(context.Background(), cli.DashboardEnrollOptions{
			URL: secondURL, TokenFile: secondTokenPath,
		})
		secondResult <- enrollmentOutcome{result: result, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	duringFirst, err := config.Load(configPath)
	if err != nil {
		close(releaseFirst)
		t.Fatal(err)
	}
	if duringFirst.UnifiedDashboard.URL != firstURL {
		close(releaseFirst)
		<-firstResult
		<-secondResult
		t.Fatalf("concurrent enrollment changed URL during the first workflow: %q", duringFirst.UnifiedDashboard.URL)
	}
	if secondPosts.Load() != 0 {
		close(releaseFirst)
		<-firstResult
		<-secondResult
		t.Fatalf("second Dashboard enrollment POST count before release = %d, want 0", secondPosts.Load())
	}
	close(releaseFirst)
	first := <-firstResult
	second := <-secondResult
	if first.err != nil || first.result.URL != firstURL {
		t.Fatalf("first Dashboard enrollment = %#v, %v", first.result, first.err)
	}
	if second.err != nil || second.result.URL != secondURL {
		t.Fatalf("second Dashboard enrollment = %#v, %v", second.result, second.err)
	}
	if firstPosts.Load() != 1 || secondPosts.Load() != 1 {
		t.Fatalf("Dashboard enrollment POST counts = %d, %d; want 1, 1", firstPosts.Load(), secondPosts.Load())
	}
}

func TestDashboardEquivalentOriginFinishesMatchingCleanupWithoutPosting(t *testing.T) {
	stateDir := t.TempDir()
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	token := []byte("test-only-enrollment-token")
	if err := os.WriteFile(tokenPath, token, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(stateDir)
	cfg.UnifiedDashboard.URL = "https://DASHBOARD.EXAMPLE.test:443"
	cfg.UnifiedDashboard.Enrolled = true
	mac := hmac.New(sha256.New, values.RelayPrivateJWK)
	_, _ = mac.Write(token)
	cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if err := config.Save(filepath.Join(stateDir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	b := newBackend(stateDir)
	posts := 0
	b.remoteHTTPClient = &http.Client{Transport: backendRoundTripFunc(func(*http.Request) (*http.Response, error) {
		posts++
		return nil, errors.New("unexpected POST")
	})}
	result, err := b.EnrollDashboard(context.Background(), cli.DashboardEnrollOptions{
		URL: "https://dashboard.example.test", TokenFile: tokenPath,
	})
	if err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if posts != 0 {
		t.Fatalf("equivalent-origin cleanup POST count = %d, want 0", posts)
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup retry left token: %v", err)
	}
	if result.URL != "https://dashboard.example.test" {
		t.Fatalf("canonical Dashboard URL = %q", result.URL)
	}
}

func TestDashboardExplicitSameOriginEnrollmentPostsAgainAfterCleanup(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := secrets.Create(stateDir); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-reenrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts++
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	cfg := config.Default(stateDir)
	cfg.UnifiedDashboard.URL = server.URL
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(filepath.Join(stateDir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	b := newBackend(stateDir)
	b.remoteHTTPClient = server.Client()
	if _, err := b.EnrollDashboard(context.Background(), cli.DashboardEnrollOptions{
		URL: server.URL, TokenFile: tokenPath,
	}); err != nil {
		t.Fatalf("same-origin re-enrollment: %v", err)
	}
	if posts != 1 {
		t.Fatalf("same-origin re-enrollment POST count = %d, want 1", posts)
	}
}

func TestDashboardGenuinelyDifferentOriginStartsNewEnrollment(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := secrets.Create(stateDir); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-new-origin-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(stateDir)
	cfg.UnifiedDashboard.URL = "https://old-dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	cfg.UnifiedDashboard.EnrollmentCleanupFingerprint = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI"
	if err := config.Save(filepath.Join(stateDir, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	posts := 0
	b := newBackend(stateDir)
	b.remoteHTTPClient = &http.Client{Transport: backendRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		posts++
		if request.Header.Get("Origin") != "https://new-dashboard.example.test" {
			t.Fatalf("new enrollment Origin = %q", request.Header.Get("Origin"))
		}
		duringRequest, err := config.Load(filepath.Join(stateDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if duringRequest.UnifiedDashboard.URL != "https://new-dashboard.example.test" ||
			duringRequest.UnifiedDashboard.Enrolled ||
			duringRequest.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
			t.Fatalf("different-origin pre-enrollment state = %#v", duringRequest.UnifiedDashboard)
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	result, err := b.EnrollDashboard(context.Background(), cli.DashboardEnrollOptions{
		URL: "https://NEW-DASHBOARD.EXAMPLE.test:443", TokenFile: tokenPath,
	})
	if err != nil {
		t.Fatalf("different-origin enrollment: %v", err)
	}
	if posts != 1 || result.URL != "https://new-dashboard.example.test" {
		t.Fatalf("different-origin result = %#v, posts = %d", result, posts)
	}
}

func TestBackendPermissionsUsesConfiguredActiveUserDesktopIPC(t *testing.T) {
	stateDir := t.TempDir()
	b := newBackend(stateDir)
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	endpointDir, err := os.MkdirTemp("", "executor-cli-permissions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(endpointDir) })
	if runtime.GOOS == "windows" {
		cfg.DesktopEndpoint = `\\.\pipe\` + filepath.Base(endpointDir) + "-desktop"
	} else {
		cfg.DesktopEndpoint = filepath.Join(endpointDir, "desktop.sock")
	}
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	values, err := secrets.Load(stateDir)
	if err != nil {
		t.Fatalf("Load secrets: %v", err)
	}

	requests := make(chan bool, 2)
	server := ipc.NewRPCServer(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey), func(_ context.Context, method string, raw []byte) (any, error) {
		if method != desktop.RPCMethodDesktopPermissions {
			t.Fatalf("desktop method = %q", method)
		}
		var params desktop.RPCDesktopPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatalf("decode permission params: %v", err)
		}
		requests <- params.Request
		return permissionmodel.NewReport("darwin", params.Request, []permissionmodel.Item{{
			ID: "screen_recording", Label: "Screen Recording", State: permissionmodel.StatePending, Required: true,
		}}), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-serverErr:
			if err != nil {
				t.Fatalf("desktop server stop: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("desktop server did not stop")
		}
	}()

	for _, request := range []bool{false, true} {
		var report permissionmodel.Report
		deadline := time.Now().Add(3 * time.Second)
		for {
			report, err = b.Permissions(context.Background(), request)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("Permissions(%v): %v", request, err)
		}
		if report.Requested != request || report.Ready || report.Platform != "darwin" {
			t.Fatalf("Permissions(%v) = %#v", request, report)
		}
		if got := <-requests; got != request {
			t.Fatalf("Permissions(%v) IPC request = %v", request, got)
		}
	}
}

type backendRoundTripFunc func(*http.Request) (*http.Response, error)

func (f backendRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSetupCreatesConfigAndSecretsWithDashboardKeyBootstrapURL(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	result, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Domain != "executor.example.com" {
		t.Fatalf("domain = %q", result.Domain)
	}
	if result.RecoveryKey == "" {
		t.Fatal("expected one-time recovery key")
	}
	cfg, err := config.Load(filepath.Join(stateDir, "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !strings.HasPrefix(result.Dashboard, "http://"+cfg.DashboardAddress+"/?token=") {
		t.Fatalf("dashboard bootstrap URL = %q", result.Dashboard)
	}
	if strings.Contains(result.RecoveryKey, "127.0.0.1") {
		t.Fatalf("recovery key unexpectedly contains a dashboard bootstrap URL: %q", result.RecoveryKey)
	}
	loaded, err := secrets.Load(stateDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(result.Dashboard, loaded.DashboardKey) {
		t.Fatalf("dashboard bootstrap URL should contain dashboard key, got %q", result.Dashboard)
	}
}

func TestSetupMovesNewConfigOffOccupiedLoopbackPorts(t *testing.T) {
	agentListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer agentListener.Close()
	dashboardListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dashboardListener.Close()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	b.defaultConfig = func(stateDir string) config.Config {
		cfg := config.Default(stateDir)
		cfg.AgentAddress = agentListener.Addr().String()
		cfg.DashboardAddress = dashboardListener.Addr().String()
		return cfg
	}
	result, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	loaded, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.AgentAddress == agentListener.Addr().String() {
		t.Fatalf("agent retained occupied address %q", loaded.AgentAddress)
	}
	if loaded.DashboardAddress == dashboardListener.Addr().String() {
		t.Fatalf("dashboard retained occupied address %q", loaded.DashboardAddress)
	}
	if !strings.HasPrefix(loaded.AgentAddress, "127.0.0.1:") || !strings.HasPrefix(loaded.DashboardAddress, "127.0.0.1:") {
		t.Fatalf("fallback addresses are not loopback: agent=%q dashboard=%q", loaded.AgentAddress, loaded.DashboardAddress)
	}
	if loaded.AgentAddress == loaded.DashboardAddress {
		t.Fatalf("agent and dashboard selected the same address %q", loaded.AgentAddress)
	}
	if !strings.HasPrefix(result.Dashboard, "http://"+loaded.DashboardAddress+"/?token=") {
		t.Fatalf("dashboard bootstrap URL %q does not use selected address %q", result.Dashboard, loaded.DashboardAddress)
	}
}

func TestSetupMovesExistingConfigOffForeignAgentPort(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("initial Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	foreignAddress := strings.TrimPrefix(foreign.URL, "http://")
	cfg.AgentAddress = foreignAddress
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("repeat Setup: %v", err)
	}
	loaded, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load repeated config: %v", err)
	}
	if loaded.AgentAddress == foreignAddress {
		t.Fatalf("repeat setup retained foreign agent address %q", loaded.AgentAddress)
	}
}

func TestSetupMovesExistingConfigOffForeignDashboardPort(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("initial Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	foreignAddress := strings.TrimPrefix(foreign.URL, "http://")
	cfg.DashboardAddress = foreignAddress
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("repeat Setup: %v", err)
	}
	loaded, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load repeated config: %v", err)
	}
	if loaded.DashboardAddress == foreignAddress {
		t.Fatalf("repeat setup retained foreign dashboard address %q", loaded.DashboardAddress)
	}
}

func TestRuntimeOperationsUseIndependentController(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	fake := &fakeControl{killResult: control.Result{RecoveryKey: "recovery", URLSecret: "url-secret"}}
	b.loadControl = func(string) (controlRuntime, error) { return fake, nil }
	result, err := b.Kill(context.Background())
	if err != nil || result.RecoveryKey != "recovery" || result.URLSecret != "url-secret" {
		t.Fatalf("Kill result=%#v err=%v", result, err)
	}
	if err := b.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if fake.kills != 1 || fake.resumes != 1 {
		t.Fatalf("controller calls: %#v", fake)
	}
}

func TestDoctorReportsOfflineRuntime(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.AgentAddress = "127.0.0.1:0"
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save isolated config: %v", err)
	}

	result, err := b.Doctor(context.Background(), false)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if result.Healthy {
		t.Fatalf("doctor should be unhealthy when runtime is offline: %#v", result)
	}
	found := map[string]bool{}
	for _, check := range result.Checks {
		if check.Name == "agent" || check.Name == "dashboard" {
			found[check.Name] = true
			if check.OK || !strings.Contains(check.Detail, "offline") {
				t.Fatalf("runtime check = %#v, want offline", check)
			}
		}
	}
	if !found["agent"] || !found["dashboard"] {
		t.Fatalf("missing agent or dashboard check: %#v", result.Checks)
	}
}

func TestRemoteOAuthRegistrationCheckAcceptsExecutorValidationResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/register" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid_client_metadata"}`))
	}))
	defer server.Close()

	check := remoteOAuthRegistrationCheck{baseURL: server.URL, client: server.Client()}
	detail, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(detail, "DCR reachable") {
		t.Fatalf("detail = %q", detail)
	}
}

func TestRemoteOAuthRegistrationCheckExplainsCloudflareBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
	}))
	defer server.Close()

	check := remoteOAuthRegistrationCheck{baseURL: server.URL, client: server.Client()}
	_, err := check.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Security > Analytics > Events") || !strings.Contains(err.Error(), "Bot Fight Mode") {
		t.Fatalf("err = %v, want actionable Cloudflare edge-policy diagnosis", err)
	}
}

func TestDoctorFullChecksPublicOAuthRegistrationWithInvalidMetadata(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/register" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client_metadata"}`))
	}))
	defer server.Close()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	b.remoteBaseURL = func(string) string { return server.URL }
	b.remoteHTTPClient = server.Client()

	if _, err := b.Doctor(context.Background(), false); err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if requests != 0 {
		t.Fatalf("normal doctor made %d remote requests, want 0", requests)
	}
	result, err := b.Doctor(context.Background(), true)
	if err != nil {
		t.Fatalf("Doctor full: %v", err)
	}
	if requests != 1 {
		t.Fatalf("full doctor made %d remote requests, want 1", requests)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "remote OAuth DCR" {
			found = true
			if !check.OK || !strings.Contains(check.Detail, "DCR reachable") {
				t.Fatalf("remote DCR check = %#v", check)
			}
		}
	}
	if !found {
		t.Fatalf("full doctor omitted remote OAuth DCR check: %#v", result.Checks)
	}
}

func TestStatusRejectsForeignServiceOnAgentPort(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.AgentAddress = strings.TrimPrefix(foreign.URL, "http://")
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Agent != "offline" {
		t.Fatalf("foreign service reported as agent state %q, want offline", status.Agent)
	}
}

func TestStatusAcceptsAuthenticatedExecutorHealthResponse(t *testing.T) {
	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	values, err := secrets.Load(b.stateDir)
	if err != nil {
		t.Fatalf("Load secrets: %v", err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.executor/health" || r.Header.Get("X-Executor-Health-Key") != values.DashboardKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Executor-Health", "ok")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer agent.Close()
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.AgentAddress = strings.TrimPrefix(agent.URL, "http://")
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Agent != "online" {
		t.Fatalf("authenticated Executor health response reported as %q, want online", status.Agent)
	}
}

func TestStatusRejectsForeignServiceOnDashboardPort(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.DashboardAddress = strings.TrimPrefix(foreign.URL, "http://")
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Dashboard != "offline" {
		t.Fatalf("foreign service reported as dashboard state %q, want offline", status.Dashboard)
	}
}

func TestStatusAcceptsAuthenticatedExecutorDashboardHealthResponse(t *testing.T) {
	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	values, err := secrets.Load(b.stateDir)
	if err != nil {
		t.Fatalf("Load secrets: %v", err)
	}
	dashboardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.executor/health" || r.Header.Get("X-Executor-Health-Key") != values.DashboardKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Executor-Health", "ok")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dashboardServer.Close()
	cfg, err := config.Load(b.configPath())
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	cfg.DashboardAddress = strings.TrimPrefix(dashboardServer.URL, "http://")
	if err := config.Save(b.configPath(), cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Dashboard != "online" {
		t.Fatalf("authenticated Executor dashboard health response reported as %q, want online", status.Dashboard)
	}
}

type fakeControl struct {
	killResult control.Result
	killErr    error
	resumeErr  error
	kills      int
	resumes    int
}

func (f *fakeControl) Kill(context.Context) (control.Result, error) {
	f.kills++
	return f.killResult, f.killErr
}

func (f *fakeControl) Resume(context.Context) error {
	f.resumes++
	return f.resumeErr
}

func TestConfigPathUsesStateDir(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	if got, want := b.configPath(), filepath.Join(stateDir, "config.json"); got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
}

func TestBackendHonorsInstallerManagedPathsFromEnvironment(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "custom", "executor.json")
	tokenPath := filepath.Join(t.TempDir(), "tunnel", "executor.token")
	t.Setenv("EXECUTOR_CONFIG_PATH", configPath)
	t.Setenv("CLOUDFLARED_TOKEN_PATH", tokenPath)

	b := newBackend(t.TempDir())
	if got := b.configPath(); got != configPath {
		t.Fatalf("configPath = %q, want installer path %q", got, configPath)
	}
	if got := b.cloudflaredTokenPath(); got != tokenPath {
		t.Fatalf("cloudflaredTokenPath = %q, want installer path %q", got, tokenPath)
	}
}

func TestDefaultStateDirUsesStableInstalledLocation(t *testing.T) {
	t.Setenv("EXECUTOR_STATE_DIR", "")
	if runtime.GOOS == "windows" {
		programData := filepath.Join(t.TempDir(), "ProgramData")
		t.Setenv("ProgramData", programData)
		if got, want := defaultStateDir(), filepath.Join(programData, "Executor"); got != want {
			t.Fatalf("defaultStateDir = %q, want %q", got, want)
		}
		return
	}
	if got, want := defaultStateDir(), "/var/lib/executor"; got != want {
		t.Fatalf("defaultStateDir = %q, want %q", got, want)
	}
}

func TestUnavailableErrorWrapsUnderlyingCause(t *testing.T) {
	t.Parallel()

	err := unavailable("runtime controller", errors.New("not linked"))
	if err == nil || !strings.Contains(err.Error(), "runtime controller unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRotateReturnsRecoveryKeyAndURLSecret(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	fake := &fakeControl{killResult: control.Result{RecoveryKey: "safe-recovery", URLSecret: "safe-url"}}
	b.loadControl = func(string) (controlRuntime, error) { return fake, nil }
	result, err := b.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.RecoveryKey == "" || result.URLSecret == "" {
		t.Fatalf("Rotate returned incomplete credentials: %#v", result)
	}
	if fake.kills != 1 || fake.resumes != 1 {
		t.Fatalf("Rotate did not restart services safely: %#v", fake)
	}
}

func TestSetupConfiguresCloudflareFromSecureTokenFile(t *testing.T) {
	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer top-secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"id": "acct-1", "name": "Primary"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			if got := r.URL.Query().Get("account.id"); got != "acct-1" {
				t.Fatalf("account filter = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{
				{"id": "zone-1", "name": "example.com"},
				{"id": "zone-2", "name": "prod.example.com"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "tunnel-1", "name": "executor", "token": "issued-token"}})
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/configurations":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-2/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-2/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "dns-1", "type": "CNAME", "name": "executor.prod.example.com", "content": "tunnel-1.cfargotunnel.com", "proxied": true}})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tunnel-1/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": "issued-token"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	t.Setenv("EXECUTOR_CLOUDFLARE_API_BASE_URL", server.URL)
	b := newBackend(stateDir)
	result, err := b.Setup(context.Background(), setupOptions{
		Domain:              "executor.prod.example.com",
		CloudflareTokenFile: tokenPath,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.Domain != "executor.prod.example.com" {
		t.Fatalf("domain = %q", result.Domain)
	}
	cfg, err := config.Load(filepath.Join(stateDir, "config.json"))
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if cfg.Cloudflare.ZoneID != "zone-2" || cfg.Cloudflare.TunnelID != "tunnel-1" || cfg.Cloudflare.DNSRecordID != "dns-1" {
		t.Fatalf("unexpected Cloudflare metadata: %#v", cfg.Cloudflare)
	}
	if cfg.Cloudflare.TokenFilePath == "" {
		t.Fatalf("missing token file path: %#v", cfg.Cloudflare)
	}
	if strings.Contains(readFile(t, filepath.Join(stateDir, "config.json")), "top-secret-token") {
		t.Fatal("config should not persist API token")
	}
}

func TestSetupRejectsInsecureCloudflareTokenFilePermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newBackend(stateDir)
	if _, err := b.Setup(context.Background(), setupOptions{
		Domain:              "executor.example.com",
		CloudflareTokenFile: tokenPath,
	}); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("err = %v, want 0600 validation", err)
	}
}

func TestSetupDoesNotConsumeOneTimeRecoveryKeyBeforeCloudflareSucceeds(t *testing.T) {
	stateDir := t.TempDir()
	tokenPath := filepath.Join(stateDir, "api-token.txt")
	if err := os.WriteFile(tokenPath, []byte("top-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"temporary failure"}]}`))
	}))
	defer server.Close()
	t.Setenv("EXECUTOR_CLOUDFLARE_API_BASE_URL", server.URL)

	_, err := newBackend(stateDir).Setup(context.Background(), setupOptions{
		Domain: "executor.example.com", CloudflareTokenFile: tokenPath,
	})
	if err == nil {
		t.Fatal("Setup succeeded despite Cloudflare failure")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "secrets.json")); !os.IsNotExist(statErr) {
		t.Fatalf("one-time secrets were consumed before Cloudflare succeeded: %v", statErr)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

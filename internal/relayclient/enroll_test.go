package relayclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestEnrollPostsExactWorkerBodyAndDeletesTokenOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
	configPath, cfg, values := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-enrollment-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectedOrigin := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/device/enroll" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Origin") != expectedOrigin {
			http.Error(writer, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-only-enrollment-token" {
			t.Fatal("enrollment bearer was not read from the token file")
		}
		var body enrollmentRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
		if err != nil {
			t.Fatal(err)
		}
		if body.DeviceID != cfg.UnifiedDashboard.DeviceID || body.Name == "" || body.Platform != runtime.GOOS ||
			body.Arch != runtime.GOARCH || body.Version != "test-version" || body.MCPURL != "https://executor.example.test/mcp" ||
			body.Generation != values.Generation || body.PublicJWK != identity.PublicJWK() {
			t.Fatalf("enrollment body = %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"device":{"device_id":"ok"}}`))
	}))
	defer server.Close()
	expectedOrigin = server.URL
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("successful enrollment token still exists: %v", err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UnifiedDashboard.Enrolled {
		t.Fatal("successful enrollment was not durably recorded")
	}
}

func TestEnrollPreservesTokenFileWhenPostFailsAndDoesNotEchoCredential(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	marker := "test-only-enrollment-token-marker"
	if err := os.WriteFile(tokenPath, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	})
	if err == nil {
		t.Fatal("failed enrollment returned nil")
	}
	if containsSensitive(err.Error(), marker, tokenPath, server.URL) {
		t.Fatalf("enrollment error exposed sensitive input: %v", err)
	}
	if _, statErr := os.Stat(tokenPath); statErr != nil {
		t.Fatalf("failed enrollment deleted token file: %v", statErr)
	}
	loaded, loadErr := config.Load(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.UnifiedDashboard.Enrolled {
		t.Fatal("failed enrollment was recorded as enrolled")
	}
}

func TestEnrollSaveFailurePreservesRetryToken(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	err := enrollWithOperations(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	}, enrollmentOperations{
		SaveConfig:  func(string, config.Config) error { return errors.New("forced save failure") },
		RemoveToken: os.Remove,
	})
	if err == nil || containsSensitive(err.Error(), tokenPath, "test-only-enrollment-token") {
		t.Fatalf("save failure error = %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("enrollment POST count = %d, want 1", posts.Load())
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("save failure removed retry token: %v", err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.Enrolled {
		t.Fatal("save failure persisted enrollment")
	}
}

func TestEnrollCleanupFailureCanRetryWithoutAnotherPost(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	options := EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	}
	err := enrollWithOperations(context.Background(), options, enrollmentOperations{
		SaveConfig:  config.Save,
		RemoveToken: func(string) error { return errors.New("forced cleanup failure") },
	})
	if err == nil || containsSensitive(err.Error(), tokenPath, "test-only-enrollment-token") {
		t.Fatalf("cleanup failure error = %v", err)
	}
	loaded, loadErr := config.Load(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !loaded.UnifiedDashboard.Enrolled {
		t.Fatal("cleanup failure lost durable enrollment state")
	}
	if !loaded.UnifiedDashboard.EnrollmentCleanupPending {
		t.Fatal("cleanup failure lost durable cleanup-pending state")
	}
	if _, statErr := os.Stat(tokenPath); statErr != nil {
		t.Fatalf("cleanup failure unexpectedly removed token: %v", statErr)
	}

	if err := enrollWithOperations(context.Background(), options, enrollmentOperations{
		SaveConfig: config.Save, RemoveToken: os.Remove,
	}); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("cleanup retry POST count = %d, want 1", posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup retry left token: %v", err)
	}
	loaded, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.EnrollmentCleanupPending {
		t.Fatal("cleanup retry left cleanup-pending state set")
	}
}

func TestCleanupPendingEnrollmentDoesNotReadTokenOrPostAgain(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("invalid token contents with spaces"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		http.Error(writer, "unexpected POST", http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	cfg.UnifiedDashboard.Enrolled = true
	cfg.UnifiedDashboard.EnrollmentCleanupPending = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	}); err != nil {
		t.Fatalf("cleanup-only enrollment: %v", err)
	}
	if posts.Load() != 0 {
		t.Fatalf("cleanup-only enrollment POST count = %d, want 0", posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup-only enrollment left token: %v", err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.EnrollmentCleanupPending {
		t.Fatal("cleanup-only enrollment left cleanup-pending state set")
	}
}

func TestEnrolledDevicePostsAgainForExplicitSameOriginToken(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-reenrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		posts.Add(1)
		if request.Header.Get("Authorization") != "Bearer test-only-reenrollment-token" {
			t.Fatal("re-enrollment bearer was not read from the explicit token file")
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: server.Client(), ExecutorVersion: "test-version",
	}); err != nil {
		t.Fatalf("explicit same-origin re-enrollment: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("explicit same-origin re-enrollment POST count = %d, want 1", posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit same-origin re-enrollment left token: %v", err)
	}
}

func TestEnrollSendsCanonicalWHATWGOriginHeader(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
		want string
	}{
		{name: "explicit default port", url: "https://DASHBOARD.EXAMPLE.test:443", want: "https://dashboard.example.test"},
		{name: "non-default port", url: "https://DASHBOARD.EXAMPLE.test:8443", want: "https://dashboard.example.test:8443"},
		{name: "IPv6 default port", url: "https://[2001:DB8::1]:443", want: "https://[2001:db8::1]"},
		{name: "IPv6 non-default port", url: "https://[2001:DB8::1]:8443", want: "https://[2001:db8::1]:8443"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configPath, cfg, _ := relayFixture(t)
			tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
			if err := os.WriteFile(tokenPath, []byte("test-only-origin-token"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.UnifiedDashboard.URL = test.url
			if err := config.Save(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: enrollmentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if got := request.Header.Get("Origin"); got != test.want {
					t.Fatalf("Origin = %q, want %q", got, test.want)
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"device":{"device_id":"ok"}}`)),
				}, nil
			})}
			if err := Enroll(context.Background(), EnrollOptions{
				ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: client, ExecutorVersion: "test-version",
			}); err != nil {
				t.Fatalf("Enroll: %v", err)
			}
		})
	}
}

func TestEnrollRejectsNonASCIIOriginHostBeforeRequest(t *testing.T) {
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-origin-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.UnifiedDashboard.URL = "https://d\u00e4shboard.example.test"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	requested := false
	client := &http.Client{Transport: enrollmentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requested = true
		return nil, errors.New("unexpected request")
	})}
	err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: client, ExecutorVersion: "test-version",
	})
	if err == nil {
		t.Fatal("Enroll accepted a non-ASCII origin host")
	}
	if requested {
		t.Fatal("non-ASCII origin reached the HTTP transport")
	}
	if _, statErr := os.Stat(tokenPath); statErr != nil {
		t.Fatalf("rejected origin removed enrollment token: %v", statErr)
	}
}

func relayFixture(t *testing.T) (string, config.Config, secrets.Values) {
	t.Helper()
	stateDir := t.TempDir()
	values, err := secrets.Create(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(stateDir)
	cfg.Domain = "executor.example.test"
	configPath := filepath.Join(stateDir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return configPath, cfg, values
}

func containsSensitive(value string, markers ...string) bool {
	for _, marker := range markers {
		if marker != "" && strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

type enrollmentRoundTripFunc func(*http.Request) (*http.Response, error)

func (f enrollmentRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

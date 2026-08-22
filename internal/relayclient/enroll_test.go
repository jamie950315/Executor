package relayclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/device/enroll" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
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

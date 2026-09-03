package relayclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

func TestConcurrentEnrollmentProcessesUsingSameTokenPostOnce(t *testing.T) {
	if os.Getenv("EXECUTOR_ENROLLMENT_PROCESS_HELPER") == "1" {
		certificate, err := os.ReadFile(os.Getenv("EXECUTOR_ENROLLMENT_CERTIFICATE"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certificate) {
			t.Fatal("parse enrollment test certificate")
		}
		readyPath := os.Getenv("EXECUTOR_ENROLLMENT_HELPER_READY")
		if readyPath != "" {
			if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		err = Enroll(context.Background(), EnrollOptions{
			ConfigPath: os.Getenv("EXECUTOR_ENROLLMENT_CONFIG"),
			TokenFile:  os.Getenv("EXECUTOR_ENROLLMENT_TOKEN"),
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			}}},
			ExecutorVersion: "test-version",
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-concurrent-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	firstPost := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if posts.Add(1) == 1 {
			close(firstPost)
			<-releaseFirst
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	certificatePath := filepath.Join(t.TempDir(), "server-certificate.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	startHelper := func(readyPath string) (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		output := &bytes.Buffer{}
		command := exec.Command(executable, "-test.run=^TestConcurrentEnrollmentProcessesUsingSameTokenPostOnce$")
		command.Stdout = output
		command.Stderr = output
		command.Env = append(os.Environ(),
			"EXECUTOR_ENROLLMENT_PROCESS_HELPER=1",
			"EXECUTOR_ENROLLMENT_CERTIFICATE="+certificatePath,
			"EXECUTOR_ENROLLMENT_CONFIG="+configPath,
			"EXECUTOR_ENROLLMENT_TOKEN="+tokenPath,
			"EXECUTOR_ENROLLMENT_HELPER_READY="+readyPath,
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		return command, output
	}

	first, firstOutput := startHelper("")
	select {
	case <-firstPost:
	case <-time.After(5 * time.Second):
		_ = first.Process.Kill()
		t.Fatalf("first enrollment process did not POST: %s", firstOutput.String())
	}
	secondReady := filepath.Join(t.TempDir(), "second-ready")
	second, secondOutput := startHelper(secondReady)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(secondReady); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			_ = first.Process.Kill()
			_ = second.Process.Kill()
			t.Fatalf("second enrollment process did not start: %s", secondOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	postsBeforeRelease := posts.Load()
	close(releaseFirst)
	if err := first.Wait(); err != nil {
		_ = second.Process.Kill()
		t.Fatalf("first enrollment process failed: %v: %s", err, firstOutput.String())
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second enrollment process failed: %v: %s", err, secondOutput.String())
	}
	if postsBeforeRelease != 1 || posts.Load() != 1 {
		t.Fatalf("concurrent enrollment POST counts = before release %d, total %d; want 1, 1", postsBeforeRelease, posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("concurrent enrollment left token: %v", err)
	}
}

func TestEnrollmentCancellationWhileWaitingPreservesToken(t *testing.T) {
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-cancelled-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	firstPost := make(chan struct{})
	releaseRequests := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if posts.Add(1) == 1 {
			close(firstPost)
		}
		<-releaseRequests
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
	firstResult := make(chan error, 1)
	go func() { firstResult <- Enroll(context.Background(), options) }()
	select {
	case <-firstPost:
	case <-time.After(5 * time.Second):
		close(releaseRequests)
		t.Fatal("first enrollment did not reach the server")
	}
	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan error, 1)
	go func() { secondResult <- Enroll(ctx, options) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-secondResult:
		if !errors.Is(err, context.Canceled) {
			close(releaseRequests)
			t.Fatalf("waiting enrollment cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseRequests)
		t.Fatal("waiting enrollment did not return promptly after cancellation")
	}
	if posts.Load() != 1 {
		close(releaseRequests)
		t.Fatalf("cancelled waiting enrollment POST count = %d, want 1", posts.Load())
	}
	if _, err := os.Stat(tokenPath); err != nil {
		close(releaseRequests)
		t.Fatalf("cancelled waiting enrollment consumed token: %v", err)
	}
	close(releaseRequests)
	if err := <-firstResult; err != nil {
		t.Fatalf("first enrollment failed after release: %v", err)
	}
}

func TestWaitingEnrollmentDoesNotReportSuccessWhenNoPostOccurred(t *testing.T) {
	configPath, cfg, _ := relayFixture(t)
	cfg.UnifiedDashboard.URL = "https://already-enrolled.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	missingTokenPath := filepath.Join(t.TempDir(), "missing-enrollment.token")
	lock, err := acquireEnrollmentLock(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- Enroll(context.Background(), EnrollOptions{
			ConfigPath: configPath, TokenFile: missingTokenPath, ExecutorVersion: "test-version",
		})
	}()
	time.Sleep(100 * time.Millisecond)
	lock.release()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("waiting enrollment reported success without a token or POST")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting enrollment did not finish after the lock was released")
	}
}

func TestEnrollmentWorkflowsForDifferentConfigPathsDoNotBlockEachOther(t *testing.T) {
	firstConfigPath, _, _ := relayFixture(t)
	secondConfigPath, _, _ := relayFixture(t)
	firstTokenPath := filepath.Join(t.TempDir(), "first-enrollment.token")
	secondTokenPath := filepath.Join(t.TempDir(), "second-enrollment.token")
	if err := os.WriteFile(firstTokenPath, []byte("test-only-first-independent-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondTokenPath, []byte("test-only-second-independent-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstRequest := make(chan struct{})
	secondRequest := make(chan struct{})
	releaseFirst := make(chan struct{})
	client := &http.Client{Transport: enrollmentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "first-independent.example.test":
			close(firstRequest)
			<-releaseFirst
		case "second-independent.example.test":
			close(secondRequest)
		default:
			return nil, errors.New("unexpected independent enrollment host")
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- Enroll(context.Background(), EnrollOptions{
			ConfigPath: firstConfigPath, DashboardURL: "https://first-independent.example.test",
			TokenFile: firstTokenPath, HTTPClient: client, ExecutorVersion: "test-version",
		})
	}()
	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		close(releaseFirst)
		t.Fatal("first independent enrollment did not reach the request boundary")
	}
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- Enroll(context.Background(), EnrollOptions{
			ConfigPath: secondConfigPath, DashboardURL: "https://second-independent.example.test",
			TokenFile: secondTokenPath, HTTPClient: client, ExecutorVersion: "test-version",
		})
	}()
	select {
	case <-secondRequest:
	case <-time.After(time.Second):
		close(releaseFirst)
		<-firstResult
		t.Fatal("different config path was blocked by the first enrollment workflow")
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first independent enrollment failed: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second independent enrollment failed: %v", err)
	}
}

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
	if loaded.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		t.Fatal("successful enrollment left a cleanup fingerprint")
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

func TestEnrollRejectsSymlinkTokenBeforeHTTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires host-specific privileges")
	}
	configPath, cfg, _ := relayFixture(t)
	tokenTarget := filepath.Join(t.TempDir(), "enrollment-target.token")
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenTarget, []byte("test-only-symlink-enrollment-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tokenTarget, tokenPath); err != nil {
		t.Fatal(err)
	}
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	requested := false
	client := &http.Client{Transport: enrollmentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requested = true
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, TokenFile: tokenPath, HTTPClient: client, ExecutorVersion: "test-version",
	})
	if err == nil {
		t.Fatal("symlink enrollment token was accepted")
	}
	if requested {
		t.Fatal("symlink enrollment token reached the HTTP transport")
	}
	if _, err := os.Stat(tokenTarget); err != nil {
		t.Fatalf("symlink rejection removed the token target: %v", err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.Enrolled {
		t.Fatal("symlink enrollment token was recorded as enrolled")
	}
}

func TestEnrollDoesNotDeleteReplacementTokenCreatedDuringRequest(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	oldToken := "test-only-original-enrollment-token"
	newToken := "test-only-replacement-enrollment-token"
	if err := os.WriteFile(tokenPath, []byte(oldToken), 0o600); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		if err := os.WriteFile(tokenPath, []byte(newToken), 0o600); err != nil {
			t.Error(err)
			http.Error(writer, "replacement failed", http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusCreated)
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
		t.Fatal("replacement token was silently deleted after enrollment")
	}
	if containsSensitive(err.Error(), oldToken, newToken, tokenPath) {
		t.Fatalf("replacement cleanup error exposed sensitive input: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("enrollment POST count = %d, want 1", posts.Load())
	}
	replacement, readErr := os.ReadFile(tokenPath)
	if readErr != nil {
		t.Fatalf("replacement token was removed: %v", readErr)
	}
	if string(replacement) != newToken {
		t.Fatal("replacement token content changed")
	}
	loaded, loadErr := config.Load(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !loaded.UnifiedDashboard.Enrolled || loaded.UnifiedDashboard.EnrollmentCleanupFingerprint == "" {
		t.Fatal("completed enrollment did not retain retry-safe cleanup state")
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
	fingerprint := loaded.UnifiedDashboard.EnrollmentCleanupFingerprint
	if fingerprint == "" {
		t.Fatal("cleanup failure lost durable cleanup fingerprint")
	}
	plainHash := sha256.Sum256([]byte("test-only-enrollment-token"))
	if fingerprint == hex.EncodeToString(plainHash[:]) || fingerprint == base64.RawURLEncoding.EncodeToString(plainHash[:]) {
		t.Fatal("cleanup fingerprint is an unkeyed token hash")
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
	if loaded.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		t.Fatal("cleanup retry left a cleanup fingerprint")
	}
}

func TestCleanupFingerprintWithMissingFileClearsWithoutAnotherPost(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-missing-cleanup-token"), 0o600); err != nil {
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
	if err := enrollWithOperations(context.Background(), options, enrollmentOperations{
		SaveConfig: config.Save,
		RemoveToken: func(string) error {
			return errors.New("forced cleanup failure")
		},
	}); err == nil {
		t.Fatal("forced cleanup failure returned nil")
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(context.Background(), options); err != nil {
		t.Fatalf("missing-file cleanup retry: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("missing-file cleanup retry POST count = %d, want 1", posts.Load())
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UnifiedDashboard.EnrollmentCleanupFingerprint != "" {
		t.Fatal("missing-file cleanup retry left a cleanup fingerprint")
	}
}

func TestFinalSaveFailureWithNewTokenAtSamePathPostsNewToken(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	oldToken := "test-only-old-enrollment-token"
	newToken := "test-only-new-enrollment-token"
	if err := os.WriteFile(tokenPath, []byte(oldToken), 0o600); err != nil {
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
	forceFinalEnrollmentSaveFailure(t, options)
	if err := os.WriteFile(tokenPath, []byte(newToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(context.Background(), options); err != nil {
		t.Fatalf("new-token retry: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("new-token retry POST count = %d, want 2", posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new-token retry left token: %v", err)
	}
}

func TestFinalSaveFailureWithOldTokenAtSamePathCleansWithoutAnotherPost(t *testing.T) {
	t.Parallel()
	configPath, cfg, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	token := "test-only-old-enrollment-token"
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
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
	forceFinalEnrollmentSaveFailure(t, options)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(context.Background(), options); err != nil {
		t.Fatalf("old-token retry: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("old-token retry POST count = %d, want 1", posts.Load())
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old-token retry left token: %v", err)
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
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	requested := false
	client := &http.Client{Transport: enrollmentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requested = true
		return nil, errors.New("unexpected request")
	})}
	err = Enroll(context.Background(), EnrollOptions{
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

func forceFinalEnrollmentSaveFailure(t *testing.T, options EnrollOptions) {
	t.Helper()
	saves := 0
	err := enrollWithOperations(context.Background(), options, enrollmentOperations{
		SaveConfig: func(path string, cfg config.Config) error {
			saves++
			if saves == 2 {
				return errors.New("forced final save failure")
			}
			return config.Save(path, cfg)
		},
		RemoveToken: os.Remove,
	})
	if err == nil {
		t.Fatal("forced final save failure returned nil")
	}
	if _, statErr := os.Stat(options.TokenFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("forced final save failure did not remove token: %v", statErr)
	}
	loaded, loadErr := config.Load(options.ConfigPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.UnifiedDashboard.EnrollmentCleanupFingerprint == "" {
		t.Fatal("forced final save failure did not preserve cleanup fingerprint")
	}
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

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/secrets"
)

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
	if !strings.HasPrefix(result.Dashboard, "http://127.0.0.1:8788/?token=") {
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

func TestRuntimeOperationsReportUnavailable(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if _, err := b.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Status err = %v, want unavailable", err)
	}
	if err := b.Kill(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Kill err = %v, want unavailable", err)
	}
	if err := b.Resume(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Resume err = %v, want unavailable", err)
	}
}

func TestDoctorReportsRuntimeUnavailableUntilWired(t *testing.T) {
	t.Parallel()

	b := newBackend(t.TempDir())
	if _, err := b.Setup(context.Background(), setupOptions{Domain: "executor.example.com"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	result, err := b.Doctor(context.Background(), false)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if result.Healthy {
		t.Fatalf("doctor should be unhealthy when runtime is unavailable: %#v", result)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "runtime" {
			found = true
			if check.OK || !strings.Contains(check.Detail, "unavailable") {
				t.Fatalf("runtime check = %#v, want unavailable", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing runtime check: %#v", result.Checks)
	}
}

func TestConfigPathUsesStateDir(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	b := newBackend(stateDir)
	if got, want := b.configPath(), filepath.Join(stateDir, "config.json"); got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
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

	result, err := b.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.RecoveryKey == "" || result.URLSecret == "" {
		t.Fatalf("Rotate returned incomplete credentials: %#v", result)
	}
}

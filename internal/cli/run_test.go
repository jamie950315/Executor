package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeBackend struct {
	called string
}

func (f *fakeBackend) Setup(context.Context, SetupOptions) (SetupResult, error) {
	f.called = "setup"
	return SetupResult{Domain: "executor.example.com", MCPURL: "https://executor.example.com/mcp", RecoveryKey: "recovery-once"}, nil
}
func (f *fakeBackend) Status(context.Context) (Status, error) {
	f.called = "status"
	return Status{State: "armed", Domain: "executor.example.com"}, nil
}
func (f *fakeBackend) Kill(context.Context) error   { f.called = "kill"; return nil }
func (f *fakeBackend) Resume(context.Context) error { f.called = "resume"; return nil }
func (f *fakeBackend) Rotate(context.Context) (RotateResult, error) {
	f.called = "rotate"
	return RotateResult{URLSecret: "rotated-once", RecoveryKey: "new-recovery-once"}, nil
}

func TestRunRotatePrintsNewRecoveryMaterialOnce(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"rotate"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "rotate" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	for _, text := range []string{"rotated-once", "new-recovery-once", "shown once"} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("rotate output %q missing %q", stdout.String(), text)
		}
	}
}
func (f *fakeBackend) Doctor(context.Context, bool) (DoctorResult, error) {
	f.called = "doctor"
	return DoctorResult{Healthy: true, Checks: []Check{{Name: "agent", OK: true}}}, nil
}
func (f *fakeBackend) EnableURLSecret(context.Context) (string, error) {
	f.called = "auth-enable-url-secret"
	return "https://executor.example.com/secret/mcp", nil
}

func TestRunStatusJSON(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"status", "--json"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "status" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	var status Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.State != "armed" {
		t.Fatalf("invalid status JSON: %q err=%v", stdout.String(), err)
	}
}

func TestRunKillAndResume(t *testing.T) {
	for _, command := range []string{"kill", "resume"} {
		backend := &fakeBackend{}
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{command}, backend, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%q", command, code, stderr.String())
		}
		if backend.called != command || !strings.Contains(strings.ToLower(stdout.String()), command) {
			t.Fatalf("%s called=%q stdout=%q", command, backend.called, stdout.String())
		}
	}
}

func TestRunSetupPrintsOneTimeConnectionMaterial(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--domain", "executor.example.com"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "setup" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	for _, text := range []string{"executor.example.com", "https://executor.example.com/mcp", "recovery-once", "shown once"} {
		if !strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(text)) {
			t.Fatalf("setup output %q missing %q", stdout.String(), text)
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"unknown"}, &fakeBackend{}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "Executor") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type fakeBackend struct {
	called                 string
	setupOptions           SetupOptions
	setupResult            *SetupResult
	killErr                error
	rotateErr              error
	enableErr              error
	permissionRequests     []bool
	dashboardEnrollOptions DashboardEnrollOptions
}

func (f *fakeBackend) EnrollDashboard(_ context.Context, options DashboardEnrollOptions) (DashboardEnrollResult, error) {
	f.called = "dashboard-enroll"
	f.dashboardEnrollOptions = options
	return DashboardEnrollResult{DeviceID: "device-1", URL: options.URL}, nil
}

func (f *fakeBackend) DashboardStatus(context.Context) (DashboardStatusResult, error) {
	f.called = "dashboard-status"
	return DashboardStatusResult{
		URL: "https://dashboard.example.test", DeviceID: "device-1", Enrolled: true, Relay: "configured",
	}, nil
}

func (f *fakeBackend) Setup(_ context.Context, options SetupOptions) (SetupResult, error) {
	f.called = "setup"
	f.setupOptions = options
	if f.setupResult != nil {
		return *f.setupResult, nil
	}
	return SetupResult{Domain: "executor.example.com", MCPURL: "https://executor.example.com/mcp", RecoveryKey: "recovery-once"}, nil
}
func (f *fakeBackend) Status(context.Context) (Status, error) {
	f.called = "status"
	return Status{State: "armed", Domain: "executor.example.com"}, nil
}
func (f *fakeBackend) Kill(context.Context) (RotateResult, error) {
	f.called = "kill"
	return RotateResult{RecoveryKey: "kill-recovery-once", URLSecret: "kill-url-once", Dashboard: "http://127.0.0.1:8788/?token=kill-dashboard-once"}, f.killErr
}
func (f *fakeBackend) Resume(context.Context) error { f.called = "resume"; return nil }
func (f *fakeBackend) Rotate(context.Context) (RotateResult, error) {
	f.called = "rotate"
	return RotateResult{URLSecret: "rotated-once", RecoveryKey: "new-recovery-once", Dashboard: "http://127.0.0.1:8788/?token=rotated-dashboard-once"}, f.rotateErr
}

func TestRunRotatePreservesMaterialWhenResumeFails(t *testing.T) {
	backend := &fakeBackend{rotateErr: errors.New("resume failed")}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"rotate"}, backend, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "new-recovery-once") || !strings.Contains(stdout.String(), "rotated-dashboard-once") || !strings.Contains(stderr.String(), "resume failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunRotatePrintsNewRecoveryMaterialOnce(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"rotate"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "rotate" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	for _, text := range []string{"rotated-once", "new-recovery-once", "rotated-dashboard-once", "shown once"} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("rotate output %q missing %q", stdout.String(), text)
		}
	}
}
func (f *fakeBackend) Doctor(context.Context, bool) (DoctorResult, error) {
	f.called = "doctor"
	return DoctorResult{Healthy: true, Checks: []Check{{Name: "agent", OK: true}}}, nil
}
func (f *fakeBackend) EnableURLSecret(context.Context) (RotateResult, error) {
	f.called = "auth-enable-url-secret"
	return RotateResult{Endpoint: "https://executor.example.com/secret/mcp", RecoveryKey: "url-mode-recovery", URLSecret: "url-mode-secret", Dashboard: "http://127.0.0.1:8788/?token=url-mode-dashboard"}, f.enableErr
}
func (f *fakeBackend) Permissions(_ context.Context, request bool) (permissionmodel.Report, error) {
	f.called = "permissions"
	f.permissionRequests = append(f.permissionRequests, request)
	state := permissionmodel.StateDenied
	if request {
		state = permissionmodel.StatePending
	}
	report := permissionmodel.NewReport("darwin", request, []permissionmodel.Item{
		{ID: "screen_recording", Label: "Screen Recording", State: state, Required: true},
		{ID: "full_disk_access", Label: "Full Disk Access", State: permissionmodel.StateManual, Required: false},
	})
	report.RestartRequired = request
	return report, nil
}

func TestRunPermissionsStatusJSON(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"permissions", "status", "--json"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "permissions" || len(backend.permissionRequests) != 1 || backend.permissionRequests[0] {
		t.Fatalf("code=%d called=%q requests=%#v stderr=%q", code, backend.called, backend.permissionRequests, stderr.String())
	}
	var report permissionmodel.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("permission JSON = %q: %v", stdout.String(), err)
	}
	if report.Platform != "darwin" || report.Requested || report.Ready || len(report.Items) != 2 {
		t.Fatalf("permission report = %#v", report)
	}
}

func TestRunPermissionsRequestAllDoesNotClaimPendingIsGranted(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"permissions", "request-all"}, backend, &stdout, &stderr)
	if code != 0 || len(backend.permissionRequests) != 1 || !backend.permissionRequests[0] {
		t.Fatalf("code=%d requests=%#v stderr=%q", code, backend.permissionRequests, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Screen Recording: pending") || !strings.Contains(output, "complete any operating-system prompts") || !strings.Contains(strings.ToLower(output), "restart") {
		t.Fatalf("permission request output = %q", output)
	}
	if strings.Contains(strings.ToLower(output), "all permissions are approved") {
		t.Fatalf("pending permission was reported as approved: %q", output)
	}
}

func TestRunPermissionsRejectsUnexpectedArgumentsBeforeRequesting(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"permissions", "request-all", "typo"}, backend, &stdout, &stderr)
	if code != 2 || len(backend.permissionRequests) != 0 {
		t.Fatalf("code=%d requests=%#v stdout=%q stderr=%q", code, backend.permissionRequests, stdout.String(), stderr.String())
	}
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

func TestRunEnableURLSecretPreservesRotatedMaterialOnResumeFailure(t *testing.T) {
	backend := &fakeBackend{enableErr: errors.New("resume failed")}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"auth", "enable-url-secret"}, backend, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "url-mode-recovery") || !strings.Contains(stdout.String(), "url-mode-dashboard") || !strings.Contains(stderr.String(), "resume failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
		if command == "kill" && (!strings.Contains(stdout.String(), "kill-recovery-once") || !strings.Contains(stdout.String(), "kill-dashboard-once") || !strings.Contains(stdout.String(), "shown once")) {
			t.Fatalf("kill output omitted one-time recovery material: %q", stdout.String())
		}
	}
}

func TestRunKillPreservesRotatedCredentialsOnPartialFailure(t *testing.T) {
	backend := &fakeBackend{killErr: errors.New("desktop already stopped")}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"kill"}, backend, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "kill-recovery-once") || !strings.Contains(stdout.String(), "kill-dashboard-once") || !strings.Contains(stderr.String(), "desktop already stopped") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunSetupPrintsOneTimeConnectionMaterial(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"setup",
		"--domain", "executor.example.com",
		"--cloudflare-token-file", "/secure/cloudflare.token",
		"--cloudflare-account-id", "acct-1",
		"--cloudflare-zone-id", "zone-1",
		"--cloudflare-tunnel-name", "executor-prod",
	}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "setup" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	if got := backend.setupOptions.CloudflareTokenFile; got != "/secure/cloudflare.token" {
		t.Fatalf("cloudflare token file = %q", got)
	}
	if got := backend.setupOptions.CloudflareAccountID; got != "acct-1" {
		t.Fatalf("cloudflare account ID = %q", got)
	}
	if got := backend.setupOptions.CloudflareZoneID; got != "zone-1" {
		t.Fatalf("cloudflare zone ID = %q", got)
	}
	if got := backend.setupOptions.CloudflareTunnelName; got != "executor-prod" {
		t.Fatalf("cloudflare tunnel name = %q", got)
	}
	for _, text := range []string{"executor.example.com", "https://executor.example.com/mcp", "Streamable HTTP", "executor stdio", "recovery-once", "shown once"} {
		if !strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(text)) {
			t.Fatalf("setup output %q missing %q", stdout.String(), text)
		}
	}
}

func TestRunSetupDoesNotClaimAnEmptyRecoveryKey(t *testing.T) {
	backend := &fakeBackend{setupResult: &SetupResult{
		Domain: "executor.example.com",
		MCPURL: "https://executor.example.com/mcp",
	}}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"setup", "--domain", "executor.example.com"}, backend, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Recovery key (shown once):") {
		t.Fatalf("setup claimed to show an empty recovery key: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Existing recovery key remains unchanged") {
		t.Fatalf("setup did not explain repeat setup behavior: %q", stdout.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"unknown"}, &fakeBackend{}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "Executor") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunDashboardEnrollPassesOnlyTokenFilePathAndDoesNotPrintCredential(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"dashboard", "enroll", "--url", "https://dashboard.example.test", "--token-file", "/secure/enrollment.token",
	}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "dashboard-enroll" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	if backend.dashboardEnrollOptions.URL != "https://dashboard.example.test" || backend.dashboardEnrollOptions.TokenFile != "/secure/enrollment.token" {
		t.Fatalf("dashboard enroll options = %#v", backend.dashboardEnrollOptions)
	}
	if strings.Contains(stdout.String(), "/secure/enrollment.token") || strings.Contains(stdout.String(), "token") {
		t.Fatalf("dashboard enrollment output exposed token material: %q", stdout.String())
	}
}

func TestRunDashboardStatusVerifiesEnrollmentWithoutCredentialMaterial(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"dashboard", "status", "--json"}, backend, &stdout, &stderr)
	if code != 0 || backend.called != "dashboard-status" {
		t.Fatalf("code=%d called=%q stderr=%q", code, backend.called, stderr.String())
	}
	var result DashboardStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("Dashboard status JSON = %q: %v", stdout.String(), err)
	}
	if !result.Enrolled || result.Relay != "configured" || result.URL != "https://dashboard.example.test" {
		t.Fatalf("Dashboard status = %#v", result)
	}
	for _, forbidden := range []string{"token", "recovery", "credential"} {
		if strings.Contains(strings.ToLower(stdout.String()), forbidden) {
			t.Fatalf("Dashboard status exposed credential material: %q", stdout.String())
		}
	}
}

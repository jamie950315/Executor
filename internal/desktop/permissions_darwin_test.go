//go:build darwin

package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type fakeDarwinPermissionProvider struct {
	state     darwinPermissionState
	requested bool
}

func TestDarwinRequestPermissionsPreservesNativeReportWhenOptionalSettingsCannotOpen(t *testing.T) {
	openKey := "open|" + darwinFullDiskSettingsURL
	runner := &fakeRunner{errors: map[string]error{openKey: errors.New("LaunchServices unavailable")}}
	provider := &fakeDarwinPermissionProvider{state: darwinPermissionState{NativeAvailable: true}}
	backend := newDarwinBackend(runner, &fakeEventPoster{})
	backend.permissions = provider

	report, err := backend.RequestPermissions(context.Background())
	if err != nil {
		t.Fatalf("optional settings failure discarded native permission report: %v", err)
	}
	if !report.Requested || !report.RestartRequired || len(report.Items) != 4 {
		t.Fatalf("native permission report = %#v", report)
	}
	if detail := report.Items[3].Detail; !strings.Contains(detail, "could not open") {
		t.Fatalf("Full Disk Access detail = %q", detail)
	}
}

func (p *fakeDarwinPermissionProvider) Status() darwinPermissionState { return p.state }
func (p *fakeDarwinPermissionProvider) Request() darwinPermissionState {
	p.requested = true
	return p.state
}

func TestDarwinPermissionStatusReportsEachNativeBoundary(t *testing.T) {
	provider := &fakeDarwinPermissionProvider{state: darwinPermissionState{
		NativeAvailable: true,
		ScreenRecording: true,
		Accessibility:   false,
		InputControl:    true,
	}}
	backend := newDarwinBackend(&fakeRunner{}, &fakeEventPoster{})
	backend.permissions = provider

	report, err := backend.PermissionStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Requested || len(report.Items) != 4 {
		t.Fatalf("unexpected permission report: %#v", report)
	}
	want := map[string]permissionmodel.State{
		"screen_recording": permissionmodel.StateGranted,
		"accessibility":    permissionmodel.StateDenied,
		"input_control":    permissionmodel.StateGranted,
		"full_disk_access": permissionmodel.StateManual,
	}
	for _, item := range report.Items {
		if item.State != want[item.ID] {
			t.Fatalf("permission %s state=%s want=%s", item.ID, item.State, want[item.ID])
		}
	}
}

func TestDarwinPermissionReportDoesNotClaimNoCGOPromptsWereRequested(t *testing.T) {
	report := darwinPermissionReport(darwinPermissionState{NativeAvailable: false}, true)
	if report.Ready {
		t.Fatalf("no-CGO permission report was ready: %#v", report)
	}
	for _, item := range report.Items[:3] {
		if item.State != permissionmodel.StateUnavailable {
			t.Fatalf("no-CGO permission %s state=%s want=unavailable", item.ID, item.State)
		}
	}
}

func TestDarwinRequestPermissionsTriggersNativePromptsAndOpensManualSettings(t *testing.T) {
	runner := &fakeRunner{}
	provider := &fakeDarwinPermissionProvider{state: darwinPermissionState{NativeAvailable: true}}
	backend := newDarwinBackend(runner, &fakeEventPoster{})
	backend.permissions = provider

	report, err := backend.RequestPermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !provider.requested || !report.Requested || !report.RestartRequired {
		t.Fatalf("request did not trigger permission flow: provider=%v report=%#v", provider.requested, report)
	}
	want := "open|x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
	if len(runner.calls) != 1 || runner.calls[0] != want {
		t.Fatalf("settings commands=%#v want=%q", runner.calls, want)
	}
	for _, item := range report.Items[:3] {
		if item.State != permissionmodel.StatePending {
			t.Fatalf("requested permission %s state=%s want=pending", item.ID, item.State)
		}
	}
}

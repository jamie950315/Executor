package desktop

import (
	"context"
	"testing"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type permissionBackendFake struct {
	recordingActionBackend
	requested bool
}

func (b *permissionBackendFake) PermissionStatus(context.Context) (permissionmodel.Report, error) {
	return permissionmodel.NewReport("test", false, []permissionmodel.Item{
		{ID: "screen_recording", Label: "Screen Recording", State: permissionmodel.StateGranted, Required: true},
	}), nil
}

func (b *permissionBackendFake) RequestPermissions(context.Context) (permissionmodel.Report, error) {
	b.requested = true
	return permissionmodel.NewReport("test", true, []permissionmodel.Item{
		{ID: "screen_recording", Label: "Screen Recording", State: permissionmodel.StatePending, Required: true},
	}), nil
}

func TestControllerPermissionsReturnsStatusWithoutRequesting(t *testing.T) {
	backend := &permissionBackendFake{}
	report, err := (&Controller{backend: backend}).Permissions(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if backend.requested || report.Requested || !report.Ready || report.Platform != "test" {
		t.Fatalf("unexpected status report: requested=%v report=%#v", backend.requested, report)
	}
}

func TestControllerPermissionsRequestsThroughActivePlatformBackend(t *testing.T) {
	backend := &permissionBackendFake{}
	report, err := (&Controller{backend: backend}).Permissions(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !backend.requested || !report.Requested || report.Ready {
		t.Fatalf("unexpected request report: requested=%v report=%#v", backend.requested, report)
	}
}

func TestControllerPermissionsDoesNotClaimUnsupportedBackendIsReady(t *testing.T) {
	report, err := (&Controller{backend: unavailableBackend{reason: "desktop locked"}}).Permissions(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.Items) != 1 || report.Items[0].State != permissionmodel.StateUnavailable {
		t.Fatalf("unexpected unsupported report: %#v", report)
	}
}

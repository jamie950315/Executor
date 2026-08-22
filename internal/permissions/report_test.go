package permissions

import "testing"

func TestNewReportIsReadyOnlyWhenEveryRequiredPermissionIsGranted(t *testing.T) {
	report := NewReport("macos", true, []Item{
		{ID: "screen_recording", Label: "Screen Recording", State: StateGranted, Required: true},
		{ID: "accessibility", Label: "Accessibility", State: StatePending, Required: true},
		{ID: "full_disk_access", Label: "Full Disk Access", State: StateManual, Required: false},
	})
	if report.Ready {
		t.Fatal("report was ready while a required permission was pending")
	}

	report = NewReport("windows", false, []Item{
		{ID: "desktop_session", Label: "Desktop Session", State: StateGranted, Required: true},
		{ID: "screen_recording", Label: "Screen Capture", State: StateNotRequired, Required: true},
	})
	if !report.Ready {
		t.Fatal("report was not ready when every required permission was satisfied")
	}
}

func TestNewReportRejectsDuplicateOrIncompletePermissionItems(t *testing.T) {
	for _, items := range [][]Item{
		{{ID: "", Label: "Screen Recording", State: StateGranted}},
		{{ID: "screen_recording", Label: "", State: StateGranted}},
		{{ID: "screen_recording", Label: "Screen Recording", State: "unknown"}},
		{
			{ID: "screen_recording", Label: "Screen Recording", State: StateGranted},
			{ID: "screen_recording", Label: "Duplicate", State: StatePending},
		},
	} {
		if _, err := ValidateReport(NewReport("macos", false, items)); err == nil {
			t.Fatalf("ValidateReport accepted invalid items: %#v", items)
		}
	}
}

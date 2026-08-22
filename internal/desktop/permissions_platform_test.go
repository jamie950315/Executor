package desktop

import (
	"testing"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

func TestWindowsPermissionReportRequiresAnActiveDesktopSessionOnly(t *testing.T) {
	ready := windowsPermissionReport(true, true)
	if !ready.Ready || !ready.Requested || len(ready.Items) != 3 {
		t.Fatalf("unexpected ready Windows report: %#v", ready)
	}
	for _, item := range ready.Items[1:] {
		if item.State != permissionmodel.StateNotRequired {
			t.Fatalf("Windows permission %s state=%s want=not_required", item.ID, item.State)
		}
	}

	offline := windowsPermissionReport(false, false)
	if offline.Ready || offline.Items[0].State != permissionmodel.StateUnavailable {
		t.Fatalf("unexpected offline Windows report: %#v", offline)
	}
}

func TestLinuxPermissionReportCoversEveryCurrentDesktopDependency(t *testing.T) {
	x11 := linuxPermissionReport(backendX11, availableTools{
		"import": true, "gdbus": true, "xdotool": true, "wmctrl": true,
	}, true, true, false, false)
	if !x11.Ready || x11.Platform != "linux-x11" || len(x11.Items) != 6 {
		t.Fatalf("unexpected X11 report: %#v", x11)
	}

	missingBus := linuxPermissionReport(backendX11, availableTools{
		"import": true, "gdbus": true, "xdotool": true, "wmctrl": true,
	}, false, true, false, false)
	if missingBus.Ready || permissionState(missingBus, "accessibility") != permissionmodel.StateUnavailable {
		t.Fatalf("X11 report trusted the gdbus executable without a live accessibility bus: %#v", missingBus)
	}

	missingWindowControl := linuxPermissionReport(backendX11, availableTools{
		"import": true, "gdbus": true, "xdotool": true,
	}, true, true, false, false)
	if missingWindowControl.Ready || permissionState(missingWindowControl, "window_control") != permissionmodel.StateUnavailable {
		t.Fatalf("X11 report omitted wmctrl readiness: %#v", missingWindowControl)
	}

	wayland := linuxPermissionReport(backendWayland, availableTools{
		"grim": true, "gdbus": true, "wtype": true,
	}, true, false, true, false)
	if wayland.Ready || wayland.Platform != "linux-wayland" || !wayland.Requested {
		t.Fatalf("unexpected Wayland report: %#v", wayland)
	}
	states := map[string]permissionmodel.State{}
	for _, item := range wayland.Items {
		states[item.ID] = item.State
	}
	if states["pointer_input"] != permissionmodel.StateManual || states["keyboard_input"] != permissionmodel.StateGranted {
		t.Fatalf("unexpected Wayland input states: %#v", states)
	}
	unauthorizedYdotool := linuxPermissionReport(backendWayland, availableTools{
		"grim": true, "gdbus": true, "wtype": true, "ydotool": true,
	}, true, false, false, false)
	if permissionState(unauthorizedYdotool, "pointer_input") != permissionmodel.StateManual {
		t.Fatalf("Wayland report trusted ydotool without a live authorized daemon: %#v", unauthorizedYdotool)
	}

	wsl := linuxPermissionReport(backendUnavailable, availableTools{}, false, false, false, true)
	if wsl.Platform != "wsl" || wsl.Ready {
		t.Fatalf("unexpected WSL report: %#v", wsl)
	}
}

func TestDetectWSLEnvironmentFallsBackToKernelRelease(t *testing.T) {
	if !detectWSLEnvironment(map[string]string{}, "5.15.167.4-microsoft-standard-WSL2") {
		t.Fatal("WSL kernel release was not detected without inherited WSL environment variables")
	}
	if detectWSLEnvironment(map[string]string{}, "6.8.0-generic") {
		t.Fatal("ordinary Linux kernel was reported as WSL")
	}
}

func TestReconcileLinuxToolsNeverReportsRemovedOrNewToolsUsableBeforeRestart(t *testing.T) {
	cached := availableTools{"import": true, "gdbus": true, "xdotool": true}
	fresh := availableTools{"import": false, "gdbus": true, "xdotool": true, "wmctrl": true}
	effective, changed := reconcileLinuxTools(cached, fresh, []string{"import", "gdbus", "xdotool", "wmctrl"})
	if !changed {
		t.Fatal("tool inventory change did not request restart")
	}
	if effective["import"] || effective["wmctrl"] {
		t.Fatalf("effective tools claimed removed or newly installed tools were usable: %#v", effective)
	}
	if !effective["gdbus"] || !effective["xdotool"] {
		t.Fatalf("effective tools lost unchanged dependencies: %#v", effective)
	}
}

func permissionState(report permissionmodel.Report, id string) permissionmodel.State {
	for _, item := range report.Items {
		if item.ID == id {
			return item.State
		}
	}
	return ""
}

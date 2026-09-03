//go:build linux

package desktop

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxBackend_ParsesWmctrlWindowsAndResolvesAppNames(t *testing.T) {
	runner := &linuxTestRunner{
		outputs: map[string][]byte{
			commandKey("wmctrl", "-lp"):          []byte("0x01200003  0 4242 host My Window\n"),
			commandKey("cat", "/proc/4242/comm"): []byte("firefox\n"),
		},
	}
	backend := linuxBackend{
		runner: runner,
		env:    map[string]string{"DISPLAY": ":0"},
		kind:   backendX11,
		tools:  availableTools{"wmctrl": true},
	}

	windows, err := backend.Windows(context.Background())
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	if len(windows) != 1 || windows[0].ID != 0x01200003 || windows[0].PID != 4242 || windows[0].App != "firefox" || windows[0].Title != "My Window" {
		t.Fatalf("unexpected windows payload: %#v", windows)
	}
}

func TestLinuxBackend_ActivateUsesWmctrlInsteadOfLaunching(t *testing.T) {
	runner := &linuxTestRunner{
		outputs: map[string][]byte{
			commandKey("wmctrl", "-lp"):          []byte("0x01200003  0 4242 host My Window\n"),
			commandKey("cat", "/proc/4242/comm"): []byte("firefox\n"),
		},
	}
	backend := linuxBackend{
		runner: runner,
		env:    map[string]string{"DISPLAY": ":0"},
		kind:   backendX11,
		tools:  availableTools{"wmctrl": true},
	}

	if err := backend.App(context.Background(), AppAction{Type: AppActionActivate, Name: "firefox"}); err != nil {
		t.Fatalf("activate app: %v", err)
	}
	if len(runner.calls) < 3 || runner.calls[2] != commandKey("wmctrl", "-ia", "0x01200003") {
		t.Fatalf("expected wmctrl focus call, got %#v", runner.calls)
	}
}

func TestLinuxBackend_ActivateReturnsUnavailableWithoutMatchingWindow(t *testing.T) {
	backend := linuxBackend{
		runner: &linuxTestRunner{outputs: map[string][]byte{commandKey("wmctrl", "-lp"): []byte("")}},
		env:    map[string]string{"DISPLAY": ":0"},
		kind:   backendX11,
		tools:  availableTools{"wmctrl": true},
	}

	err := backend.App(context.Background(), AppAction{Type: AppActionActivate, Name: "firefox"})
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected unavailable error, got %T", err)
	}
}

func TestLinuxBackend_MouseFailureReleasesButtonAndModifiers(t *testing.T) {
	failingMove := commandKey("xdotool", "mousemove", "30", "40")
	runner := &linuxTestRunner{errors: map[string]error{failingMove: errors.New("input failed")}}
	backend := linuxBackend{
		runner: runner,
		env:    map[string]string{"DISPLAY": ":0"},
		kind:   backendX11,
		tools:  availableTools{"xdotool": true},
	}

	err := backend.Mouse(context.Background(), MouseAction{
		Type: MouseActionDrag,
		Path: []Point{{X: 10, Y: 20}, {X: 30, Y: 40}},
		Keys: []string{"CTRL"},
	})
	if err == nil {
		t.Fatal("failed drag returned nil")
	}
	wantTail := []string{
		commandKey("xdotool", "mouseup", "1"),
		commandKey("xdotool", "keyup", "ctrl"),
	}
	if len(runner.calls) < len(wantTail) ||
		runner.calls[len(runner.calls)-2] != wantTail[0] ||
		runner.calls[len(runner.calls)-1] != wantTail[1] {
		t.Fatalf("failed drag cleanup calls = %#v, want tail %#v", runner.calls, wantTail)
	}
	for _, cleanup := range wantTail {
		if !runner.deadlines[cleanup] {
			t.Fatalf("cleanup command %q had no bounded deadline", cleanup)
		}
	}
}

func TestLinuxPermissionStatusChecksLiveBusAndFlagsNewToolsForRestart(t *testing.T) {
	runner := &linuxTestRunner{outputs: map[string][]byte{
		commandKey("gdbus", "call", "--session", "--dest", "org.a11y.Bus", "--object-path", "/org/a11y/bus", "--method", "org.a11y.Bus.GetAddress"): []byte("('unix:path=/run/user/1000/at-spi/bus',)"),
	}}
	backend := linuxBackend{
		runner: runner,
		env:    map[string]string{"DISPLAY": ":0"},
		kind:   backendX11,
		tools: availableTools{
			"gdbus": true, "xdotool": true, "wmctrl": true,
		},
		detectTools: func(...string) availableTools {
			return availableTools{"import": true, "gdbus": true, "xdotool": true, "wmctrl": true}
		},
		kernelRelease: func() string { return "6.8.0-generic" },
	}

	report, err := backend.PermissionStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !report.RestartRequired {
		t.Fatalf("newly installed screenshot tool was usable without helper restart: %#v", report)
	}
	if permissionState(report, "accessibility") != "granted" {
		t.Fatalf("live accessibility bus was not checked: %#v", report)
	}
}

func TestLinuxPermissionStatusIsNotReadyUntilChangedInventoryRestarts(t *testing.T) {
	runner := &linuxTestRunner{outputs: map[string][]byte{
		commandKey("gdbus", "call", "--session", "--dest", "org.a11y.Bus", "--object-path", "/org/a11y/bus", "--method", "org.a11y.Bus.GetAddress"): []byte("('unix:path=/run/user/1000/at-spi/bus',)"),
	}}
	backend := linuxBackend{
		runner: runner, env: map[string]string{"DISPLAY": ":0"}, kind: backendX11,
		tools: availableTools{"import": true, "gdbus": true, "xdotool": true, "wmctrl": true},
		detectTools: func(...string) availableTools {
			return availableTools{"import": true, "gdbus": true, "xdotool": true, "wmctrl": true, "ydotool": true}
		},
		kernelRelease: func() string { return "6.8.0-generic" },
	}
	report, err := backend.PermissionStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !report.RestartRequired {
		t.Fatalf("changed helper inventory reported ready before restart: %#v", report)
	}
}

type linuxTestRunner struct {
	calls     []string
	outputs   map[string][]byte
	errors    map[string]error
	deadlines map[string]bool
}

func (f *linuxTestRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args...)
	f.calls = append(f.calls, key)
	if f.deadlines == nil {
		f.deadlines = map[string]bool{}
	}
	_, f.deadlines[key] = ctx.Deadline()
	if err := f.errors[key]; err != nil {
		return f.outputs[key], err
	}
	return f.outputs[key], nil
}

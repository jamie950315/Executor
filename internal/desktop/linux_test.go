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

type linuxTestRunner struct {
	calls   []string
	outputs map[string][]byte
}

func (f *linuxTestRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args...)
	f.calls = append(f.calls, key)
	return f.outputs[key], nil
}

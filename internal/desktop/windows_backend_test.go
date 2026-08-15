//go:build windows

package desktop

import (
	"context"
	"reflect"
	"testing"
)

func TestWindowsBackend_UsesExpectedPowerShellCommands(t *testing.T) {
	runner := &windowsTestRunner{
		outputs: map[string][]byte{
			commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsEnumWindowsScript()):   []byte(`[{"app":"notepad","title":"Notes","id":12}]`),
			commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsAccessibilityScript()): []byte(`{"application":"foreground","windows":[{"title":"Notes","role":"window"}]}`),
		},
	}
	backend := windowsBackend{runner: runner}

	if err := backend.Screenshot(context.Background(), `C:\Temp\shot.png`); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	windows, err := backend.Windows(context.Background())
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	tree, err := backend.Accessibility(context.Background())
	if err != nil {
		t.Fatalf("accessibility: %v", err)
	}
	if err := backend.Mouse(context.Background(), MouseAction{Type: MouseActionClick, X: 1, Y: 2, Button: MouseButtonLeft}); err != nil {
		t.Fatalf("mouse: %v", err)
	}
	if err := backend.Keyboard(context.Background(), KeyboardAction{Text: "hello"}); err != nil {
		t.Fatalf("keyboard: %v", err)
	}
	if err := backend.App(context.Background(), AppAction{Type: AppActionLaunch, Name: "notepad.exe"}); err != nil {
		t.Fatalf("app: %v", err)
	}

	if len(windows) != 1 || windows[0].App != "notepad" {
		t.Fatalf("unexpected windows payload: %#v", windows)
	}
	if tree.Application != "foreground" {
		t.Fatalf("unexpected accessibility payload: %#v", tree)
	}

	want := []string{
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsScreenshotScript(`C:\Temp\shot.png`)),
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsEnumWindowsScript()),
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsAccessibilityScript()),
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsMouseScript(MouseAction{Type: MouseActionClick, X: 1, Y: 2, Button: MouseButtonLeft})),
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsKeyboardScript(KeyboardAction{Text: "hello"})),
		commandKey("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsAppScript(AppAction{Type: AppActionLaunch, Name: "notepad.exe"})),
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected command sequence: %#v", runner.calls)
	}
}

type windowsTestRunner struct {
	calls   []string
	outputs map[string][]byte
}

func (f *windowsTestRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args...)
	f.calls = append(f.calls, key)
	return f.outputs[key], nil
}

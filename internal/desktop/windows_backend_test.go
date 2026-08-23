//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestWindowsDesktopAvailabilityProbeRealSession(t *testing.T) {
	expectation := os.Getenv("EXECUTOR_EXPECT_WINDOWS_DESKTOP_AVAILABLE")
	if expectation == "" {
		t.Skip("set EXECUTOR_EXPECT_WINDOWS_DESKTOP_AVAILABLE for a real Windows session probe")
	}
	got := (windowsBackend{runner: defaultCommandRunner{}}).Available(context.Background())
	want := expectation == "1"
	if got != want {
		t.Fatalf("desktop availability = %v, want %v for this Windows session", got, want)
	}
}

func TestWindowsEnumWindowsScriptProducesJSONArray(t *testing.T) {
	output, err := exec.CommandContext(
		context.Background(),
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		buildWindowsEnumWindowsScript(),
	).CombinedOutput()
	if err != nil {
		t.Fatalf("window enumeration script failed: %v\n%s", err, output)
	}

	var windows []Window
	if err := json.Unmarshal(output, &windows); err != nil {
		t.Fatalf("window enumeration script returned invalid JSON: %v\n%s", err, output)
	}
}

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

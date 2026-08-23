package desktop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestController_ReturnsStructuredUnavailableError(t *testing.T) {
	ctrl := &Controller{
		backend: unavailableBackend{reason: "desktop locked"},
	}

	_, err := ctrl.Windows(context.Background())
	if err == nil {
		t.Fatal("expected unavailable error")
	}

	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected UnavailableError, got %T", err)
	}
	if unavailable.Reason != "desktop locked" {
		t.Fatalf("unexpected unavailable reason: %#v", unavailable)
	}
}

func TestDetectLinuxBackend(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want backendKind
	}{
		{
			name: "wayland display wins",
			env: map[string]string{
				"XDG_SESSION_TYPE": "wayland",
				"WAYLAND_DISPLAY":  "wayland-0",
			},
			want: backendWayland,
		},
		{
			name: "x11 display",
			env: map[string]string{
				"DISPLAY": ":0",
			},
			want: backendX11,
		},
		{
			name: "unknown desktop",
			env:  map[string]string{},
			want: backendUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectLinuxBackend(tt.env); got != tt.want {
				t.Fatalf("detectLinuxBackend() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWaylandCommandSelectionUsesFallbackTools(t *testing.T) {
	tools := availableTools{
		"gnome-screenshot": true,
		"ydotool":          true,
	}

	screenshot, err := chooseWaylandScreenshotCommand(tools, "/tmp/shot.png")
	if err != nil {
		t.Fatalf("chooseWaylandScreenshotCommand: %v", err)
	}
	if !reflect.DeepEqual(screenshot, []string{"gnome-screenshot", "-f", "/tmp/shot.png"}) {
		t.Fatalf("unexpected screenshot fallback: %#v", screenshot)
	}

	keyboard, err := chooseWaylandKeyboardCommand(tools, KeyboardAction{Text: "hello"})
	if err != nil {
		t.Fatalf("chooseWaylandKeyboardCommand: %v", err)
	}
	if !reflect.DeepEqual(keyboard, []string{"ydotool", "type", "--key-delay", "1", "hello"}) {
		t.Fatalf("unexpected keyboard fallback: %#v", keyboard)
	}

	mouse, err := chooseWaylandMouseCommand(tools, MouseAction{Type: MouseActionClick, X: 10, Y: 20, Button: MouseButtonLeft})
	if err != nil {
		t.Fatalf("chooseWaylandMouseCommand: %v", err)
	}
	if !reflect.DeepEqual(mouse, []string{"ydotool", "mousemove", "--absolute", "10", "20", "click", "1"}) {
		t.Fatalf("unexpected mouse fallback: %#v", mouse)
	}
}

func TestWaylandCommandSelectionReturnsStructuredUnavailable(t *testing.T) {
	_, err := chooseWaylandKeyboardCommand(availableTools{}, KeyboardAction{Text: "hello"})
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected UnavailableError, got %T", err)
	}
	if !strings.Contains(unavailable.Reason, "wayland keyboard input unavailable") {
		t.Fatalf("unexpected unavailable reason: %#v", unavailable)
	}
}

func TestWindowsPowerShellBuilders(t *testing.T) {
	availability := buildWindowsDesktopAvailabilityScript()
	for _, required := range []string{"OpenInputDesktop", "GetUserObjectInformation", "WTSQuerySessionInformation", "WTSConnectState", "WTSActive", "'Default'"} {
		if !strings.Contains(availability, required) {
			t.Fatalf("Windows desktop availability probe missing %q", required)
		}
	}
	if strings.Contains(availability, "$PSVersionTable") {
		t.Fatal("Windows desktop availability probe only checks whether PowerShell starts")
	}

	screenshot := buildWindowsScreenshotScript(`C:\Temp\shot.png`)
	if !strings.Contains(screenshot, "CopyFromScreen") || !strings.Contains(screenshot, "shot.png") {
		t.Fatalf("unexpected screenshot script: %s", screenshot)
	}

	enumWindows := buildWindowsEnumWindowsScript()
	if !strings.Contains(enumWindows, "EnumWindows") || !strings.Contains(enumWindows, "ConvertTo-Json") {
		t.Fatalf("unexpected enum windows script: %s", enumWindows)
	}
	if strings.Contains(strings.ToLower(enumWindows), "$pid=") || !strings.Contains(enumWindows, "$processId=0") {
		t.Fatalf("window enumeration must avoid read-only $PID: %s", enumWindows)
	}

	keyboard := buildWindowsKeyboardScript(KeyboardAction{Text: "abc"})
	if !strings.Contains(keyboard, "SendWait") {
		t.Fatalf("unexpected keyboard script: %s", keyboard)
	}

	mouse := buildWindowsMouseScript(MouseAction{Type: MouseActionClick, X: 3, Y: 4, Button: MouseButtonLeft})
	if !strings.Contains(mouse, "SetCursorPos") || !strings.Contains(mouse, "mouse_event") {
		t.Fatalf("unexpected mouse script: %s", mouse)
	}

	app := buildWindowsAppScript(AppAction{Type: AppActionActivate, Name: "notepad"})
	if !strings.Contains(app, "AppActivate") {
		t.Fatalf("unexpected app script: %s", app)
	}
}

type unavailableBackend struct {
	reason string
}

func (u unavailableBackend) Screenshot(ctx context.Context, path string) error {
	return &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) Windows(ctx context.Context) ([]Window, error) {
	return nil, &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) Mouse(ctx context.Context, action MouseAction) error {
	return &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	return &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) App(ctx context.Context, action AppAction) error {
	return &UnavailableError{Reason: u.reason}
}

func (u unavailableBackend) Available(ctx context.Context) bool {
	return false
}

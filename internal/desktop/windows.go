//go:build windows

package desktop

import (
	"context"
	"encoding/json"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type windowsBackend struct {
	runner commandRunner
}

func (b windowsBackend) Screenshot(ctx context.Context, path string) error {
	_, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsScreenshotScript(path))
	if err != nil {
		return wrapDesktopError("windows screenshot unavailable", err)
	}
	return nil
}

func (b windowsBackend) Windows(ctx context.Context) ([]Window, error) {
	data, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsEnumWindowsScript())
	if err != nil {
		return nil, wrapDesktopError("windows window enumeration unavailable", err)
	}
	var windows []Window
	if len(data) == 0 {
		return windows, nil
	}
	if err := json.Unmarshal(data, &windows); err != nil {
		return nil, err
	}
	return windows, nil
}

func (b windowsBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	data, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsAccessibilityScript())
	if err != nil {
		return AccessibilityTree{}, wrapDesktopError("windows accessibility unavailable", err)
	}
	var tree AccessibilityTree
	if len(data) == 0 {
		return tree, nil
	}
	if err := json.Unmarshal(data, &tree); err != nil {
		return AccessibilityTree{}, err
	}
	return tree, nil
}

func (b windowsBackend) Mouse(ctx context.Context, action MouseAction) error {
	if _, err := expandMouseAction(action); err != nil {
		return err
	}
	if _, err := mouseButtonCode(action.Button); err != nil {
		return err
	}
	if _, err := normalizeModifiers(action.Keys); err != nil {
		return err
	}
	_, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsMouseScript(action))
	if err != nil {
		return wrapDesktopError("windows mouse input unavailable", err)
	}
	return nil
}

func (b windowsBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	if len(action.Keys) > 0 {
		if _, err := normalizeKeys(action.Keys); err != nil {
			return err
		}
	}
	_, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsKeyboardScript(action))
	if err != nil {
		return wrapDesktopError("windows keyboard input unavailable", err)
	}
	return nil
}

func (b windowsBackend) App(ctx context.Context, action AppAction) error {
	_, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsAppScript(action))
	if err != nil {
		return wrapDesktopError("windows application action unavailable", err)
	}
	return nil
}

func (b windowsBackend) Available(ctx context.Context) bool {
	_, err := b.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", buildWindowsDesktopAvailabilityScript())
	return err == nil
}

func (b windowsBackend) PermissionStatus(ctx context.Context) (permissionmodel.Report, error) {
	return windowsPermissionReport(b.Available(ctx), false), nil
}

func (b windowsBackend) RequestPermissions(ctx context.Context) (permissionmodel.Report, error) {
	return windowsPermissionReport(b.Available(ctx), true), nil
}

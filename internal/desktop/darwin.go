//go:build darwin

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

const darwinWindowsScript = "ObjC.import('Foundation'); var se = Application('System Events'); var apps = se.applicationProcesses.whose({backgroundOnly: false})(); JSON.stringify(apps.map(function(app) { try { var appName = ''; try { appName = app.name(); } catch (error) {} var wins = []; try { wins = app.windows().map(function(win) { var title = ''; var id = 0; try { title = win.name() || ''; } catch (error) {} try { id = win.id() || 0; } catch (error) {} return {title: title, id: id}; }); } catch (error) { wins = []; } return {app: appName, windows: wins}; } catch (error) { return {app: '', windows: []}; } }));"
const darwinAccessibilityScript = "ObjC.import('Foundation'); var se = Application('System Events'); var app = se.applicationProcesses.whose({frontmost: true})[0]; JSON.stringify({application: app ? app.name() : '', windows: app ? app.windows().map(function(win) { return {title: win.name() || '', role: 'window'}; }) : []});"

type eventPoster interface {
	PostMouse(action MouseAction) error
	PostKeyboard(action KeyboardAction) error
}

type darwinBackend struct {
	runner      commandRunner
	events      eventPoster
	permissions darwinPermissionProvider
	geometry    func(context.Context) (int, int, error)
}

func newDarwinBackend(runner commandRunner, events eventPoster) darwinBackend {
	return darwinBackend{runner: runner, events: events, permissions: defaultDarwinPermissionProvider(), geometry: mainDisplayDimensions}
}

type darwinPermissionState struct {
	NativeAvailable bool
	ScreenRecording bool
	Accessibility   bool
	InputControl    bool
}

type darwinPermissionProvider interface {
	Status() darwinPermissionState
	Request() darwinPermissionState
}

const darwinFullDiskSettingsURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"

func (b darwinBackend) PermissionStatus(context.Context) (permissionmodel.Report, error) {
	return darwinPermissionReport(b.permissions.Status(), false), nil
}

func (b darwinBackend) RequestPermissions(ctx context.Context) (permissionmodel.Report, error) {
	state := b.permissions.Request()
	report := darwinPermissionReport(state, true)
	if _, err := b.runner.Run(ctx, "open", darwinFullDiskSettingsURL); err != nil {
		for index := range report.Items {
			if report.Items[index].ID == "full_disk_access" {
				report.Items[index].Detail += "; could not open System Settings automatically: " + err.Error()
				break
			}
		}
	}
	return report, nil
}

func darwinPermissionReport(state darwinPermissionState, requested bool) permissionmodel.Report {
	nativeState := func(granted bool) permissionmodel.State {
		if !state.NativeAvailable {
			return permissionmodel.StateUnavailable
		}
		if granted {
			return permissionmodel.StateGranted
		}
		if requested {
			return permissionmodel.StatePending
		}
		return permissionmodel.StateDenied
	}
	report := permissionmodel.NewReport("macos", requested, []permissionmodel.Item{
		{ID: "screen_recording", Label: "Screen Recording", State: nativeState(state.ScreenRecording), Required: true, Detail: "Allows Executor Desktop.app to capture the primary display."},
		{ID: "accessibility", Label: "Accessibility", State: nativeState(state.Accessibility), Required: true, Detail: "Allows Executor Desktop.app to inspect and control applications."},
		{ID: "input_control", Label: "Input Control", State: nativeState(state.InputControl), Required: true, Detail: "Allows Executor Desktop.app to post keyboard and pointer events."},
		{ID: "full_disk_access", Label: "Full Disk Access", State: permissionmodel.StateManual, Required: false, Detail: "Add the installed executor binary when access to TCC-protected files is required.", SettingsURL: darwinFullDiskSettingsURL},
	})
	report.RestartRequired = requested && state.NativeAvailable && (!state.ScreenRecording || !state.Accessibility || !state.InputControl)
	return report
}

func (b darwinBackend) Screenshot(ctx context.Context, path string) error {
	geometry := b.geometry
	if geometry == nil {
		geometry = mainDisplayDimensions
	}
	width, height, err := geometry(ctx)
	if err != nil || width < 1 || height < 1 {
		return wrapDesktopError("main display geometry unavailable", err)
	}
	if output, err := b.runner.Run(ctx, "screencapture", "-x", "-D", "1", path); err != nil {
		return wrapDesktopError("screenshot unavailable", withCommandDiagnostic(err, output))
	}
	if output, err := b.runner.Run(ctx, "sips", "-z", fmt.Sprintf("%d", height), fmt.Sprintf("%d", width), path); err != nil {
		return wrapDesktopError("screenshot coordinate normalization unavailable", withCommandDiagnostic(err, output))
	}
	return nil
}

func withCommandDiagnostic(err error, output []byte) error {
	diagnostic := strings.TrimSpace(string(output))
	if diagnostic == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, diagnostic)
}

func (b darwinBackend) Windows(ctx context.Context) ([]Window, error) {
	data, err := b.runner.Run(ctx, "osascript", "-l", "JavaScript", "-e", darwinWindowsScript)
	if err != nil {
		return nil, wrapDesktopError("window enumeration unavailable", err)
	}

	var payload []struct {
		App     string `json:"app"`
		Windows []struct {
			Title string `json:"title"`
			ID    int    `json:"id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode windows payload: %w", err)
	}

	windows := make([]Window, 0)
	for _, item := range payload {
		for _, window := range item.Windows {
			windows = append(windows, Window{
				App:   item.App,
				Title: window.Title,
				ID:    window.ID,
			})
		}
	}
	return windows, nil
}

func (b darwinBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	data, err := b.runner.Run(ctx, "osascript", "-l", "JavaScript", "-e", darwinAccessibilityScript)
	if err != nil {
		return AccessibilityTree{}, wrapDesktopError("accessibility unavailable", err)
	}

	var tree AccessibilityTree
	if err := json.Unmarshal(data, &tree); err != nil {
		return AccessibilityTree{}, fmt.Errorf("decode accessibility payload: %w", err)
	}
	return tree, nil
}

func (b darwinBackend) Mouse(ctx context.Context, action MouseAction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.events.PostMouse(action); err != nil {
		return wrapDesktopError("mouse input unavailable", err)
	}
	return nil
}

func (b darwinBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if action.Text != "" {
		if _, err := b.runner.Run(ctx, "osascript", "-e", fmt.Sprintf("tell application \"System Events\" to keystroke %q", action.Text)); err != nil {
			return wrapDesktopError("keyboard input unavailable", err)
		}
		return nil
	}
	if len(action.Keys) > 0 {
		primary, modifiers, err := splitKeyChord(action.Keys)
		if err != nil {
			return err
		}
		keyCode, err := darwinKeyCode(primary)
		if err != nil {
			return err
		}
		modifierNames := make([]string, 0, len(modifiers))
		for _, modifier := range modifiers {
			modifierNames = append(modifierNames, string(modifier))
		}
		if err := b.events.PostKeyboard(KeyboardAction{KeyCode: keyCode, Modifiers: modifierNames}); err != nil {
			return wrapDesktopError("keyboard input unavailable", err)
		}
		return nil
	}
	if err := b.events.PostKeyboard(action); err != nil {
		return wrapDesktopError("keyboard input unavailable", err)
	}
	return nil
}

func (b darwinBackend) App(ctx context.Context, action AppAction) error {
	var script string
	switch action.Type {
	case AppActionActivate:
		script = fmt.Sprintf("tell application %q to activate", action.Name)
	case AppActionLaunch:
		script = fmt.Sprintf("tell application %q to launch", action.Name)
	case AppActionQuit:
		script = fmt.Sprintf("tell application %q to quit", action.Name)
	default:
		return fmt.Errorf("unsupported app action %q", action.Type)
	}
	if _, err := b.runner.Run(ctx, "osascript", "-e", script); err != nil {
		return wrapDesktopError("application action unavailable", err)
	}
	return nil
}

func (b darwinBackend) Available(ctx context.Context) bool {
	_, err := b.runner.Run(ctx, "osascript", "-e", `tell application "System Events" to count processes`)
	return err == nil
}

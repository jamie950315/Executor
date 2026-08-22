package desktop

import (
	"strings"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

func windowsPermissionReport(sessionAvailable, requested bool) permissionmodel.Report {
	sessionState := permissionmodel.StateUnavailable
	if sessionAvailable {
		sessionState = permissionmodel.StateGranted
	}
	return permissionmodel.NewReport("windows", requested, []permissionmodel.Item{
		{ID: "desktop_session", Label: "Active Desktop Session", State: sessionState, Required: true, Detail: "Executor Desktop must run in the logged-in user's interactive session."},
		{ID: "screen_recording", Label: "Screen Capture", State: permissionmodel.StateNotRequired, Required: true, Detail: "Windows does not require a separate consent grant for capture inside the active user session."},
		{ID: "input_control", Label: "Input Control", State: permissionmodel.StateNotRequired, Required: true, Detail: "Windows does not require a separate consent grant for input inside the active user session."},
	})
}

func linuxPermissionReport(kind backendKind, tools availableTools, accessibilityAvailable, ydotoolAvailable, requested, wsl bool) permissionmodel.Report {
	platform := "linux"
	if wsl {
		platform = "wsl"
	} else if kind == backendX11 {
		platform = "linux-x11"
	} else if kind == backendWayland {
		platform = "linux-wayland"
	}
	state := func(available bool) permissionmodel.State {
		if available {
			return permissionmodel.StateGranted
		}
		return permissionmodel.StateUnavailable
	}
	session := kind != backendUnavailable
	screenshot := false
	keyboard := false
	pointer := false
	switch kind {
	case backendX11:
		screenshot = tools["import"]
		keyboard = tools["xdotool"]
		pointer = tools["xdotool"]
	case backendWayland:
		screenshot = tools["grim"] || tools["gnome-screenshot"]
		keyboard = tools["wtype"] || ydotoolAvailable
		pointer = ydotoolAvailable
	}
	pointerState := state(pointer)
	pointerDetail := "Install the platform input helper required by Executor."
	if kind == backendWayland && !pointer {
		pointerState = permissionmodel.StateManual
		pointerDetail = "Install and authorize ydotool for pointer input; compositor policy may still restrict advanced actions."
	}
	items := []permissionmodel.Item{
		{ID: "desktop_session", Label: "Active Desktop Session", State: state(session), Required: true, Detail: "A logged-in graphical session is required."},
		{ID: "screen_recording", Label: "Screen Capture", State: state(screenshot), Required: true, Detail: "Install grim or gnome-screenshot on Wayland, or ImageMagick import on X11."},
		{ID: "accessibility", Label: "Accessibility Bus", State: state(session && accessibilityAvailable), Required: true, Detail: "Install gdbus and enable the session accessibility bus."},
		{ID: "keyboard_input", Label: "Keyboard Input", State: state(keyboard), Required: true, Detail: "Install xdotool on X11, or wtype/ydotool on Wayland."},
		{ID: "pointer_input", Label: "Pointer Input", State: pointerState, Required: true, Detail: pointerDetail},
	}
	if kind == backendX11 {
		items = append(items, permissionmodel.Item{
			ID: "window_control", Label: "Window and Application Control", State: state(tools["wmctrl"]), Required: true,
			Detail: "Install wmctrl for X11 window enumeration and application activation.",
		})
	}
	return permissionmodel.NewReport(platform, requested, items)
}

func detectWSLEnvironment(env map[string]string, kernelRelease string) bool {
	if env["WSL_INTEROP"] != "" || env["WSL_DISTRO_NAME"] != "" {
		return true
	}
	release := strings.ToLower(kernelRelease)
	return strings.Contains(release, "microsoft") || strings.Contains(release, "wsl")
}

func reconcileLinuxTools(cached, fresh availableTools, names []string) (availableTools, bool) {
	effective := make(availableTools, len(names))
	changed := false
	for _, name := range names {
		if cached[name] != fresh[name] {
			changed = true
		}
		effective[name] = cached[name] && fresh[name]
	}
	return effective, changed
}

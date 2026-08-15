//go:build linux

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type backendKind string

const (
	backendX11         backendKind = "x11"
	backendWayland     backendKind = "wayland"
	backendUnavailable backendKind = "unavailable"
)

type linuxBackend struct {
	runner commandRunner
	env    map[string]string
	kind   backendKind
	tools  availableTools
}

func newLinuxBackend(runner commandRunner, env map[string]string) linuxBackend {
	return linuxBackend{
		runner: runner,
		env:    env,
		kind:   detectLinuxBackend(env),
		tools:  detectAvailableTools("grim", "gnome-screenshot", "wmctrl", "gdbus", "xdotool", "wtype", "ydotool"),
	}
}

func detectLinuxBackend(env map[string]string) backendKind {
	switch strings.ToLower(env["XDG_SESSION_TYPE"]) {
	case "wayland":
		if env["WAYLAND_DISPLAY"] != "" {
			return backendWayland
		}
	case "x11":
		if env["DISPLAY"] != "" {
			return backendX11
		}
	}
	if env["WAYLAND_DISPLAY"] != "" {
		return backendWayland
	}
	if env["DISPLAY"] != "" {
		return backendX11
	}
	return backendUnavailable
}

func (b linuxBackend) Screenshot(ctx context.Context, path string) error {
	switch b.kind {
	case backendWayland:
		command, err := chooseWaylandScreenshotCommand(b.tools, path)
		if err != nil {
			return err
		}
		if _, err := b.runner.Run(ctx, command[0], command[1:]...); err != nil {
			return wrapDesktopError("wayland screenshot unavailable", err)
		}
		return nil
	case backendX11:
		if _, err := b.runner.Run(ctx, "import", "-window", "root", path); err != nil {
			return wrapDesktopError("x11 screenshot unavailable", err)
		}
		return nil
	default:
		return &UnavailableError{Reason: "no active Linux desktop session detected"}
	}
}

func (b linuxBackend) Windows(ctx context.Context) ([]Window, error) {
	if b.kind != backendX11 {
		return nil, &UnavailableError{Reason: "window enumeration requires X11 tools"}
	}
	if !b.tools["wmctrl"] {
		return nil, &UnavailableError{Reason: "x11 window enumeration unavailable: install wmctrl"}
	}
	data, err := b.runner.Run(ctx, "wmctrl", "-lp")
	if err != nil {
		return nil, wrapDesktopError("x11 window enumeration unavailable", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	windows := make([]Window, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		windows = append(windows, Window{Title: line})
	}
	return windows, nil
}

func (b linuxBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	if b.kind == backendUnavailable {
		return AccessibilityTree{}, &UnavailableError{Reason: "no active Linux desktop session detected"}
	}
	if !b.tools["gdbus"] {
		return AccessibilityTree{}, &UnavailableError{Reason: "accessibility bus unavailable: install gdbus"}
	}
	data, err := b.runner.Run(ctx, "gdbus", "call", "--session", "--dest", "org.a11y.Bus", "--object-path", "/org/a11y/bus", "--method", "org.a11y.Bus.GetAddress")
	if err != nil {
		return AccessibilityTree{}, wrapDesktopError("accessibility bus unavailable", err)
	}
	return AccessibilityTree{
		Application: strings.TrimSpace(string(data)),
	}, nil
}

func (b linuxBackend) Mouse(ctx context.Context, action MouseAction) error {
	switch b.kind {
	case backendX11:
		if !b.tools["xdotool"] {
			return &UnavailableError{Reason: "x11 mouse input unavailable: install xdotool"}
		}
		args := []string{"mousemove", fmt.Sprintf("%d", action.X), fmt.Sprintf("%d", action.Y)}
		if action.Type == MouseActionClick {
			args = []string{"mousemove", fmt.Sprintf("%d", action.X), fmt.Sprintf("%d", action.Y), "click", "1"}
		}
		if _, err := b.runner.Run(ctx, "xdotool", args...); err != nil {
			return wrapDesktopError("x11 mouse input unavailable", err)
		}
		return nil
	case backendWayland:
		command, err := chooseWaylandMouseCommand(b.tools, action)
		if err != nil {
			return err
		}
		if _, err := b.runner.Run(ctx, command[0], command[1:]...); err != nil {
			return wrapDesktopError("wayland mouse input unavailable", err)
		}
		return nil
	default:
		return &UnavailableError{Reason: "no active Linux desktop session detected"}
	}
}

func (b linuxBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	switch b.kind {
	case backendX11:
		if !b.tools["xdotool"] {
			return &UnavailableError{Reason: "x11 keyboard input unavailable: install xdotool"}
		}
		if action.Text != "" {
			if _, err := b.runner.Run(ctx, "xdotool", "type", "--delay", "1", action.Text); err != nil {
				return wrapDesktopError("x11 keyboard input unavailable", err)
			}
			return nil
		}
		if _, err := b.runner.Run(ctx, "xdotool", "key", fmt.Sprintf("%d", action.KeyCode)); err != nil {
			return wrapDesktopError("x11 keyboard input unavailable", err)
		}
		return nil
	case backendWayland:
		command, err := chooseWaylandKeyboardCommand(b.tools, action)
		if err != nil {
			return err
		}
		if _, err := b.runner.Run(ctx, command[0], command[1:]...); err != nil {
			return wrapDesktopError("wayland keyboard input unavailable", err)
		}
		return nil
	default:
		return &UnavailableError{Reason: "no active Linux desktop session detected"}
	}
}

func (b linuxBackend) App(ctx context.Context, action AppAction) error {
	var command string
	switch action.Type {
	case AppActionActivate, AppActionLaunch:
		command = action.Name
	case AppActionQuit:
		command = "pkill"
	default:
		return fmt.Errorf("unsupported app action %q", action.Type)
	}

	switch action.Type {
	case AppActionQuit:
		if _, err := b.runner.Run(ctx, command, action.Name); err != nil {
			return wrapDesktopError("application action unavailable", err)
		}
	default:
		if _, err := b.runner.Run(ctx, command); err != nil {
			return wrapDesktopError("application action unavailable", err)
		}
	}
	return nil
}

func (b linuxBackend) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind backendKind `json:"kind"`
	}{Kind: b.kind})
}

func detectAvailableTools(names ...string) availableTools {
	tools := availableTools{}
	for _, name := range names {
		_, err := exec.LookPath(name)
		tools[name] = err == nil
	}
	return tools
}

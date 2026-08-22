//go:build linux

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type backendKind string

const (
	backendX11         backendKind = "x11"
	backendWayland     backendKind = "wayland"
	backendUnavailable backendKind = "unavailable"
)

type linuxBackend struct {
	runner        commandRunner
	env           map[string]string
	kind          backendKind
	tools         availableTools
	detectTools   func(...string) availableTools
	kernelRelease func() string
}

var linuxDesktopToolNames = []string{"import", "grim", "gnome-screenshot", "wmctrl", "gdbus", "xdotool", "wtype", "ydotool"}

func newLinuxBackend(runner commandRunner, env map[string]string) linuxBackend {
	return linuxBackend{
		runner:      runner,
		env:         env,
		kind:        detectLinuxBackend(env),
		tools:       detectAvailableTools(linuxDesktopToolNames...),
		detectTools: detectAvailableTools,
		kernelRelease: func() string {
			data, _ := os.ReadFile("/proc/sys/kernel/osrelease")
			return string(data)
		},
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
		window, err := b.parseWindowLine(ctx, line)
		if err != nil {
			return nil, err
		}
		windows = append(windows, window)
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
		commands, err := buildX11MouseCommands(action)
		if err != nil {
			return err
		}
		for index, command := range commands {
			if _, err := b.runner.Run(ctx, "xdotool", command...); err != nil {
				for _, remaining := range commands[index+1:] {
					if len(remaining) > 0 && remaining[0] == "keyup" {
						_, _ = b.runner.Run(context.WithoutCancel(ctx), "xdotool", remaining...)
					}
				}
				return wrapDesktopError("x11 mouse input unavailable", err)
			}
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
		if len(action.Keys) > 0 {
			command, err := buildX11KeypressCommand(action.Keys)
			if err != nil {
				return err
			}
			if _, err := b.runner.Run(ctx, "xdotool", command...); err != nil {
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
	switch action.Type {
	case AppActionActivate:
		if b.kind != backendX11 || !b.tools["wmctrl"] {
			return &UnavailableError{Reason: "application activation requires wmctrl on X11"}
		}
		windows, err := b.Windows(ctx)
		if err != nil {
			return err
		}
		for _, window := range windows {
			if window.App == action.Name {
				if _, err := b.runner.Run(ctx, "wmctrl", "-ia", fmt.Sprintf("0x%08x", window.ID)); err != nil {
					return wrapDesktopError("application activation unavailable", err)
				}
				return nil
			}
		}
		return &UnavailableError{Reason: "application activation unavailable: no matching window"}
	case AppActionLaunch:
		if _, err := b.runner.Run(ctx, action.Name); err != nil {
			return wrapDesktopError("application action unavailable", err)
		}
		return nil
	case AppActionQuit:
		if _, err := b.runner.Run(ctx, "pkill", action.Name); err != nil {
			return wrapDesktopError("application action unavailable", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported app action %q", action.Type)
	}
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

func (b linuxBackend) Available(ctx context.Context) bool {
	return b.kind != backendUnavailable
}

func (b linuxBackend) PermissionStatus(ctx context.Context) (permissionmodel.Report, error) {
	return b.permissionReport(ctx, false), nil
}

func (b linuxBackend) RequestPermissions(ctx context.Context) (permissionmodel.Report, error) {
	return b.permissionReport(ctx, true), nil
}

func (b linuxBackend) permissionReport(ctx context.Context, requested bool) permissionmodel.Report {
	restartRequired := false
	effectiveTools := b.tools
	if b.detectTools != nil {
		fresh := b.detectTools(linuxDesktopToolNames...)
		effectiveTools, restartRequired = reconcileLinuxTools(b.tools, fresh, linuxDesktopToolNames)
	}
	accessibilityAvailable := false
	if b.kind != backendUnavailable && effectiveTools["gdbus"] {
		_, err := b.runner.Run(ctx, "gdbus", "call", "--session", "--dest", "org.a11y.Bus", "--object-path", "/org/a11y/bus", "--method", "org.a11y.Bus.GetAddress")
		accessibilityAvailable = err == nil
	}
	ydotoolAvailable := false
	if b.kind == backendWayland && effectiveTools["ydotool"] {
		// A zero-distance relative move exercises ydotoold authorization without changing the pointer position.
		_, err := b.runner.Run(ctx, "ydotool", "mousemove", "--", "0", "0")
		ydotoolAvailable = err == nil
	}
	kernelRelease := ""
	if b.kernelRelease != nil {
		kernelRelease = b.kernelRelease()
	}
	report := linuxPermissionReport(b.kind, effectiveTools, accessibilityAvailable, ydotoolAvailable, requested, detectWSLEnvironment(b.env, kernelRelease))
	report.RestartRequired = restartRequired
	if restartRequired {
		report.Ready = false
	}
	return report
}

func (b linuxBackend) PreflightActions(actions []Action) error {
	for index, action := range actions {
		var err error
		switch action.Type {
		case ActionClick, ActionDoubleClick, ActionMove, ActionDrag, ActionScroll:
			mouseAction := MouseAction{
				Type: map[ActionKind]MouseActionType{
					ActionClick: MouseActionClick, ActionDoubleClick: MouseActionDoubleClick,
					ActionMove: MouseActionMove, ActionDrag: MouseActionDrag, ActionScroll: MouseActionScroll,
				}[action.Type],
				X: action.X, Y: action.Y, Button: action.Button, Keys: action.Keys,
				Path: action.Path, ScrollX: action.ScrollX, ScrollY: action.ScrollY,
			}
			switch b.kind {
			case backendX11:
				if !b.tools["xdotool"] {
					err = &UnavailableError{Reason: "x11 mouse input unavailable: install xdotool"}
				} else {
					_, err = buildX11MouseCommands(mouseAction)
				}
			case backendWayland:
				_, err = chooseWaylandMouseCommand(b.tools, mouseAction)
			default:
				err = &UnavailableError{Reason: "no active Linux desktop session detected"}
			}
		case ActionType, ActionKeypress:
			keyboardAction := KeyboardAction{Text: action.Text, Keys: action.Keys}
			switch b.kind {
			case backendX11:
				if !b.tools["xdotool"] {
					err = &UnavailableError{Reason: "x11 keyboard input unavailable: install xdotool"}
				} else if len(keyboardAction.Keys) > 0 {
					_, err = buildX11KeypressCommand(keyboardAction.Keys)
				}
			case backendWayland:
				_, err = chooseWaylandKeyboardCommand(b.tools, keyboardAction)
			default:
				err = &UnavailableError{Reason: "no active Linux desktop session detected"}
			}
		}
		if err != nil {
			return fmt.Errorf("action %d (%s): %w", index, action.Type, err)
		}
	}
	return nil
}

func (b linuxBackend) parseWindowLine(ctx context.Context, line string) (Window, error) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return Window{}, fmt.Errorf("unexpected wmctrl output: %q", line)
	}
	idValue, err := strconv.ParseInt(strings.TrimPrefix(fields[0], "0x"), 16, 64)
	if err != nil {
		return Window{}, fmt.Errorf("parse wmctrl window id: %w", err)
	}
	pid, err := strconv.Atoi(fields[2])
	if err != nil {
		return Window{}, fmt.Errorf("parse wmctrl pid: %w", err)
	}
	title := strings.Join(fields[4:], " ")
	app := ""
	if data, err := b.runner.Run(ctx, "cat", fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		app = strings.TrimSpace(string(data))
	}
	return Window{
		ID:    int(idValue),
		PID:   pid,
		App:   app,
		Title: title,
	}, nil
}

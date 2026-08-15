package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/terminal"
)

type Caller interface {
	Call(ctx context.Context, method string, params any, result any) error
}

type MCP struct {
	broker  Caller
	desktop Caller
}

func NewMCP(broker, desktop Caller) *MCP {
	return &MCP{broker: broker, desktop: desktop}
}

func (d *MCP) Dispatch(ctx context.Context, call mcp.ToolCall) (any, error) {
	switch call.Name {
	case "terminal":
		return d.terminal(ctx, call.Arguments)
	case "terminal_output":
		sessionID, err := requiredString(call.Arguments, "sessionId")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, d.privilegedCaller(call.Arguments), desktop.RPCMethodTerminalRead, desktop.RPCTerminalReadParams{
			SessionID: sessionID,
			Cursor:    integer(call.Arguments["cursor"]),
		})
	case "terminal_sessions":
		action, err := requiredString(call.Arguments, "action")
		if err != nil {
			return nil, err
		}
		method, ok := map[string]string{"list": "terminal.list", "inspect": "terminal.inspect"}[action]
		if !ok {
			return nil, fmt.Errorf("unsupported terminal sessions action %q", action)
		}
		if action == "inspect" {
			sessionID, err := requiredString(call.Arguments, "sessionId")
			if err != nil {
				return nil, err
			}
			listed, err := d.call(ctx, d.privilegedCaller(call.Arguments), desktop.RPCMethodTerminalList, struct{}{})
			if err != nil {
				return nil, err
			}
			for _, item := range anySlice(listed) {
				entry, _ := item.(map[string]any)
				session, _ := entry["Session"].(map[string]any)
				id, _ := session["ID"].(string)
				if id == "" {
					session, _ = entry["session"].(map[string]any)
					id, _ = session["id"].(string)
				}
				if id == sessionID {
					return entry, nil
				}
			}
			return nil, fmt.Errorf("terminal session %q not found", sessionID)
		}
		return d.call(ctx, d.privilegedCaller(call.Arguments), method, struct{}{})
	case "filesystem_read":
		return d.filesystemRead(ctx, call.Arguments)
	case "filesystem_write":
		return d.filesystemWrite(ctx, call.Arguments)
	case "desktop_observe":
		return d.desktopObserve(ctx, call.Arguments)
	case "desktop_control":
		return d.desktopControl(ctx, call.Arguments)
	case "device_status":
		return d.deviceStatus(ctx, call.Arguments)
	default:
		return nil, fmt.Errorf("unsupported Executor tool %q", call.Name)
	}
}

func (d *MCP) terminal(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"create": "terminal.start",
		"write":  "terminal.write",
		"signal": desktop.RPCMethodTerminalSignal,
		"resize": desktop.RPCMethodTerminalResize,
		"close":  "terminal.close",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported terminal action %q", action)
	}
	caller := d.privilegedCaller(arguments)
	switch action {
	case "create":
		command, _ := arguments["command"].(string)
		initial := []byte(nil)
		if command != "" {
			initial = []byte(command + "\n")
		}
		environment := map[string]string{}
		if raw, ok := arguments["environment"].(map[string]any); ok {
			for key, value := range raw {
				if text, ok := value.(string); ok {
					environment[key] = text
				}
			}
		}
		columns := int(integer(arguments["columns"]))
		rows := int(integer(arguments["rows"]))
		if columns < 0 || rows < 0 || columns > terminal.MaxDimension || rows > terminal.MaxDimension {
			return nil, fmt.Errorf("terminal dimensions must be between 1 and %d when provided", terminal.MaxDimension)
		}
		cwd, _ := arguments["cwd"].(string)
		return d.call(ctx, caller, method, desktop.RPCTerminalStartParams{
			Dir: cwd, Env: environment, InitialInput: initial,
			Columns: columns, Rows: rows,
		})
	case "write":
		sessionID, err := requiredString(arguments, "sessionId")
		if err != nil {
			return nil, err
		}
		input, _ := arguments["input"].(string)
		return d.call(ctx, caller, method, desktop.RPCTerminalWriteParams{SessionID: sessionID, Input: []byte(input)})
	case "signal":
		sessionID, err := requiredString(arguments, "sessionId")
		if err != nil {
			return nil, err
		}
		signalName, err := requiredString(arguments, "signal")
		if err != nil {
			return nil, err
		}
		signal := terminal.Signal(signalName)
		if signal != terminal.SignalInterrupt && signal != terminal.SignalTerminate && signal != terminal.SignalKill {
			return nil, fmt.Errorf("unsupported terminal signal %q", signalName)
		}
		return d.call(ctx, caller, desktop.RPCMethodTerminalSignal, desktop.RPCTerminalSignalParams{SessionID: sessionID, Signal: signal})
	case "resize":
		sessionID, err := requiredString(arguments, "sessionId")
		if err != nil {
			return nil, err
		}
		columns := int(integer(arguments["columns"]))
		rows := int(integer(arguments["rows"]))
		if columns < 1 || rows < 1 || columns > terminal.MaxDimension || rows > terminal.MaxDimension {
			return nil, fmt.Errorf("terminal resize requires columns and rows between 1 and %d", terminal.MaxDimension)
		}
		return d.call(ctx, caller, desktop.RPCMethodTerminalResize, desktop.RPCTerminalResizeParams{SessionID: sessionID, Columns: columns, Rows: rows})
	case "close":
		sessionID, err := requiredString(arguments, "sessionId")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, caller, method, desktop.RPCSessionParams{SessionID: sessionID})
	default:
		return nil, fmt.Errorf("unsupported terminal action %q", action)
	}
}

func (d *MCP) filesystemRead(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"read_file":      "filesystem.read",
		"read_directory": "filesystem.list",
		"stat":           "filesystem.stat",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem read action %q", action)
	}
	path, err := requiredString(arguments, "path")
	if err != nil {
		return nil, err
	}
	caller := d.privilegedCaller(arguments)
	if action == "read_file" {
		if caller == nil {
			return nil, errors.New("Executor helper is unavailable")
		}
		var data []byte
		if err := caller.Call(ctx, method, desktop.RPCFilesystemPathParams{Path: path}, &data); err != nil {
			return nil, err
		}
		return map[string]any{"content": string(data)}, nil
	}
	return d.call(ctx, caller, method, desktop.RPCFilesystemPathParams{Path: path})
}

func (d *MCP) filesystemWrite(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"write_file":  "filesystem.write",
		"append_file": "filesystem.append",
		"mkdir":       "filesystem.mkdir",
		"move":        "filesystem.move",
		"delete":      "filesystem.delete",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem write action %q", action)
	}
	caller := d.privilegedCaller(arguments)
	path, err := requiredString(arguments, "path")
	if err != nil {
		return nil, err
	}
	switch action {
	case "write_file":
		content, _ := arguments["content"].(string)
		return d.call(ctx, caller, method, desktop.RPCFilesystemWriteParams{Path: path, Data: []byte(content), Perm: fs.FileMode(0o644)})
	case "move":
		destination, err := requiredString(arguments, "destination")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, caller, method, desktop.RPCFilesystemMoveParams{Src: path, Dst: destination})
	case "delete":
		return d.call(ctx, caller, method, desktop.RPCFilesystemPathParams{Path: path})
	case "append_file":
		content, _ := arguments["content"].(string)
		return d.call(ctx, caller, method, desktop.RPCFilesystemWriteParams{Path: path, Data: []byte(content), Perm: fs.FileMode(0o644)})
	case "mkdir":
		return d.call(ctx, caller, method, desktop.RPCFilesystemMkdirParams{Path: path, Perm: fs.FileMode(0o755)})
	default:
		return nil, fmt.Errorf("unsupported filesystem write action %q", action)
	}
}

func (d *MCP) desktopObserve(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"screenshot":         "desktop.screenshot",
		"accessibility_tree": "desktop.accessibility",
		"windows":            "desktop.windows",
		"applications":       "desktop.applications",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported desktop observe action %q", action)
	}
	switch action {
	case "windows", "accessibility_tree":
		return d.call(ctx, d.desktop, method, struct{}{})
	case "screenshot":
		path, err := requiredString(arguments, "path")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopScreenshotParams{Path: path})
	case "applications":
		windows, err := d.call(ctx, d.desktop, desktop.RPCMethodDesktopWindows, struct{}{})
		if err != nil {
			return nil, err
		}
		items, _ := windows.([]any)
		seen := map[string]bool{}
		applications := make([]string, 0)
		for _, item := range items {
			entry, _ := item.(map[string]any)
			name, _ := entry["app"].(string)
			if name != "" && !seen[name] {
				seen[name] = true
				applications = append(applications, name)
			}
		}
		return map[string]any{"applications": applications}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop observe action %q", action)
	}
}

func (d *MCP) desktopControl(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"mouse_move":   "desktop.mouse",
		"mouse_click":  "desktop.mouse",
		"key_press":    "desktop.keyboard",
		"type_text":    "desktop.keyboard",
		"window_focus": "desktop.app",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported desktop control action %q", action)
	}
	switch action {
	case "mouse_move", "mouse_click":
		button, _ := arguments["button"].(string)
		if button == "middle" {
			button = string(desktop.MouseButtonCenter)
		}
		actionType := desktop.MouseActionMove
		if action == "mouse_click" {
			actionType = desktop.MouseActionClick
		}
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopMouseParams{Action: desktop.MouseAction{
			Type: actionType, X: int(integer(arguments["x"])), Y: int(integer(arguments["y"])), Button: desktop.MouseButton(button),
		}})
	case "type_text":
		text, err := requiredString(arguments, "text")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopKeyboardParams{Action: desktop.KeyboardAction{Text: text}})
	case "key_press":
		modifiers := stringSlice(arguments["modifiers"])
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopKeyboardParams{Action: desktop.KeyboardAction{
			KeyCode: int(integer(arguments["keyCode"])), Modifiers: modifiers,
		}})
	case "window_focus":
		name, err := requiredString(arguments, "name")
		if err != nil {
			return nil, err
		}
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopAppParams{Action: desktop.AppAction{Type: desktop.AppActionActivate, Name: name}})
	default:
		return nil, fmt.Errorf("unsupported desktop control action %q", action)
	}
}

func (d *MCP) deviceStatus(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	switch action {
	case "desktop":
		return d.call(ctx, d.desktop, desktop.RPCMethodDeviceStatus, struct{}{})
	case "terminals":
		owner, ownerErr := d.call(ctx, d.desktop, "terminal.list", map[string]any{})
		admin, adminErr := d.call(ctx, d.broker, "terminal.list", map[string]any{})
		return map[string]any{"owner": owner, "admin": admin}, errors.Join(ownerErr, adminErr)
	case "summary":
		broker, brokerErr := d.call(ctx, d.broker, desktop.RPCMethodDeviceStatus, struct{}{})
		desktop, desktopErr := d.call(ctx, d.desktop, desktop.RPCMethodDeviceStatus, struct{}{})
		return map[string]any{"broker": broker, "desktop": desktop}, errors.Join(brokerErr, desktopErr)
	default:
		return nil, fmt.Errorf("unsupported device status action %q", action)
	}
}

func (d *MCP) privilegedCaller(arguments map[string]any) Caller {
	privilege, _ := arguments["privilege"].(string)
	if privilege == "admin" {
		return d.broker
	}
	return d.desktop
}

func (d *MCP) call(ctx context.Context, caller Caller, method string, arguments any) (any, error) {
	if caller == nil {
		return nil, errors.New("Executor helper is unavailable")
	}
	var raw json.RawMessage
	if err := caller.Call(ctx, method, arguments, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{"ok": true}, nil
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func integer(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func requiredString(arguments map[string]any, name string) (string, error) {
	value, _ := arguments[name].(string)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

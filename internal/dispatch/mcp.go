package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jamie950315/executor/internal/mcp"
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
		return d.call(ctx, d.privilegedCaller(call.Arguments), "terminal.read", call.Arguments)
	case "terminal_sessions":
		action, err := requiredString(call.Arguments, "action")
		if err != nil {
			return nil, err
		}
		method, ok := map[string]string{"list": "terminal.list", "inspect": "terminal.inspect"}[action]
		if !ok {
			return nil, fmt.Errorf("unsupported terminal sessions action %q", action)
		}
		return d.call(ctx, d.privilegedCaller(call.Arguments), method, call.Arguments)
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
		"signal": "terminal.signal",
		"close":  "terminal.close",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported terminal action %q", action)
	}
	return d.call(ctx, d.privilegedCaller(arguments), method, arguments)
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
	return d.call(ctx, d.privilegedCaller(arguments), method, arguments)
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
	return d.call(ctx, d.privilegedCaller(arguments), method, arguments)
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
	return d.call(ctx, d.desktop, method, arguments)
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
	return d.call(ctx, d.desktop, method, arguments)
}

func (d *MCP) deviceStatus(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	switch action {
	case "desktop":
		return d.call(ctx, d.desktop, "device.status", arguments)
	case "terminals":
		owner, ownerErr := d.call(ctx, d.desktop, "terminal.list", map[string]any{})
		admin, adminErr := d.call(ctx, d.broker, "terminal.list", map[string]any{})
		return map[string]any{"owner": owner, "admin": admin}, errors.Join(ownerErr, adminErr)
	case "summary":
		broker, brokerErr := d.call(ctx, d.broker, "device.status", arguments)
		desktop, desktopErr := d.call(ctx, d.desktop, "device.status", arguments)
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

func (d *MCP) call(ctx context.Context, caller Caller, method string, arguments map[string]any) (any, error) {
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

func requiredString(arguments map[string]any, name string) (string, error) {
	value, _ := arguments[name].(string)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

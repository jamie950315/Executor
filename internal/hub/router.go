// Package hub implements the relay-only multi-device MCP routing boundary.
// It has no host URLs, local dispatch fallback, or retry path.
package hub

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jamie950315/executor/internal/mcp"
)

var ErrUnconfirmed = errors.New("relay response unavailable; operation outcome unconfirmed; do not automatically retry")

type Device struct {
	ID         string `json:"deviceId"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Online     bool   `json:"online"`
	Authorized bool   `json:"authorized"`
}

// Relay must authenticate its machine identity and independently enforce current
// per-device authorization at execution time. Devices is a fresh directory for
// that identity, not browser cookies or model-supplied authorization data.
// Call must bind remote sessions/captures to device and caller, and must not retry
// writes or fall back to a direct device endpoint. The production adapter is
// intentionally not implemented here yet.
type Relay interface {
	Devices(context.Context) ([]Device, error)
	Call(context.Context, string, mcp.ToolCall) (any, error)
}

type Router struct {
	relay    Relay
	mu       sync.Mutex
	sessions map[string]sessionBinding
}

func New(relay Relay) *Router {
	return &Router{relay: relay, sessions: make(map[string]sessionBinding)}
}

func Tools() []mcp.Tool {
	tools := mcp.BuiltinTools()
	for i := range tools {
		schema := tools[i].InputSchema
		properties := schema["properties"].(map[string]any)
		properties["deviceId"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact deviceId from devices_list; no implicit default or fallback."}
		required, _ := schema["required"].([]string)
		schema["required"] = append(required, "deviceId")
	}
	return append([]mcp.Tool{{Name: "devices_list", Description: "List devices registered with the Hub and their current authorization/online state.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Annotations: mcp.ToolAnnotations{ReadOnlyHint: true}}}, tools...)
}

func (r *Router) Dispatch(ctx context.Context, call mcp.ToolCall) (any, error) {
	if r.relay == nil {
		return nil, errors.New("Hub relay is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	known := false
	for _, tool := range Tools() {
		if tool.Name == call.Name {
			known = true
			break
		}
	}
	if !known {
		return nil, errors.New("unknown Hub tool")
	}
	devices, err := r.relay.Devices(ctx)
	if err != nil {
		return nil, errors.New("device directory unavailable")
	}
	seen := map[string]bool{}
	for _, device := range devices {
		if strings.TrimSpace(device.ID) == "" || seen[device.ID] {
			return nil, errors.New("invalid device directory")
		}
		seen[device.ID] = true
	}
	if call.Name == "devices_list" {
		return map[string]any{"devices": devices}, nil
	}
	id, ok := call.Arguments["deviceId"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return nil, errors.New("an explicit deviceId is required")
	}
	var target *Device
	for i := range devices {
		if devices[i].ID == id {
			target = &devices[i]
			break
		}
	}
	if target == nil {
		return nil, errors.New("unknown deviceId")
	}
	if !target.Authorized {
		return nil, errors.New("device is not authorized for this Hub")
	}
	if !target.Online {
		return nil, errors.New("device is offline; no fallback attempted")
	}
	arguments := make(map[string]any, len(call.Arguments))
	for k, v := range call.Arguments {
		if k != "deviceId" {
			arguments[k] = v
		}
	}
	call.Arguments = arguments
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if call.Name == "terminal" || call.Name == "terminal_output" || call.Name == "terminal_sessions" {
		return r.dispatchTerminal(ctx, id, call)
	}
	result, err := r.relay.Call(ctx, id, call)
	if err != nil {
		return nil, ErrUnconfirmed
	}
	return result, nil
}

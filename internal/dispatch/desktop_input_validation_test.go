package dispatch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestLegacyDesktopControlRejectsMalformedInputBeforeIPC(t *testing.T) {
	for _, input := range []struct {
		name string
		args map[string]any
	}{
		{"missing_coordinate", map[string]any{"action": "mouse_click", "y": 20}},
		{"string_coordinate", map[string]any{"action": "mouse_click", "x": "10", "y": 20}},
		{"fractional_coordinate", map[string]any{"action": "mouse_move", "x": 10.5, "y": 20}},
		{"overflow_coordinate", map[string]any{"action": "mouse_move", "x": 1e30, "y": 20}},
		{"null_coordinate", map[string]any{"action": "mouse_click", "x": nil, "y": 20}},
		{"invalid_button_type", map[string]any{"action": "mouse_click", "x": 10, "y": 20, "button": true}},
		{"missing_key_code", map[string]any{"action": "key_press"}},
		{"fractional_key_code", map[string]any{"action": "key_press", "keyCode": 12.5}},
		{"invalid_modifier_type", map[string]any{"action": "key_press", "keyCode": 12, "modifiers": "shift"}},
		{"invalid_modifier_entry", map[string]any{"action": "key_press", "keyCode": 12, "modifiers": []any{"shift", true}}},
	} {
		t.Run(input.name, func(t *testing.T) {
			caller := &recordingCaller{responses: map[string]any{"desktop.mouse": map[string]any{"ok": true}, "desktop.keyboard": map[string]any{"ok": true}}}
			dispatcher := NewMCP(nil, caller)
			if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_control", Arguments: input.args}); err == nil {
				t.Error("malformed desktop control was accepted")
			}
			if len(caller.calls) != 0 {
				t.Error("malformed desktop control reached helper IPC")
			}
			if dispatcher.desktopGeneration != 0 {
				t.Error("rejected input invalidated existing captures")
			}
		})
	}
}

func TestLegacyDesktopControlPreservesValidIntegerAndModifierInput(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{"desktop.mouse": map[string]any{"ok": true}, "desktop.keyboard": map[string]any{"ok": true}}}
	dispatcher := NewMCP(nil, caller)
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_control", Arguments: map[string]any{
		"action": "mouse_move", "x": json.Number("0"), "y": float64(20),
	}}); err != nil {
		t.Fatal(err)
	}
	if caller.calls[0].params["action"].(map[string]any)["y"] != float64(20) {
		t.Fatal("valid coordinate changed")
	}
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_control", Arguments: map[string]any{
		"action": "key_press", "keyCode": int64(12), "modifiers": []string{"shift"},
	}}); err != nil {
		t.Fatal(err)
	}
	modifiers, _ := caller.calls[1].params["action"].(map[string]any)["modifiers"].([]any)
	if len(modifiers) != 1 || modifiers[0] != "shift" {
		t.Fatal("valid modifier was dropped")
	}
	// macOS uses native key code 0 for A; distinguish explicit zero from missing input.
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_control", Arguments: map[string]any{
		"action": "key_press", "keyCode": 0,
	}}); err != nil {
		t.Fatalf("explicit native key code zero was rejected: %v", err)
	}
}

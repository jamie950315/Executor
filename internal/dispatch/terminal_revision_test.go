package dispatch

import (
	"context"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestTerminalRevisionDispatchPipeOptionsAndStreams(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{
		"terminal.start":        map[string]any{"ID": "pipe", "tty": false},
		"terminal.read":         map[string]any{"Data": "", "stream": "stderr"},
		"terminal.close-stdin":  nil,
		"terminal.capabilities": map[string]any{"terminalResize": true},
	}}
	d := NewMCP(caller, caller)
	if _, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "create", "argv": []any{"echo", "test"}, "tty": false}}); err != nil {
		t.Fatal(err)
	}
	if caller.calls[0].params["tty"] != false {
		t.Errorf("tty=false was dropped: %#v", caller.calls[0].params)
	}
	if _, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{"sessionId": "pipe", "stream": "stderr", "limit": 3}}); err != nil {
		t.Fatal(err)
	}
	if caller.calls[1].params["stream"] != "stderr" {
		t.Errorf("stream was dropped: %#v", caller.calls[1].params)
	}
	if _, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "close_stdin", "sessionId": "pipe"}}); err != nil {
		t.Error(err)
	}
	if _, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_sessions", Arguments: map[string]any{"action": "capabilities"}}); err != nil {
		t.Error(err)
	}
}

func TestTerminalRevisionDispatchRejectsMalformedOptions(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "create", "argv": []any{"echo"}, "tty": "false"},
		{"action": "create", "argv": []any{"echo"}, "tty": nil},
		{"action": "create", "tty": false},
		{"action": "create", "command": "echo test", "tty": false},
		{"action": "create", "argv": []any{"echo"}, "tty": false, "columns": 10},
		{"action": "write", "sessionId": "test", "input": "hello", "tty": false},
	} {
		caller := &recordingCaller{responses: map[string]any{"terminal.start": map[string]any{}, "terminal.write": nil}}
		_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: args})
		if err == nil || len(caller.calls) != 0 {
			t.Errorf("invalid options reached IPC: %#v err=%v", args, err)
		}
	}
	for _, stream := range []any{nil, false, 1, "", "bad"} {
		caller := &recordingCaller{responses: map[string]any{"terminal.read": map[string]any{}}}
		_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{"sessionId": "test", "stream": stream}})
		if err == nil || len(caller.calls) != 0 {
			t.Errorf("invalid stream reached IPC: %#v err=%v", stream, err)
		}
	}
}

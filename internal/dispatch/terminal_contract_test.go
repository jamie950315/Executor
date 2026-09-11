package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
)

func TestTerminalOutputContractPassesByteLimitBeforeReading(t *testing.T) {
	for _, privilege := range []string{"owner", "admin"} {
		for _, limit := range []int{32, 65536, 1048576} {
			caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodTerminalRead: map[string]any{"Data": ""}}}
			args := map[string]any{"sessionId": "test-session", "cursor": 8, "privilege": privilege}
			if limit != 65536 {
				args["limit"] = limit
			}
			_, err := NewMCP(caller, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			params := caller.calls[0].params
			if params["limit"] != float64(limit) || params["cursor"] != float64(8) {
				t.Fatalf("%s output parameters = %#v; want limit=%d bytes", privilege, params, limit)
			}
		}
	}
}

func TestTerminalOutputContractRejectsInvalidRangesBeforeIPC(t *testing.T) {
	for _, test := range []struct {
		key   string
		value any
	}{
		{"limit", 0}, {"limit", -1}, {"limit", 1048577}, {"limit", 1.5},
		{"limit", "32"}, {"limit", nil}, {"limit", true},
		{"cursor", -1}, {"cursor", 0.5}, {"cursor", "0"}, {"cursor", nil},
		{"cursor", math.Inf(1)}, {"cursor", math.NaN()}, {"cursor", float64(1 << 63)},
		{"privilege", "root"}, {"privilege", false},
	} {
		caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodTerminalRead: map[string]any{}}}
		args := map[string]any{"sessionId": "test-session", test.key: test.value}
		if _, err := NewMCP(caller, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: args}); err == nil || len(caller.calls) != 0 {
			t.Errorf("invalid %s=%#v reached IPC: calls=%d err=%v", test.key, test.value, len(caller.calls), err)
		}
	}
}

func TestTerminalContractCreateArgvAndAcknowledgement(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{
		desktop.RPCMethodTerminalStart: map[string]any{"ID": "command-session", "mode": "command"},
		desktop.RPCMethodTerminalWrite: nil,
	}}
	d := NewMCP(nil, caller)
	argv := []any{"/bin/sh", "-c", "printf '%s' 'hello world'; exit 7"}
	if _, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "create", "argv": argv}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caller.calls[0].params["command"], argv) || caller.calls[0].params["initial_input"] != nil {
		t.Fatalf("argv must reach actual process arguments: %#v", caller.calls[0].params)
	}
	ack, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "write", "sessionId": "command-session", "input": "hello\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ack, map[string]any{"ok": true, "inputAccepted": true, "completion": "unknown"}) {
		t.Fatalf("write acknowledgement claimed or omitted command status: %#v", ack)
	}
}

func TestTerminalContractRejectsInvalidCommandArgumentsBeforeIPC(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "create", "argv": "echo hi"},
		{"action": "create", "argv": []any{}},
		{"action": "create", "argv": []any{""}},
		{"action": "create", "argv": []any{"echo", 5}},
		{"action": "create", "argv": []any{"echo", "a\x00b"}},
		{"action": "create", "argv": []any{"echo"}, "command": "pwd"},
		{"action": "create", "command": []any{"echo"}},
		{"action": "create", "environment": map[string]any{"TEST": 3}},
		{"action": "create", "columns": 3.5},
		{"action": "create", "cwd": 1},
		{"action": "write", "sessionId": "test-session", "input": false},
		{"action": "write", "sessionId": "test-session", "argv": []any{"echo"}},
	} {
		caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodTerminalStart: map[string]any{}, desktop.RPCMethodTerminalWrite: nil}}
		if _, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: args}); err == nil || len(caller.calls) != 0 {
			t.Errorf("invalid arguments reached IPC: %#v calls=%d err=%v", args, len(caller.calls), err)
		}
	}
}

func TestTerminalContractPreservesDeniedWriteWithoutRetryOrAck(t *testing.T) {
	denied := errors.New("policy denied")
	caller := &recordingCaller{err: denied}
	result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "write", "sessionId": "test-session", "input": "hello\n"}})
	if !errors.Is(err, denied) || result != nil || len(caller.calls) != 1 {
		t.Fatalf("denied write: result=%#v err=%v calls=%d", result, err, len(caller.calls))
	}
}

func TestTerminalOutputContractAcceptsJSONNumbers(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodTerminalRead: map[string]any{}}}
	_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{"sessionId": "s", "cursor": json.Number("7"), "limit": json.Number("32")}})
	if err != nil || caller.calls[0].params["cursor"] != float64(7) || caller.calls[0].params["limit"] != float64(32) {
		t.Fatalf("JSON integer parameters = %#v, err=%v", caller.calls, err)
	}
}

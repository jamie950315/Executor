package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestMCPRoutesOwnerAndAdminTerminalToSeparateHelpers(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{responses: map[string]any{"terminal.start": map[string]any{"id": "admin-1"}}}
	desktop := &recordingCaller{responses: map[string]any{"terminal.start": map[string]any{"id": "owner-1"}}}
	dispatcher := NewMCP(broker, desktop)

	owner, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "create", "privilege": "owner", "cwd": "/tmp", "command": "pwd",
	}})
	if err != nil {
		t.Fatalf("owner terminal: %v", err)
	}
	if owner.(map[string]any)["id"] != "owner-1" || len(desktop.calls) != 1 || len(broker.calls) != 0 {
		t.Fatalf("owner routing mismatch: owner=%#v desktop=%#v broker=%#v", owner, desktop.calls, broker.calls)
	}

	admin, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "create", "privilege": "admin", "command": "whoami",
	}})
	if err != nil {
		t.Fatalf("admin terminal: %v", err)
	}
	if admin.(map[string]any)["id"] != "admin-1" || len(broker.calls) != 1 {
		t.Fatalf("admin routing mismatch: admin=%#v broker=%#v", admin, broker.calls)
	}
}

func TestMCPRoutesFilesystemAndDesktopActions(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{responses: map[string]any{"filesystem.read": []byte("root")}}
	desktop := &recordingCaller{responses: map[string]any{
		"filesystem.read": []byte("owner"),
		"desktop.windows": []map[string]any{{"app": "Finder", "title": "Desktop"}},
	}}
	dispatcher := NewMCP(broker, desktop)

	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
		"action": "read_file", "path": "/etc/hosts", "privilege": "admin",
	}}); err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if got := broker.calls[0].method; got != "filesystem.read" {
		t.Fatalf("admin method = %q", got)
	}

	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_observe", Arguments: map[string]any{
		"action": "windows",
	}}); err != nil {
		t.Fatalf("desktop windows: %v", err)
	}
	if got := desktop.calls[0].method; got != "desktop.windows" {
		t.Fatalf("desktop method = %q", got)
	}
}

func TestMCPRejectsUnknownActionsBeforeIPC(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{}
	desktop := &recordingCaller{}
	dispatcher := NewMCP(broker, desktop)
	_, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
		"action": "format_disk", "path": "/dev/disk0",
	}})
	if err == nil {
		t.Fatal("unknown action succeeded")
	}
	if len(broker.calls)+len(desktop.calls) != 0 {
		t.Fatalf("unknown action reached IPC: broker=%#v desktop=%#v", broker.calls, desktop.calls)
	}
}

type recordingCaller struct {
	calls     []recordedCall
	responses map[string]any
	err       error
}

type recordedCall struct {
	method string
	params map[string]any
}

func (c *recordingCaller) Call(_ context.Context, method string, params any, result any) error {
	encoded, _ := json.Marshal(params)
	decoded := map[string]any{}
	_ = json.Unmarshal(encoded, &decoded)
	c.calls = append(c.calls, recordedCall{method: method, params: decoded})
	if c.err != nil {
		return c.err
	}
	response, ok := c.responses[method]
	if !ok {
		return errors.New("missing fake response")
	}
	encoded, _ = json.Marshal(response)
	return json.Unmarshal(encoded, result)
}

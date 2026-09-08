package dispatch

import (
	"context"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestFileWritesRejectMissingOrNonStringContentBeforeIPC(t *testing.T) {
	for _, action := range []string{"write_file", "append_file"} {
		for _, value := range []any{nil, false, 12, []any{"text"}} {
			caller := &recordingCaller{}
			arguments := map[string]any{"action": action, "path": "/unused"}
			if value != nil {
				arguments["content"] = value
			}
			_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: arguments})
			if err == nil || len(caller.calls) != 0 {
				t.Errorf("%s content %T: error=%v calls=%d", action, value, err, len(caller.calls))
			}
		}
	}
}

func TestFileWriteAllowsExplicitEmptyContent(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{"filesystem.write": map[string]any{"ok": true}}}
	_, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
		"action": "write_file", "path": "/unused", "content": "",
	}})
	if err != nil || len(caller.calls) != 1 {
		t.Fatalf("explicit empty file: %v, calls=%d", err, len(caller.calls))
	}
}

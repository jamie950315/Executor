package dispatch

import (
	"context"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestMutationContractRejectsInvalidPrivilegeAndRecursiveBeforeIPC(t *testing.T) {
	for _, name := range []string{"filesystem_read", "filesystem_write", "terminal", "terminal_output", "terminal_sessions"} {
		for _, privilege := range []any{nil, false, "root", "", 0} {
			caller := &recordingCaller{}
			_, err := NewMCP(caller, caller).Dispatch(context.Background(), mcp.ToolCall{Name: name, Arguments: map[string]any{
				"action": "delete", "path": "/fixture", "privilege": privilege, "sessionId": "fixture",
			}})
			if err == nil || len(caller.calls) != 0 {
				t.Errorf("%s invalid privilege %v reached IPC: error=%v calls=%d", name, privilege, err, len(caller.calls))
			}
		}
	}
	for _, action := range []string{"delete", "mkdir"} {
		for _, recursive := range []any{nil, "false", 0, []any{}} {
			caller := &recordingCaller{}
			_, err := NewMCP(caller, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
				"action": action, "path": "/fixture", "recursive": recursive,
			}})
			if err == nil || len(caller.calls) != 0 {
				t.Errorf("%s invalid recursive %v reached IPC: error=%v calls=%d", action, recursive, err, len(caller.calls))
			}
		}
	}
}

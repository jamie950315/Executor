package dispatch

import (
	"context"
	"github.com/jamie950315/executor/internal/mcp"
	"testing"
)

func TestLiveDesktopUsesTrustedSessionScope(t *testing.T) {
	caller := &recordingCaller{responses: map[string]any{"desktop.live": map[string]any{"supported": true}}}
	d := NewMCP(nil, caller)
	_, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_live", SessionID: "browser-one", Arguments: map[string]any{"action": "status", "owner": "spoofed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].params["owner"] != "session\x00browser-one" {
		t.Fatal("live request trusted client-supplied scope")
	}
}

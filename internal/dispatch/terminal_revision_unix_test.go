//go:build darwin || linux

package dispatch

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/broker"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/terminal"
)

func revisionHelper(t *testing.T, privilege string) (*MCP, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "executor-revision-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	manager := terminal.NewManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	key := []byte("0123456789abcdef0123456789abcdef")
	endpoint := filepath.Join(dir, "helper.sock")
	var server *ipc.RPCServer
	if privilege == "admin" {
		server = broker.NewAdminRPCServer(endpoint, key, manager, filesystem.NewLocalService())
	} else {
		server = desktop.NewHelperRPCServer(endpoint, key, manager, filesystem.NewLocalService(), unavailableDesktop{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("helper shutdown timeout")
		}
	})
	waitForDispatchSocket(t, endpoint)
	caller := ipc.NewRPCClient(endpoint, key)
	return NewMCP(caller, caller), dir
}

func revisionTool(t *testing.T, d *MCP, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result.(map[string]any)
}

func revisionOutput(t *testing.T, d *MCP, id, privilege, stream string) (string, map[string]any) {
	t.Helper()
	var data []byte
	cursor := float64(0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value := revisionTool(t, d, "terminal_output", map[string]any{"sessionId": id, "privilege": privilege, "stream": stream, "cursor": cursor, "limit": 3})
		bytes, err := base64.StdEncoding.DecodeString(value["Data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes) > 3 || value["NextCursor"] != cursor+float64(len(bytes)) {
			t.Fatal("invalid stream cursor or byte bound")
		}
		data = append(data, bytes...)
		cursor = value["NextCursor"].(float64)
		if value["sessionRunning"] == false && value["hasMore"] == false {
			return string(data), value
		}
		if len(bytes) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("session did not complete")
	return "", nil
}

func TestTerminalRevisionAuthenticatedPipesAndCWD(t *testing.T) {
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege, func(t *testing.T) {
			d, dir := revisionHelper(t, privilege)
			cap := revisionTool(t, d, "terminal_sessions", map[string]any{"action": "capabilities", "privilege": privilege})
			if cap["nonInteractive"] != true || cap["terminalResize"] != true {
				t.Fatalf("wrong helper capabilities: %#v", cap)
			}
			created := revisionTool(t, d, "terminal", map[string]any{"action": "create", "argv": []any{"/bin/sh", "-c", "cat; printf 'STDERR_ONLY' >&2; exit 7"}, "tty": false, "cwd": dir, "privilege": privilege})
			if created["tty"] != false {
				t.Fatal("IPC dropped tty=false")
			}
			id := created["ID"].(string)
			content := "中文\x00literal\x03\n"
			revisionTool(t, d, "terminal", map[string]any{"action": "write", "sessionId": id, "privilege": privilege, "input": content})
			revisionTool(t, d, "terminal", map[string]any{"action": "close_stdin", "sessionId": id, "privilege": privilege})
			stdout, meta := revisionOutput(t, d, id, privilege, "stdout")
			stderr, _ := revisionOutput(t, d, id, privilege, "stderr")
			if stdout != content || stderr != "STDERR_ONLY" || meta["exitCode"] != float64(7) {
				t.Fatalf("stream/exit mismatch: stdout=%q stderr=%q exit=%v", stdout, stderr, meta["exitCode"])
			}
			revisionTool(t, d, "terminal", map[string]any{"action": "close", "sessionId": id, "privilege": privilege})
			_, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{"action": "create", "argv": []any{"/usr/bin/true"}, "cwd": filepath.Join(dir, "missing"), "privilege": privilege}})
			if err == nil || !strings.Contains(err.Error(), "CWD_NOT_FOUND") {
				t.Fatalf("wrong IPC cwd error: %v", err)
			}
		})
	}
}

func TestTerminalRevisionAuthenticatedNativeResize(t *testing.T) {
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege, func(t *testing.T) {
			d, dir := revisionHelper(t, privilege)
			created := revisionTool(t, d, "terminal", map[string]any{"action": "create", "argv": []any{"/bin/sh", "-c", "read line; stty size"}, "columns": 91, "rows": 37, "cwd": dir, "privilege": privilege})
			id := created["ID"].(string)
			if created["tty"] != true {
				t.Fatal("default TTY changed")
			}
			revisionTool(t, d, "terminal", map[string]any{"action": "resize", "sessionId": id, "columns": 132, "rows": 43, "privilege": privilege})
			revisionTool(t, d, "terminal", map[string]any{"action": "write", "sessionId": id, "input": "\n", "privilege": privilege})
			text, meta := revisionOutput(t, d, id, privilege, "combined")
			if !strings.Contains(text, "43 132") || meta["exitCode"] != float64(0) {
				t.Fatalf("resize not visible: %q", text)
			}
			_, err := d.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{"sessionId": id, "stream": "stderr", "privilege": privilege}})
			if err == nil || !strings.Contains(err.Error(), "STREAM_UNAVAILABLE") {
				t.Fatalf("PTY advertised separate streams: %v", err)
			}
			revisionTool(t, d, "terminal", map[string]any{"action": "close", "sessionId": id, "privilege": privilege})
		})
	}
}

//go:build darwin || linux

package dispatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/broker"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestTerminalRangeThroughAuthenticatedHelpers(t *testing.T) {
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "executor-terminal-page-")
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
					t.Error("helper shutdown timed out")
				}
			})
			waitForDispatchSocket(t, endpoint)
			caller := ipc.NewRPCClient(endpoint, key)
			d := NewMCP(caller, caller)
			created, err := d.Dispatch(ctx, mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
				"action": "create", "privilege": privilege, "cwd": dir,
				"argv": []any{"/bin/sh", "-c", "printf '%04096d' 0; exit 7"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			id := created.(map[string]any)["ID"].(string)
			deadline := time.Now().Add(5 * time.Second)
			for {
				listed := manager.List()
				if len(listed) == 1 && !listed[0].Running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fixture process did not finish")
				}
				time.Sleep(10 * time.Millisecond)
			}
			var got []byte
			cursor := float64(0)
			for page := 0; ; page++ {
				result, err := d.Dispatch(ctx, mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{
					"sessionId": id, "privilege": privilege, "cursor": cursor, "limit": 32,
				}})
				if err != nil {
					t.Fatal(err)
				}
				chunk := result.(map[string]any)
				data, err := base64.StdEncoding.DecodeString(chunk["Data"].(string))
				if err != nil {
					t.Fatal(err)
				}
				if len(data) > 32 || chunk["NextCursor"] != cursor+float64(len(data)) || chunk["returnedBytes"] != float64(len(data)) {
					t.Fatalf("IPC ignored requested 32-byte bound or cursor: returned=%d next=%v", len(data), chunk["NextCursor"])
				}
				if chunk["Truncated"] != false || chunk["sessionRunning"] != false || chunk["commandRunning"] != false || chunk["exitCode"] != float64(7) {
					t.Fatalf("incorrect completed-process metadata: running=%v command=%v exit=%v", chunk["sessionRunning"], chunk["commandRunning"], chunk["exitCode"])
				}
				got = append(got, data...)
				cursor = chunk["NextCursor"].(float64)
				if chunk["hasMore"] == false {
					break
				}
				if len(data) == 0 || page > 128 {
					t.Fatal("pagination did not advance")
				}
			}
			if !bytes.Equal(got, bytes.Repeat([]byte{'0'}, 4096)) {
				t.Fatalf("reassembled output length=%d", len(got))
			}
			for _, args := range []desktop.RPCTerminalReadParams{
				{SessionID: id, Cursor: -1, Limit: 32}, {SessionID: id, Limit: -1}, {SessionID: id, Limit: 1048577},
			} {
				var chunk terminal.OutputChunk
				if err := caller.Call(ctx, desktop.RPCMethodTerminalRead, args, &chunk); err == nil {
					t.Fatalf("invalid IPC bounds accepted: cursor=%d limit=%d", args.Cursor, args.Limit)
				}
			}
			t.Logf("%s: reassembled 4096 bytes from 128 bounded pages; process exit 7 and invalid IPC ranges verified", privilege)
		})
	}
}

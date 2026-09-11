//go:build darwin || linux

package dispatch

import (
	"context"
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

func TestFilesystemRangeContractThroughAuthenticatedOwnerAndAdminHelpers(t *testing.T) {
	for _, privilege := range []string{"owner", "admin"} {
		t.Run(privilege, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "executor-range-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			key := []byte("0123456789abcdef0123456789abcdef")
			endpoint := filepath.Join(dir, "helper.sock")
			manager := terminal.NewManager()
			t.Cleanup(func() { _ = manager.KillAll() })
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
						t.Errorf("helper shutdown: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Error("helper shutdown timed out")
				}
			})
			waitForDispatchSocket(t, endpoint)
			caller := ipc.NewRPCClient(endpoint, key)
			d := NewMCP(caller, caller)
			path := filepath.Join(dir, "fixture.txt")
			content := strings.Repeat("0123456789abcdef", 8192)
			for _, write := range []struct{ action, content string }{{"write_file", content}, {"append_file", "tail"}} {
				result, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
					"action": write.action, "path": path, "content": write.content, "privilege": privilege,
				}})
				if err != nil {
					t.Fatal(err)
				}
				metadata := result.(map[string]any)
				if metadata["ok"] != true || metadata["writtenBytes"] != len(write.content) || metadata["encoding"] != "utf8" {
					t.Fatalf("write metadata = %#v", result)
				}
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != content+"tail" {
				t.Fatalf("actual file data mismatch: bytes=%d error=%v", len(actual), err)
			}
			for _, test := range []struct {
				offset    int
				limit     int
				want      string
				truncated bool
			}{
				{8, 10, "89abcdef01", true},
				{len(content), 4, "tail", false},
				{len(actual), 4, "", false},
				{len(actual) + 10, 4, "", false},
			} {
				result, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
					"action": "read_file", "path": path, "offset": test.offset, "limit": test.limit, "privilege": privilege,
				}})
				if err != nil {
					t.Fatal(err)
				}
				metadata := result.(map[string]any)
				if metadata["content"] != test.want || metadata["returnedBytes"] != len(test.want) || metadata["nextOffsetBytes"] != int64(test.offset+len(test.want)) || metadata["truncated"] != test.truncated || metadata["eof"] != !test.truncated {
					t.Fatalf("offset=%d limit=%d result=%#v", test.offset, test.limit, metadata)
				}
			}
			result, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
				"action": "read_file", "path": path, "privilege": privilege,
			}})
			if err != nil || result.(map[string]any)["returnedBytes"] != 65536 || result.(map[string]any)["truncated"] != true {
				t.Fatalf("default bounded read: %v", err)
			}
			var legacy []byte
			if err := caller.Call(ctx, desktop.RPCMethodFilesystemRead, desktop.RPCFilesystemPathParams{Path: path}, &legacy); err != nil || string(legacy) != string(actual) {
				t.Fatalf("legacy ReadFile changed: bytes=%d error=%v", len(legacy), err)
			}
			t.Logf("%s: authenticated write+append verified %d actual bytes; 10-byte slice, tail, EOF, past-EOF, default 65536-byte cap and legacy read verified", privilege, len(actual))
			nested := filepath.Join(dir, "delete-fixture")
			if err := os.Mkdir(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(nested, "sentinel.txt")
			if err := os.WriteFile(sentinel, []byte("preserved"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, deleteErr := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
				"action": "delete", "path": nested, "privilege": privilege, "recursive": false,
			}})
			preserved, readErr := os.ReadFile(sentinel)
			if deleteErr == nil || readErr != nil || string(preserved) != "preserved" {
				t.Fatalf("recursive=false removed a nonempty fixture: delete=%v read=%v", deleteErr, readErr)
			}
			if _, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
				"action": "delete", "path": nested, "privilege": privilege, "recursive": true,
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(nested); !os.IsNotExist(err) {
				t.Fatalf("recursive=true failed to remove fixture: %v", err)
			}
			newDirectory := filepath.Join(dir, "missing-parent", "child")
			if _, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
				"action": "mkdir", "path": newDirectory, "privilege": privilege, "recursive": false,
			}}); err == nil {
				t.Fatal("recursive=false unexpectedly created missing parents")
			}
			if _, err := d.Dispatch(ctx, mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
				"action": "mkdir", "path": newDirectory, "privilege": privilege, "recursive": true,
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(newDirectory); err != nil {
				t.Fatal(err)
			}
		})
	}
}

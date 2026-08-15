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

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestMCPDispatcherRunsPersistentOwnerTerminalThroughAuthenticatedHelper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir, err := os.MkdirTemp("/tmp", "executor-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	endpoint := filepath.Join(dir, "desktop.sock")
	manager := terminal.NewManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	server := desktop.NewHelperRPCServer(endpoint, key, manager, filesystem.NewLocalService(), unavailableDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForDispatchSocket(t, endpoint)

	dispatcher := NewMCP(nil, ipc.NewRPCClient(endpoint, key))
	created, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "create", "privilege": "owner", "cwd": dir, "command": "echo EXECUTOR_READY",
	}})
	if err != nil {
		t.Fatalf("create terminal through helper: %v", err)
	}
	sessionID, _ := created.(map[string]any)["ID"].(string)
	if sessionID == "" {
		sessionID, _ = created.(map[string]any)["id"].(string)
	}
	if sessionID == "" {
		t.Fatalf("missing session ID: %#v", created)
	}

	deadline := time.Now().Add(3 * time.Second)
	cursor := float64(0)
	for time.Now().Before(deadline) {
		output, readErr := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", Arguments: map[string]any{
			"sessionId": sessionID, "cursor": cursor, "privilege": "owner",
		}})
		if readErr != nil {
			t.Fatalf("read terminal through helper: %v", readErr)
		}
		encoded := output.(map[string]any)["Data"]
		if encoded == nil {
			encoded = output.(map[string]any)["data"]
		}
		if strings.Contains(stringValue(encoded), "EXECUTOR_READY") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("terminal output did not contain initial command result")
}

func TestMCPDispatcherReadsAndWritesOwnerFilesystemThroughHelper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir, err := os.MkdirTemp("/tmp", "executor-files-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	endpoint := filepath.Join(dir, "desktop.sock")
	server := desktop.NewHelperRPCServer(endpoint, key, terminal.NewManager(), filesystem.NewLocalService(), unavailableDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForDispatchSocket(t, endpoint)
	dispatcher := NewMCP(nil, ipc.NewRPCClient(endpoint, key))

	path := filepath.Join(dir, "note.txt")
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
		"action": "write_file", "privilege": "owner", "path": path, "content": "hello",
	}}); err != nil {
		t.Fatalf("write through helper: %v", err)
	}
	result, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
		"action": "read_file", "privilege": "owner", "path": path,
	}})
	if err != nil {
		t.Fatalf("read through helper: %v", err)
	}
	if got := result.(map[string]any)["content"]; got != "hello" {
		t.Fatalf("read content = %#v, result=%#v", got, result)
	}
}

func TestMCPDispatcherObservesDesktopThroughHelper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir, err := os.MkdirTemp("/tmp", "executor-desktop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	endpoint := filepath.Join(dir, "desktop.sock")
	server := desktop.NewHelperRPCServer(endpoint, key, terminal.NewManager(), filesystem.NewLocalService(), unavailableDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitForDispatchSocket(t, endpoint)

	result, err := NewMCP(nil, ipc.NewRPCClient(endpoint, key)).Dispatch(context.Background(), mcp.ToolCall{
		Name: "desktop_observe", Arguments: map[string]any{"action": "windows"},
	})
	if err != nil {
		t.Fatalf("windows through helper: %v", err)
	}
	windows := result.([]any)
	if len(windows) != 1 || windows[0].(map[string]any)["app"] != "Finder" {
		t.Fatalf("unexpected windows: %#v", result)
	}
}

func waitForDispatchSocket(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(endpoint); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket did not appear: %s", endpoint)
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		decoded, err := base64.StdEncoding.DecodeString(typed)
		if err == nil {
			return string(decoded)
		}
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

type unavailableDesktop struct{}

func (unavailableDesktop) Available(context.Context) bool           { return true }
func (unavailableDesktop) Screenshot(context.Context, string) error { return nil }
func (unavailableDesktop) Windows(context.Context) ([]desktop.Window, error) {
	return []desktop.Window{{App: "Finder", Title: "Desktop"}}, nil
}
func (unavailableDesktop) Accessibility(context.Context) (desktop.AccessibilityTree, error) {
	return desktop.AccessibilityTree{}, nil
}
func (unavailableDesktop) Mouse(context.Context, desktop.MouseAction) error       { return nil }
func (unavailableDesktop) Keyboard(context.Context, desktop.KeyboardAction) error { return nil }
func (unavailableDesktop) App(context.Context, desktop.AppAction) error           { return nil }

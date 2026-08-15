//go:build darwin || linux

package desktop

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestHelperRPCServer_RejectsUnknownMethodAndUnknownFields(t *testing.T) {
	t.Parallel()

	endpoint := helperSocketPath(t)
	key := []byte("fedcba9876543210fedcba9876543210")
	server := NewHelperRPCServer(endpoint, key, &helperTerminal{}, &helperFilesystem{}, &helperDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), "nope.method", struct{}{}, &struct{}{}); err == nil {
		t.Fatal("unknown method unexpectedly succeeded")
	}
	if err := client.Call(context.Background(), RPCMethodTerminalWrite, map[string]any{
		"session_id": "owner",
		"input":      []byte("x"),
		"extra":      true,
	}, &struct{}{}); err == nil {
		t.Fatal("unknown field unexpectedly succeeded")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

func TestStrictRPCParamsRejectTrailingJSONValues(t *testing.T) {
	t.Parallel()
	if err := decodeStrictParams("device.test", []byte(`{} {"extra":true}`), &struct{}{}); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

type helperTerminal struct{}

func (helperTerminal) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	return terminal.Session{ID: "owner"}, nil
}
func (helperTerminal) Write(sessionID string, input []byte) error { return nil }
func (helperTerminal) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	return terminal.OutputChunk{}, nil
}
func (helperTerminal) List() []terminal.SessionInfo { return nil }
func (helperTerminal) Close(sessionID string) error { return nil }
func (helperTerminal) Kill(sessionID string) error  { return nil }
func (helperTerminal) KillAll() error               { return nil }

type helperFilesystem struct{}

func (helperFilesystem) ReadFile(path string) ([]byte, error) { return nil, nil }
func (helperFilesystem) List(path string) ([]filesystem.Entry, error) {
	return nil, nil
}
func (helperFilesystem) Glob(pattern string) ([]string, error) { return nil, nil }
func (helperFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return filesystem.FileInfo{}, nil
}
func (helperFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error { return nil }
func (helperFilesystem) Move(src, dst string) error                                 { return nil }
func (helperFilesystem) Delete(path string) error                                   { return nil }

type helperDesktop struct{}

func (helperDesktop) Screenshot(ctx context.Context, path string) error { return nil }
func (helperDesktop) Windows(ctx context.Context) ([]Window, error)     { return nil, nil }
func (helperDesktop) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (helperDesktop) Mouse(ctx context.Context, action MouseAction) error       { return nil }
func (helperDesktop) Keyboard(ctx context.Context, action KeyboardAction) error { return nil }
func (helperDesktop) App(ctx context.Context, action AppAction) error           { return nil }

func waitForHelperEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(endpoint); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint did not appear: %s", endpoint)
}

func helperSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "executor-helper-rpc-")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

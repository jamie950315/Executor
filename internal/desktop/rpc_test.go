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

func TestHelperRPCServer_DeviceStatusAndNewMethods(t *testing.T) {
	t.Parallel()

	endpoint := helperSocketPath(t)
	key := []byte("00112233445566778899aabbccddeeff")
	term := &helperStatusTerminal{}
	files := &helperStatusFilesystem{}
	gui := &helperStatusDesktop{}
	server := NewHelperRPCServer(endpoint, key, term, files, gui)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForHelperEndpoint(t, endpoint)

	client := ipc.NewRPCClient(endpoint, key)
	if err := client.Call(context.Background(), RPCMethodTerminalSignal, map[string]any{
		"session_id": "owner",
		"signal":     "interrupt",
	}, &struct{}{}); err != nil {
		t.Fatalf("terminal.signal: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodTerminalResize, map[string]any{
		"session_id": "owner",
		"columns":    132,
		"rows":       43,
	}, &struct{}{}); err != nil {
		t.Fatalf("terminal.resize: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodFilesystemAppend, map[string]any{
		"path": "/tmp/a.txt",
		"data": []byte("x"),
		"perm": 384,
	}, &struct{}{}); err != nil {
		t.Fatalf("filesystem.append: %v", err)
	}
	if err := client.Call(context.Background(), RPCMethodFilesystemMkdir, map[string]any{
		"path": "/tmp/dir",
		"perm": 493,
	}, &struct{}{}); err != nil {
		t.Fatalf("filesystem.mkdir: %v", err)
	}

	var status RPCDeviceStatus
	if err := client.Call(context.Background(), RPCMethodDeviceStatus, struct{}{}, &status); err != nil {
		t.Fatalf("device.status: %v", err)
	}
	if status.Component != "desktop" || !status.Available || status.TerminalSessions != 2 {
		t.Fatalf("unexpected device status: %#v", status)
	}
	if term.resizeSessionID != "owner" || term.resizeColumns != 132 || term.resizeRows != 43 {
		t.Fatalf("terminal resize = %#v", term)
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
func (helperTerminal) Signal(sessionID string, signal terminal.Signal) error {
	return nil
}
func (helperTerminal) Resize(sessionID string, columns, rows int) error { return nil }

type helperFilesystem struct{}

func (helperFilesystem) ReadFile(path string) ([]byte, error) { return nil, nil }
func (helperFilesystem) List(path string) ([]filesystem.Entry, error) {
	return nil, nil
}
func (helperFilesystem) Glob(pattern string) ([]string, error) { return nil, nil }
func (helperFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return filesystem.FileInfo{}, nil
}
func (helperFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error  { return nil }
func (helperFilesystem) AppendFile(path string, data []byte, perm fs.FileMode) error { return nil }
func (helperFilesystem) Mkdir(path string, perm fs.FileMode) error                   { return nil }
func (helperFilesystem) Move(src, dst string) error                                  { return nil }
func (helperFilesystem) Delete(path string) error                                    { return nil }

type helperDesktop struct{}

func (helperDesktop) Screenshot(ctx context.Context, path string) error { return nil }
func (helperDesktop) Windows(ctx context.Context) ([]Window, error)     { return nil, nil }
func (helperDesktop) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (helperDesktop) Mouse(ctx context.Context, action MouseAction) error       { return nil }
func (helperDesktop) Keyboard(ctx context.Context, action KeyboardAction) error { return nil }
func (helperDesktop) App(ctx context.Context, action AppAction) error           { return nil }
func (helperDesktop) Available(ctx context.Context) bool                        { return true }

type helperStatusTerminal struct {
	signalSessionID string
	signal          terminal.Signal
	resizeSessionID string
	resizeColumns   int
	resizeRows      int
}

func (h *helperStatusTerminal) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	return terminal.Session{}, nil
}
func (h *helperStatusTerminal) Write(sessionID string, input []byte) error { return nil }
func (h *helperStatusTerminal) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	return terminal.OutputChunk{}, nil
}
func (h *helperStatusTerminal) List() []terminal.SessionInfo { return []terminal.SessionInfo{{}, {}} }
func (h *helperStatusTerminal) Close(sessionID string) error { return nil }
func (h *helperStatusTerminal) Kill(sessionID string) error  { return nil }
func (h *helperStatusTerminal) KillAll() error               { return nil }
func (h *helperStatusTerminal) Signal(sessionID string, signal terminal.Signal) error {
	h.signalSessionID = sessionID
	h.signal = signal
	return nil
}
func (h *helperStatusTerminal) Resize(sessionID string, columns, rows int) error {
	h.resizeSessionID = sessionID
	h.resizeColumns = columns
	h.resizeRows = rows
	return nil
}

type helperStatusFilesystem struct{}

func (helperStatusFilesystem) ReadFile(path string) ([]byte, error) { return nil, nil }
func (helperStatusFilesystem) List(path string) ([]filesystem.Entry, error) {
	return nil, nil
}
func (helperStatusFilesystem) Glob(pattern string) ([]string, error) { return nil, nil }
func (helperStatusFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return filesystem.FileInfo{}, nil
}
func (helperStatusFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}
func (helperStatusFilesystem) AppendFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}
func (helperStatusFilesystem) Mkdir(path string, perm fs.FileMode) error { return nil }
func (helperStatusFilesystem) Move(src, dst string) error                { return nil }
func (helperStatusFilesystem) Delete(path string) error                  { return nil }

type helperStatusDesktop struct{}

func (helperStatusDesktop) Screenshot(ctx context.Context, path string) error { return nil }
func (helperStatusDesktop) Windows(ctx context.Context) ([]Window, error)     { return nil, nil }
func (helperStatusDesktop) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (helperStatusDesktop) Mouse(ctx context.Context, action MouseAction) error       { return nil }
func (helperStatusDesktop) Keyboard(ctx context.Context, action KeyboardAction) error { return nil }
func (helperStatusDesktop) App(ctx context.Context, action AppAction) error           { return nil }
func (helperStatusDesktop) Available(ctx context.Context) bool                        { return true }

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

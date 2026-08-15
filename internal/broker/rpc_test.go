//go:build darwin || linux

package broker

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestAdminRPCServer_ExposesAdminTerminalAndFilesystemOnly(t *testing.T) {
	t.Parallel()

	endpoint := brokerSocketPath(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	term := &rpcTestTerminal{session: terminal.Session{ID: "admin", PID: 101, Dir: "/tmp/admin"}}
	files := &rpcTestFilesystem{readData: []byte("admin-data")}
	server := NewAdminRPCServer(endpoint, key, term, files)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForBrokerEndpoint(t, endpoint)

	client := ipc.NewRPCClient(endpoint, key)
	var session terminal.Session
	if err := client.Call(context.Background(), desktop.RPCMethodTerminalStart, map[string]any{
		"command": []string{"/bin/sh"},
		"dir":     "/tmp/admin",
		"env":     map[string]string{"ROLE": "admin"},
	}, &session); err != nil {
		t.Fatalf("terminal.start: %v", err)
	}
	if session.ID != "admin" || term.started.Dir != "/tmp/admin" {
		t.Fatalf("unexpected terminal start result: %#v %#v", session, term.started)
	}

	var data []byte
	if err := client.Call(context.Background(), desktop.RPCMethodFilesystemRead, map[string]any{"path": "/tmp/admin.txt"}, &data); err != nil {
		t.Fatalf("filesystem.read: %v", err)
	}
	if string(data) != "admin-data" || files.readPath != "/tmp/admin.txt" {
		t.Fatalf("unexpected filesystem read result: %q path=%q", string(data), files.readPath)
	}

	if err := client.Call(context.Background(), desktop.RPCMethodDesktopWindows, struct{}{}, &[]desktop.Window{}); err == nil {
		t.Fatal("desktop.windows unexpectedly succeeded on broker RPC")
	}

	if err := client.Call(context.Background(), desktop.RPCMethodTerminalStart, map[string]any{
		"command": []string{"/bin/sh"},
		"dir":     "/tmp/admin",
		"extra":   true,
	}, &terminal.Session{}); err == nil {
		t.Fatal("terminal.start unexpectedly accepted unknown params")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admin RPC server did not stop")
	}
}

func TestAdminRPCParamsRejectTrailingJSONValues(t *testing.T) {
	t.Parallel()
	if err := decodeAdminParams("device.test", []byte(`{} {"extra":true}`), &struct{}{}); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestDesktopRPCClient_TalksToHelperRPCServer(t *testing.T) {
	t.Parallel()

	endpoint := brokerSocketPath(t)
	key := []byte("abcdef0123456789abcdef0123456789")
	term := &rpcTestTerminal{
		session: terminal.Session{ID: "owner", PID: 7, Dir: "/Users/jamie"},
		list:    []terminal.SessionInfo{{Session: terminal.Session{ID: "owner", PID: 7, Dir: "/Users/jamie"}, Running: true}},
		chunk:   terminal.OutputChunk{Data: []byte("tail"), StartCursor: 2, NextCursor: 6, Running: true},
	}
	files := &rpcTestFilesystem{
		readData: []byte("owner-data"),
		entries:  []filesystem.Entry{{Name: "a.txt", Path: "/tmp/a.txt"}},
		matches:  []string{"/tmp/a.txt"},
		info:     filesystem.FileInfo{Path: "/tmp/a.txt", Size: 10},
	}
	gui := &rpcTestDesktop{
		windows: []desktop.Window{{App: "Finder", Title: "Desktop"}},
		tree:    desktop.AccessibilityTree{Application: "Finder"},
	}
	server := desktop.NewHelperRPCServer(endpoint, key, term, files, gui)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForBrokerEndpoint(t, endpoint)

	client := NewDesktopRPCClient(endpoint, key)

	session, err := client.Terminal.Start(context.Background(), terminal.SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     "/Users/jamie",
		Env:     map[string]string{"ROLE": "owner"},
	})
	if err != nil || session.ID != "owner" {
		t.Fatalf("client.Start = %#v, %v", session, err)
	}
	if err := client.Terminal.Write("owner", []byte("echo hi")); err != nil {
		t.Fatalf("client.Write: %v", err)
	}
	chunk, err := client.Terminal.Read("owner", 2)
	if err != nil || string(chunk.Data) != "tail" || chunk.StartCursor != 2 {
		t.Fatalf("client.Read = %#v, %v", chunk, err)
	}
	list := client.Terminal.List()
	if len(list) != 1 || list[0].Session.ID != "owner" {
		t.Fatalf("client.List = %#v", list)
	}
	if err := client.Terminal.Close("owner"); err != nil {
		t.Fatalf("client.Close: %v", err)
	}
	if err := client.Terminal.Kill("owner"); err != nil {
		t.Fatalf("client.Kill: %v", err)
	}
	if err := client.Terminal.KillAll(); err != nil {
		t.Fatalf("client.KillAll: %v", err)
	}

	if data, err := client.Filesystem.ReadFile("/tmp/a.txt"); err != nil || string(data) != "owner-data" {
		t.Fatalf("client.ReadFile = %q, %v", string(data), err)
	}
	if entries, err := client.Filesystem.List("/tmp"); err != nil || len(entries) != 1 {
		t.Fatalf("client.List(filesystem) = %#v, %v", entries, err)
	}
	if matches, err := client.Filesystem.Glob("/tmp/*.txt"); err != nil || len(matches) != 1 {
		t.Fatalf("client.Glob = %#v, %v", matches, err)
	}
	if info, err := client.Filesystem.Stat("/tmp/a.txt"); err != nil || info.Size != 10 {
		t.Fatalf("client.Stat = %#v, %v", info, err)
	}
	if err := client.Filesystem.WriteFile("/tmp/b.txt", []byte("b"), 0o600); err != nil {
		t.Fatalf("client.WriteFile: %v", err)
	}
	if err := client.Filesystem.Move("/tmp/a.txt", "/tmp/b.txt"); err != nil {
		t.Fatalf("client.Move: %v", err)
	}
	if err := client.Filesystem.Delete("/tmp/b.txt"); err != nil {
		t.Fatalf("client.Delete: %v", err)
	}

	if err := client.Desktop.Screenshot(context.Background(), "/tmp/shot.png"); err != nil {
		t.Fatalf("client.Screenshot: %v", err)
	}
	if windows, err := client.Desktop.Windows(context.Background()); err != nil || len(windows) != 1 {
		t.Fatalf("client.Windows = %#v, %v", windows, err)
	}
	if tree, err := client.Desktop.Accessibility(context.Background()); err != nil || tree.Application != "Finder" {
		t.Fatalf("client.Accessibility = %#v, %v", tree, err)
	}
	if err := client.Desktop.Mouse(context.Background(), desktop.MouseAction{Type: desktop.MouseActionMove, X: 1, Y: 2}); err != nil {
		t.Fatalf("client.Mouse: %v", err)
	}
	if err := client.Desktop.Keyboard(context.Background(), desktop.KeyboardAction{Text: "x"}); err != nil {
		t.Fatalf("client.Keyboard: %v", err)
	}
	if err := client.Desktop.App(context.Background(), desktop.AppAction{Type: desktop.AppActionActivate, Name: "Finder"}); err != nil {
		t.Fatalf("client.App: %v", err)
	}

	if term.writeSessionID != "owner" || term.readCursor != 2 || term.closeSessionID != "owner" || term.killSessionID != "owner" || term.killAllCount != 1 {
		t.Fatalf("unexpected terminal RPC calls: %#v", term)
	}
	if files.writePath != "/tmp/b.txt" || files.moveSrc != "/tmp/a.txt" || files.moveDst != "/tmp/b.txt" || files.deletePath != "/tmp/b.txt" {
		t.Fatalf("unexpected filesystem RPC calls: %#v", files)
	}
	if gui.screenshotPath != "/tmp/shot.png" || gui.mouse.X != 1 || gui.keyboard.Text != "x" || gui.app.Name != "Finder" {
		t.Fatalf("unexpected desktop RPC calls: %#v", gui)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper RPC server did not stop")
	}
}

type rpcTestTerminal struct {
	session        terminal.Session
	started        terminal.SessionSpec
	writeSessionID string
	writeInput     []byte
	readSessionID  string
	readCursor     int64
	list           []terminal.SessionInfo
	chunk          terminal.OutputChunk
	closeSessionID string
	killSessionID  string
	killAllCount   int
}

func (f *rpcTestTerminal) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	f.started = spec
	return f.session, nil
}

func (f *rpcTestTerminal) Write(sessionID string, input []byte) error {
	f.writeSessionID = sessionID
	f.writeInput = append([]byte(nil), input...)
	return nil
}

func (f *rpcTestTerminal) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	f.readSessionID = sessionID
	f.readCursor = cursor
	return f.chunk, nil
}

func (f *rpcTestTerminal) List() []terminal.SessionInfo {
	return f.list
}

func (f *rpcTestTerminal) Close(sessionID string) error {
	f.closeSessionID = sessionID
	return nil
}

func (f *rpcTestTerminal) Kill(sessionID string) error {
	f.killSessionID = sessionID
	return nil
}

func (f *rpcTestTerminal) KillAll() error {
	f.killAllCount++
	return nil
}

type rpcTestFilesystem struct {
	readPath   string
	readData   []byte
	listPath   string
	entries    []filesystem.Entry
	globPat    string
	matches    []string
	statPath   string
	info       filesystem.FileInfo
	writePath  string
	writeData  []byte
	writePerm  fs.FileMode
	moveSrc    string
	moveDst    string
	deletePath string
}

func (f *rpcTestFilesystem) ReadFile(path string) ([]byte, error) {
	f.readPath = path
	return f.readData, nil
}
func (f *rpcTestFilesystem) List(path string) ([]filesystem.Entry, error) {
	f.listPath = path
	return f.entries, nil
}
func (f *rpcTestFilesystem) Glob(pattern string) ([]string, error) {
	f.globPat = pattern
	return f.matches, nil
}
func (f *rpcTestFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	f.statPath = path
	return f.info, nil
}
func (f *rpcTestFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	f.writePath = path
	f.writeData = append([]byte(nil), data...)
	f.writePerm = perm
	return nil
}
func (f *rpcTestFilesystem) Move(src, dst string) error {
	f.moveSrc = src
	f.moveDst = dst
	return nil
}
func (f *rpcTestFilesystem) Delete(path string) error {
	f.deletePath = path
	return nil
}

type rpcTestDesktop struct {
	screenshotPath string
	windows        []desktop.Window
	tree           desktop.AccessibilityTree
	mouse          desktop.MouseAction
	keyboard       desktop.KeyboardAction
	app            desktop.AppAction
}

func (f *rpcTestDesktop) Screenshot(ctx context.Context, path string) error {
	f.screenshotPath = path
	return nil
}
func (f *rpcTestDesktop) Windows(ctx context.Context) ([]desktop.Window, error) {
	return f.windows, nil
}
func (f *rpcTestDesktop) Accessibility(ctx context.Context) (desktop.AccessibilityTree, error) {
	return f.tree, nil
}
func (f *rpcTestDesktop) Mouse(ctx context.Context, action desktop.MouseAction) error {
	f.mouse = action
	return nil
}
func (f *rpcTestDesktop) Keyboard(ctx context.Context, action desktop.KeyboardAction) error {
	f.keyboard = action
	return nil
}
func (f *rpcTestDesktop) App(ctx context.Context, action desktop.AppAction) error {
	f.app = action
	return nil
}

func waitForBrokerEndpoint(t *testing.T, endpoint string) {
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

func brokerSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "executor-broker-rpc-")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

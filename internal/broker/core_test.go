package broker

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/terminal"
)

func TestCore_RoutesTerminalSessionsByPrivilege(t *testing.T) {
	ownerExec := &fakeTerminalExecutor{
		session: terminal.Session{ID: "owner-session"},
	}
	adminExec := &fakeTerminalExecutor{
		session: terminal.Session{ID: "admin-session"},
	}
	core := NewCore(ownerExec, adminExec, &fakeFilesystem{}, &fakeDesktop{})

	ownerSession, err := core.StartSession(context.Background(), PrivilegeOwner, terminal.SessionSpec{Dir: "/tmp/owner"})
	if err != nil {
		t.Fatalf("start owner session: %v", err)
	}
	if ownerSession.ID != "owner-session" {
		t.Fatalf("unexpected owner session: %#v", ownerSession)
	}
	if ownerExec.startCount != 1 || adminExec.startCount != 0 {
		t.Fatalf("unexpected executor routing: owner=%d admin=%d", ownerExec.startCount, adminExec.startCount)
	}

	adminSession, err := core.StartSession(context.Background(), PrivilegeAdmin, terminal.SessionSpec{Dir: "/tmp/admin"})
	if err != nil {
		t.Fatalf("start admin session: %v", err)
	}
	if adminSession.ID != "admin-session" {
		t.Fatalf("unexpected admin session: %#v", adminSession)
	}
	if ownerExec.startCount != 1 || adminExec.startCount != 1 {
		t.Fatalf("unexpected executor routing after admin start: owner=%d admin=%d", ownerExec.startCount, adminExec.startCount)
	}

	if _, err := core.ListSessions(PrivilegeOwner); err != nil {
		t.Fatalf("list owner sessions: %v", err)
	}
	if err := core.CloseSession("owner-session", PrivilegeOwner); err != nil {
		t.Fatalf("close owner session: %v", err)
	}
	if err := core.KillAllSessions(PrivilegeAdmin); err != nil {
		t.Fatalf("kill all admin sessions: %v", err)
	}
	if ownerExec.listCount != 1 || ownerExec.closeCount != 1 {
		t.Fatalf("unexpected owner executor calls: list=%d close=%d", ownerExec.listCount, ownerExec.closeCount)
	}
	if adminExec.killAllCount != 1 {
		t.Fatalf("unexpected admin kill all calls: %d", adminExec.killAllCount)
	}
}

func TestCore_RejectsUnknownPrivilege(t *testing.T) {
	core := NewCore(&fakeTerminalExecutor{}, &fakeTerminalExecutor{}, &fakeFilesystem{}, &fakeDesktop{})

	_, err := core.StartSession(context.Background(), Privilege("root"), terminal.SessionSpec{})
	if err == nil {
		t.Fatal("expected privilege error")
	}
	if !errors.Is(err, ErrUnknownPrivilege) {
		t.Fatalf("expected ErrUnknownPrivilege, got %v", err)
	}
}

func TestCore_DelegatesFilesystemAndDesktop(t *testing.T) {
	fs := &fakeFilesystem{
		readData: []byte("hello"),
		entries:  []filesystem.Entry{{Path: "/tmp/a.txt"}},
		info:     filesystem.FileInfo{Path: "/tmp/a.txt", Size: 5},
	}
	gui := &fakeDesktop{
		windows: []desktop.Window{{App: "Finder", Title: "Desktop"}},
		tree:    desktop.AccessibilityTree{Application: "Finder"},
	}
	core := NewCore(&fakeTerminalExecutor{}, &fakeTerminalExecutor{}, fs, gui)

	data, err := core.ReadFile("/tmp/a.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("read file = %q, %v", string(data), err)
	}
	if _, err := core.List("/tmp"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := core.Glob("/tmp/*.txt"); err != nil {
		t.Fatalf("glob: %v", err)
	}
	if _, err := core.Stat("/tmp/a.txt"); err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := core.WriteFile("/tmp/b.txt", []byte("b"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := core.Move("/tmp/a.txt", "/tmp/b.txt"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := core.Delete("/tmp/b.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := core.Screenshot(context.Background(), "/tmp/shot.png"); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if _, err := core.Windows(context.Background()); err != nil {
		t.Fatalf("windows: %v", err)
	}
	if _, err := core.Accessibility(context.Background()); err != nil {
		t.Fatalf("accessibility: %v", err)
	}
	if err := core.Mouse(context.Background(), desktop.MouseAction{Type: desktop.MouseActionMove, X: 1, Y: 2}); err != nil {
		t.Fatalf("mouse: %v", err)
	}
	if err := core.Keyboard(context.Background(), desktop.KeyboardAction{Text: "x"}); err != nil {
		t.Fatalf("keyboard: %v", err)
	}
	if err := core.App(context.Background(), desktop.AppAction{Type: desktop.AppActionActivate, Name: "Finder"}); err != nil {
		t.Fatalf("app: %v", err)
	}
}

type fakeTerminalExecutor struct {
	session      terminal.Session
	startCount   int
	listCount    int
	closeCount   int
	killAllCount int
	resizeCount  int
}

func (f *fakeTerminalExecutor) Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error) {
	f.startCount++
	return f.session, nil
}

func (f *fakeTerminalExecutor) Write(sessionID string, input []byte) error {
	return nil
}

func (f *fakeTerminalExecutor) Read(sessionID string, cursor int64) (terminal.OutputChunk, error) {
	return terminal.OutputChunk{}, nil
}

func (f *fakeTerminalExecutor) Kill(sessionID string) error {
	return nil
}

func (f *fakeTerminalExecutor) List() []terminal.SessionInfo {
	f.listCount++
	return []terminal.SessionInfo{{Session: f.session, Running: true}}
}

func (f *fakeTerminalExecutor) Close(sessionID string) error {
	f.closeCount++
	return nil
}

func (f *fakeTerminalExecutor) KillAll() error {
	f.killAllCount++
	return nil
}

func (f *fakeTerminalExecutor) Signal(sessionID string, signal terminal.Signal) error {
	return nil
}

func (f *fakeTerminalExecutor) Resize(sessionID string, columns, rows int) error {
	f.resizeCount++
	return nil
}

type fakeFilesystem struct {
	readData []byte
	entries  []filesystem.Entry
	info     filesystem.FileInfo
}

func (f *fakeFilesystem) ReadFile(path string) ([]byte, error) {
	return f.readData, nil
}

func (f *fakeFilesystem) List(path string) ([]filesystem.Entry, error) {
	return f.entries, nil
}

func (f *fakeFilesystem) Glob(pattern string) ([]string, error) {
	return []string{pattern}, nil
}

func (f *fakeFilesystem) Stat(path string) (filesystem.FileInfo, error) {
	return f.info, nil
}

func (f *fakeFilesystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}

func (f *fakeFilesystem) AppendFile(path string, data []byte, perm fs.FileMode) error {
	return nil
}

func (f *fakeFilesystem) Mkdir(path string, perm fs.FileMode) error {
	return nil
}

func (f *fakeFilesystem) Move(src, dst string) error {
	return nil
}

func (f *fakeFilesystem) Delete(path string) error {
	return nil
}

type fakeDesktop struct {
	windows []desktop.Window
	tree    desktop.AccessibilityTree
}

func (f *fakeDesktop) Screenshot(ctx context.Context, path string) error {
	return nil
}

func (f *fakeDesktop) Windows(ctx context.Context) ([]desktop.Window, error) {
	return f.windows, nil
}

func (f *fakeDesktop) Accessibility(ctx context.Context) (desktop.AccessibilityTree, error) {
	return f.tree, nil
}

func (f *fakeDesktop) Mouse(ctx context.Context, action desktop.MouseAction) error {
	return nil
}

func (f *fakeDesktop) Keyboard(ctx context.Context, action desktop.KeyboardAction) error {
	return nil
}

func (f *fakeDesktop) App(ctx context.Context, action desktop.AppAction) error {
	return nil
}

func (f *fakeDesktop) Available(ctx context.Context) bool {
	return true
}

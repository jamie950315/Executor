//go:build darwin || linux

package terminal

import (
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManager_PersistentSessionKeepsDirEnvAndCursor(t *testing.T) {
	manager := NewManager()
	dir := t.TempDir()
	session, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     dir,
		Env: map[string]string{
			"EXECUTOR_TEST_VALUE": "kept",
		},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Kill(session.ID)
	})

	if err := manager.Write(session.ID, []byte("pwd\nprintf 'env:%s\\n' \"$EXECUTOR_TEST_VALUE\"\n")); err != nil {
		t.Fatalf("write session: %v", err)
	}

	chunk := waitForOutput(t, manager, session.ID, 0, dir, "env:kept")
	if !strings.Contains(string(chunk.Data), dir) {
		t.Fatalf("expected output to contain cwd %q, got %q", dir, string(chunk.Data))
	}
	if !strings.Contains(string(chunk.Data), "env:kept") {
		t.Fatalf("expected output to contain env marker, got %q", string(chunk.Data))
	}

	empty, err := manager.Read(session.ID, chunk.NextCursor)
	if err != nil {
		t.Fatalf("read tail cursor: %v", err)
	}
	if len(empty.Data) != 0 {
		t.Fatalf("expected empty incremental read, got %q", string(empty.Data))
	}
}

func TestManager_StartIgnoresRequestContextCancellationAfterLaunch(t *testing.T) {
	manager := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	session, err := manager.Start(ctx, SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Kill(session.ID)
	})

	cancel()
	time.Sleep(200 * time.Millisecond)

	if err := manager.Write(session.ID, []byte("echo STILL_ALIVE\n")); err != nil {
		t.Fatalf("write after context cancel: %v", err)
	}
	chunk := waitForOutput(t, manager, session.ID, 0, "STILL_ALIVE")
	if !chunk.Running {
		t.Fatalf("expected session to remain running after request context cancel")
	}
}

func TestManager_UsesUnpredictableSessionIDsAndListsSessions(t *testing.T) {
	manager := NewManager()
	first, err := manager.Start(context.Background(), SessionSpec{Command: []string{"/bin/sh"}, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("start first session: %v", err)
	}
	second, err := manager.Start(context.Background(), SessionSpec{Command: []string{"/bin/sh"}, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.KillAll()
	})

	hexID := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if !hexID.MatchString(first.ID) || !hexID.MatchString(second.ID) {
		t.Fatalf("expected random hex session IDs, got %q and %q", first.ID, second.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("expected distinct session IDs, got %q", first.ID)
	}

	sessions := manager.List()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 listed sessions, got %d", len(sessions))
	}
}

func TestManager_KillStopsBackgroundProcessTree(t *testing.T) {
	manager := NewManager()
	session, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if err := manager.Write(session.ID, []byte("sleep 30 &\necho READY:$!\n")); err != nil {
		t.Fatalf("write session: %v", err)
	}

	chunk := waitForOutput(t, manager, session.ID, 0, "READY:")
	output := string(chunk.Data)
	pidText := strings.TrimSpace(output[strings.LastIndex(output, "READY:")+len("READY:"):])
	pidText = strings.Fields(pidText)[0]
	childPID, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatalf("parse child pid from %q: %v", output, err)
	}

	if err := manager.Kill(session.ID); err != nil {
		t.Fatalf("kill session: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if err != nil {
			if err == syscall.ESRCH {
				return
			}
			t.Fatalf("probe child pid %d: %v", childPID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("background process %d still alive after session kill", childPID)
}

func TestManager_SignalInterruptStopsForegroundCommandWithoutRemovingSession(t *testing.T) {
	manager := NewManager()
	session, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Kill(session.ID)
	})

	if err := manager.Write(session.ID, []byte("trap 'echo INTERRUPTED' INT\nsleep 30\necho AFTER\n")); err != nil {
		t.Fatalf("write session: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if err := manager.Signal(session.ID, SignalInterrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}

	chunk := waitForOutput(t, manager, session.ID, 0, "INTERRUPTED")

	if err := manager.Write(session.ID, []byte("echo STILL_RUNNING\n")); err != nil {
		t.Fatalf("write after interrupt: %v", err)
	}
	waitForOutput(t, manager, session.ID, chunk.NextCursor, "STILL_RUNNING")
}

func TestManager_CloseRemovesSession(t *testing.T) {
	manager := NewManager()
	session, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if err := manager.Close(session.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}

	if _, err := manager.Read(session.ID, 0); err == nil {
		t.Fatal("expected closed session to be removed")
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("expected no listed sessions after close, got %d", len(got))
	}
}

func TestManager_ResizeValidatesAndForwardsDimensions(t *testing.T) {
	process := newResizeTestProcess()
	manager := &Manager{launcher: resizeTestLauncher{process: process}, sessions: map[string]*sessionState{}}
	session, err := manager.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(session.ID) })

	if err := manager.Resize(session.ID, 132, 43); err != nil {
		t.Fatalf("resize: %v", err)
	}
	process.mu.Lock()
	columns, rows := process.columns, process.rows
	process.mu.Unlock()
	if columns != 132 || rows != 43 {
		t.Fatalf("resize dimensions = %dx%d", columns, rows)
	}
	if err := manager.Resize(session.ID, 0, 43); err == nil {
		t.Fatal("zero-width resize succeeded")
	}
	if err := manager.Resize(session.ID, 32768, 43); err == nil {
		t.Fatal("overflowing resize succeeded")
	}
}

func TestManager_StartRejectsOverflowingTerminalDimensionsBeforeLaunch(t *testing.T) {
	manager := &Manager{launcher: panicTestLauncher{}, sessions: map[string]*sessionState{}}
	if _, err := manager.Start(context.Background(), SessionSpec{Command: []string{"fake"}, Columns: 32768, Rows: 24}); err == nil {
		t.Fatal("overflowing initial dimensions succeeded")
	}
}

func TestManager_WriteDoesNotBlockOutputCapture(t *testing.T) {
	process := newBlockingIOTestProcess()
	manager := &Manager{launcher: blockingIOTestLauncher{process: process}, sessions: map[string]*sessionState{}}
	session, err := manager.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(session.ID) })

	writeDone := make(chan error, 1)
	go func() { writeDone <- manager.Write(session.ID, []byte("blocked input")) }()
	<-process.writeStarted
	process.output <- []byte("OUTPUT_DRAINED")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		chunk, readErr := manager.Read(session.ID, 0)
		if readErr != nil {
			t.Fatalf("read: %v", readErr)
		}
		if strings.Contains(string(chunk.Data), "OUTPUT_DRAINED") {
			close(process.releaseWrite)
			if err := <-writeDone; err != nil {
				t.Fatalf("write: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(process.releaseWrite)
	<-writeDone
	t.Fatal("output capture was blocked by a pending terminal write")
}

func TestManager_RejectsUnknownSignalWithoutTouchingProcess(t *testing.T) {
	process := newResizeTestProcess()
	manager := &Manager{launcher: resizeTestLauncher{process: process}, sessions: map[string]*sessionState{}}
	session, err := manager.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(session.ID) })
	if err := manager.Signal(session.ID, Signal("typo")); err == nil {
		t.Fatal("unknown signal succeeded")
	}
	process.mu.Lock()
	signalCount := process.signalCount
	process.mu.Unlock()
	if signalCount != 0 {
		t.Fatalf("unknown signal reached process %d times", signalCount)
	}
}

func TestManager_KillAllStopsAllProcessTrees(t *testing.T) {
	manager := NewManager()
	first, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start first session: %v", err)
	}
	second, err := manager.Start(context.Background(), SessionSpec{
		Command: []string{"/bin/sh"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}

	if err := manager.Write(first.ID, []byte("sleep 30 &\necho READY1:$!\n")); err != nil {
		t.Fatalf("write first session: %v", err)
	}
	if err := manager.Write(second.ID, []byte("sleep 30 &\necho READY2:$!\n")); err != nil {
		t.Fatalf("write second session: %v", err)
	}

	firstPID := parseBackgroundPID(t, waitForOutput(t, manager, first.ID, 0, "READY1:"), "READY1:")
	secondPID := parseBackgroundPID(t, waitForOutput(t, manager, second.ID, 0, "READY2:"), "READY2:")

	if err := manager.KillAll(); err != nil {
		t.Fatalf("kill all sessions: %v", err)
	}

	waitForPIDExit(t, firstPID)
	waitForPIDExit(t, secondPID)

	if got := manager.List(); len(got) != 0 {
		t.Fatalf("expected no listed sessions after kill all, got %d", len(got))
	}
}

func TestOutputBuffer_BoundsOldOutputByCursor(t *testing.T) {
	buffer := newOutputBuffer(8)
	buffer.Append([]byte("abcdef"))
	buffer.Append([]byte("ghijkl"))

	chunk := buffer.Read(0)
	if string(chunk.Data) != "efghijkl" {
		t.Fatalf("expected bounded data, got %q", string(chunk.Data))
	}
	if chunk.StartCursor != 4 || chunk.NextCursor != 12 || !chunk.Truncated {
		t.Fatalf("unexpected chunk metadata: %#v", chunk)
	}

	tail := buffer.Read(8)
	if string(tail.Data) != "ijkl" {
		t.Fatalf("expected tail read, got %q", string(tail.Data))
	}
	if tail.StartCursor != 8 || tail.NextCursor != 12 || tail.Truncated {
		t.Fatalf("unexpected tail chunk metadata: %#v", tail)
	}
}

func waitForOutput(t *testing.T, manager *Manager, sessionID string, cursor int64, needles ...string) OutputChunk {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		chunk, err := manager.Read(sessionID, cursor)
		if err != nil {
			t.Fatalf("read output: %v", err)
		}
		text := string(chunk.Data)
		found := true
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				found = false
				break
			}
		}
		if found {
			return chunk
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for output containing %q", needles)
	return OutputChunk{}
}

func parseBackgroundPID(t *testing.T, chunk OutputChunk, marker string) int {
	t.Helper()
	output := string(chunk.Data)
	pidText := strings.TrimSpace(output[strings.LastIndex(output, marker)+len(marker):])
	pidText = strings.Fields(pidText)[0]
	childPID, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatalf("parse child pid from %q: %v", output, err)
	}
	return childPID
}

func waitForPIDExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err != nil {
			if err == syscall.ESRCH {
				return
			}
			t.Fatalf("probe child pid %d: %v", pid, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("background process %d still alive after session shutdown", pid)
}

type resizeTestLauncher struct{ process *resizeTestProcess }

func (l resizeTestLauncher) Start(SessionSpec) (terminalProcess, error) { return l.process, nil }

type panicTestLauncher struct{}

func (panicTestLauncher) Start(SessionSpec) (terminalProcess, error) {
	panic("launcher called for invalid dimensions")
}

type resizeTestProcess struct {
	mu          sync.Mutex
	columns     int
	rows        int
	done        chan struct{}
	once        sync.Once
	reader      *io.PipeReader
	writer      *io.PipeWriter
	signalCount int
}

func newResizeTestProcess() *resizeTestProcess {
	reader, writer := io.Pipe()
	return &resizeTestProcess{done: make(chan struct{}), reader: reader, writer: writer}
}

func (p *resizeTestProcess) PID() int                       { return 42 }
func (p *resizeTestProcess) Read(data []byte) (int, error)  { return p.reader.Read(data) }
func (p *resizeTestProcess) Write(data []byte) (int, error) { return len(data), nil }
func (p *resizeTestProcess) Wait() error                    { <-p.done; return nil }
func (p *resizeTestProcess) CloseInput() error              { return nil }
func (p *resizeTestProcess) Signal(Signal) error {
	p.mu.Lock()
	p.signalCount++
	p.mu.Unlock()
	return nil
}
func (p *resizeTestProcess) Kill() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.writer.Close()
	})
	return nil
}
func (p *resizeTestProcess) Resize(columns, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.columns = columns
	p.rows = rows
	return nil
}

type blockingIOTestLauncher struct{ process *blockingIOTestProcess }

func (l blockingIOTestLauncher) Start(SessionSpec) (terminalProcess, error) { return l.process, nil }

type blockingIOTestProcess struct {
	output       chan []byte
	writeStarted chan struct{}
	releaseWrite chan struct{}
	done         chan struct{}
	killOnce     sync.Once
}

func newBlockingIOTestProcess() *blockingIOTestProcess {
	return &blockingIOTestProcess{
		output: make(chan []byte, 1), writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}), done: make(chan struct{}),
	}
}

func (p *blockingIOTestProcess) PID() int { return 43 }
func (p *blockingIOTestProcess) Read(data []byte) (int, error) {
	select {
	case output := <-p.output:
		return copy(data, output), nil
	case <-p.done:
		return 0, io.EOF
	}
}
func (p *blockingIOTestProcess) Write(data []byte) (int, error) {
	select {
	case <-p.writeStarted:
	default:
		close(p.writeStarted)
	}
	<-p.releaseWrite
	return len(data), nil
}
func (p *blockingIOTestProcess) Wait() error           { <-p.done; return nil }
func (p *blockingIOTestProcess) CloseInput() error     { return nil }
func (p *blockingIOTestProcess) Signal(Signal) error   { return nil }
func (p *blockingIOTestProcess) Resize(int, int) error { return nil }
func (p *blockingIOTestProcess) Kill() error {
	p.killOnce.Do(func() { close(p.done) })
	return nil
}

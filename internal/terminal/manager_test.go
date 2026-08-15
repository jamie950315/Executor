//go:build darwin || linux

package terminal

import (
	"context"
	"regexp"
	"strconv"
	"strings"
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

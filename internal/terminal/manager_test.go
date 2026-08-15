package terminal

import (
	"context"
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

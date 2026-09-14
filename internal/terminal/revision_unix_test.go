//go:build darwin || linux

package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestTerminalRevisionSignalFixture(t *testing.T) {
	if os.Getenv("EXECUTOR_SIGNAL_FIXTURE") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	fmt.Println("SIGNAL_READY")
	received := <-ch
	if err := os.WriteFile(os.Getenv("EXECUTOR_SIGNAL_RESULT"), []byte("handled"), 0o600); err != nil {
		os.Exit(99)
	}
	fmt.Println("SIGNAL_FINISHED")
	if received == syscall.SIGTERM {
		os.Exit(42)
	}
	os.Exit(43)
}

func revisionWait(t *testing.T, m *Manager, id string) OutputChunk {
	t.Helper()
	state, err := m.session(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-state.done:
	case <-time.After(6 * time.Second):
		t.Fatal("fixture session did not complete")
	}
	chunk, err := m.Read(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

func TestTerminalRevisionTerminateRunsHandlerAndPreservesFinalOutput(t *testing.T) {
	for _, sig := range []Signal{SignalTerminate, SignalInterrupt} {
		t.Run(string(sig), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "handler-result")
			m := NewManager()
			s, err := m.Start(context.Background(), SessionSpec{Command: []string{executable, "-test.run=^TestTerminalRevisionSignalFixture$"}, Env: map[string]string{"EXECUTOR_SIGNAL_FIXTURE": "1", "EXECUTOR_SIGNAL_RESULT": path}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			waitForOutput(t, m, s.ID, 0, "SIGNAL_READY")
			if err := m.Signal(s.ID, sig); err != nil {
				t.Fatal(err)
			}
			chunk := revisionWait(t, m, s.ID)
			want := 42
			if sig == SignalInterrupt {
				want = 43
			}
			if chunk.ExitCode == nil || *chunk.ExitCode != want || !strings.Contains(string(chunk.Data), "SIGNAL_FINISHED") {
				t.Fatalf("handler output/exit lost: exit=%v output=%q", chunk.ExitCode, chunk.Data)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "handled" {
				t.Fatalf("handler file: %q %v", body, err)
			}
		})
	}
}

func TestTerminalRevisionNativeInitialSizeAndResize(t *testing.T) {
	m := NewManager()
	s, err := m.Start(context.Background(), SessionSpec{Command: []string{"/bin/sh", "-c", "stty size; printf 'SIZE_READY\\n'; read value; stty size"}, Columns: 91, Rows: 37})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	first := waitForOutput(t, m, s.ID, 0, "SIZE_READY")
	if !strings.Contains(string(first.Data), "37 91") {
		t.Errorf("initial PTY size ignored: %q", first.Data)
	}
	if err := m.Resize(s.ID, 132, 43); err != nil {
		t.Fatal(err)
	}
	if err := m.Write(s.ID, []byte("\n")); err != nil {
		t.Fatal(err)
	}
	last := revisionWait(t, m, s.ID)
	if !strings.Contains(string(last.Data), "43 132") {
		t.Fatalf("resize not visible to process: %q", last.Data)
	}
}

func TestTerminalRevisionCWDIdentifiesMissingOrNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, code := range map[string]string{filepath.Join(dir, "missing"): "CWD_NOT_FOUND", file: "CWD_NOT_DIRECTORY"} {
		m := NewManager()
		_, err := m.Start(context.Background(), SessionSpec{Command: []string{"/usr/bin/true"}, Dir: path})
		if err == nil || !strings.Contains(err.Error(), code) || !strings.Contains(err.Error(), path) {
			t.Errorf("cwd error must identify target: %v", err)
		}
		if len(m.List()) != 0 {
			t.Error("invalid cwd created a session")
		}
	}
}

type revisionStreamReader interface {
	ReadStream(string, int64, int, string) (OutputChunk, error)
}
type revisionInputCloser interface{ CloseStdin(string) error }

func revisionPipeSpec(t *testing.T, argv []string) SessionSpec {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Command": argv, "tty": false})
	if err != nil {
		t.Fatal(err)
	}
	var spec SessionSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestTerminalRevisionPipeModeHasNoTTYAndSeparateStreams(t *testing.T) {
	m := NewManager()
	spec := revisionPipeSpec(t, []string{"/bin/sh", "-c", "for fd in 0 1 2; do if test -t $fd; then printf TTY; else printf PIPE; fi; done; printf '\\000OUT\\n'; printf 'ERR\\n' >&2; exit 7"})
	s, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	chunk := revisionWait(t, m, s.ID)
	if !strings.Contains(string(chunk.Data), "PIPEPIPEPIPE") {
		t.Errorf("pipe execution is still a TTY: %q", chunk.Data)
	}
	r, ok := any(m).(revisionStreamReader)
	if !ok {
		t.Fatal("separate output stream reads are missing")
	}
	for stream, want := range map[string]string{"stdout": "PIPEPIPEPIPE\x00OUT\n", "stderr": "ERR\n"} {
		var all []byte
		cursor := int64(0)
		for {
			part, err := r.ReadStream(s.ID, cursor, 2, stream)
			if err != nil {
				t.Fatal(err)
			}
			if len(part.Data) > 2 || part.NextCursor != cursor+int64(len(part.Data)) {
				t.Fatal("invalid stream pagination")
			}
			all = append(all, part.Data...)
			cursor = part.NextCursor
			if !part.HasMore {
				break
			}
		}
		if string(all) != want {
			t.Errorf("%s bytes=%q want=%q", stream, all, want)
		}
	}
	if chunk.ExitCode == nil || *chunk.ExitCode != 7 {
		t.Fatal("pipe exit code lost")
	}
	metadata := contractJSON(t, s)
	if metadata["tty"] != false {
		t.Fatalf("pipe mode metadata missing: %#v", metadata)
	}
}

func TestTerminalRevisionPipeStdinEOFAndSignal(t *testing.T) {
	m := NewManager()
	s, err := m.Start(context.Background(), revisionPipeSpec(t, []string{"/bin/sh", "-c", "cat; printf EOF >&2"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	closer, ok := any(m).(revisionInputCloser)
	if !ok {
		t.Fatal("close_stdin without removing session is missing")
	}
	if err := m.Write(s.ID, []byte("literal\x03\x00\n")); err != nil {
		t.Fatal(err)
	}
	if err := closer.CloseStdin(s.ID); err != nil {
		t.Fatal(err)
	}
	chunk := revisionWait(t, m, s.ID)
	if !strings.Contains(string(chunk.Data), "literal\x03\x00\n") || !strings.Contains(string(chunk.Data), "EOF") {
		t.Fatalf("pipe stdin/final output lost: %q", chunk.Data)
	}
	if err := m.Write(s.ID, []byte("late")); err == nil {
		t.Fatal("write after EOF accepted")
	}
}

func TestTerminalRevisionPipeInterruptAndTerminate(t *testing.T) {
	for _, sig := range []Signal{SignalInterrupt, SignalTerminate} {
		t.Run(string(sig), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			spec := revisionPipeSpec(t, []string{executable, "-test.run=^TestTerminalRevisionSignalFixture$"})
			spec.Env = map[string]string{"EXECUTOR_SIGNAL_FIXTURE": "1", "EXECUTOR_SIGNAL_RESULT": filepath.Join(t.TempDir(), "result")}
			m := NewManager()
			s, err := m.Start(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			waitForOutput(t, m, s.ID, 0, "SIGNAL_READY")
			if err := m.Signal(s.ID, sig); err != nil {
				t.Fatal(err)
			}
			chunk := revisionWait(t, m, s.ID)
			want := 42
			if sig == SignalInterrupt {
				want = 43
			}
			if chunk.ExitCode == nil || *chunk.ExitCode != want || !strings.Contains(string(chunk.Data), "SIGNAL_FINISHED") {
				t.Fatal("pipe signal handler outcome missing")
			}
		})
	}
}

func TestTerminalRevisionChildPATHLookupAndExactArguments(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "fixture-tool")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tty := range []bool{true, false} {
		for _, searchPath := range []string{dir, "."} {
			m := NewManager()
			s, err := m.Start(context.Background(), SessionSpec{Command: []string{"fixture-tool", "literal ; $HOME '中文'"}, Dir: dir, Env: map[string]string{"PATH": searchPath}, TTY: &tty})
			if err != nil {
				t.Error(err)
				continue
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			chunk := revisionWait(t, m, s.ID)
			if !strings.Contains(string(chunk.Data), "literal ; $HOME '中文'") {
				t.Fatalf("argv changed: %q", chunk.Data)
			}
		}
	}
}

func TestTerminalRevisionLargePipeFixture(t *testing.T) {
	if os.Getenv("EXECUTOR_LARGE_PIPE_FIXTURE") != "1" {
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = os.Stdout.Write(bytes.Repeat([]byte{'O'}, 2<<20)) }()
	go func() { defer wg.Done(); _, _ = os.Stderr.Write(bytes.Repeat([]byte{'E'}, 2<<20)) }()
	wg.Wait()
	os.Exit(0)
}

func TestTerminalRevisionDrainsBothLargePipesBeforeCompletion(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := revisionPipeSpec(t, []string{exe, "-test.run=^TestTerminalRevisionLargePipeFixture$"})
	spec.Env = map[string]string{"EXECUTOR_LARGE_PIPE_FIXTURE": "1"}
	m := NewManager()
	s, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	revisionWait(t, m, s.ID)
	r := any(m).(revisionStreamReader)
	for stream, want := range map[string]byte{"stdout": 'O', "stderr": 'E'} {
		cursor := int64(0)
		for {
			chunk, err := r.ReadStream(s.ID, cursor, 65536, stream)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(chunk.Data, bytes.Repeat([]byte{want}, len(chunk.Data))) || chunk.Truncated {
				t.Fatalf("%s bytes corrupted or lost", stream)
			}
			cursor = chunk.NextCursor
			if !chunk.HasMore {
				break
			}
		}
		if cursor != 2<<20 {
			t.Fatalf("%s drained %d bytes", stream, cursor)
		}
	}
}

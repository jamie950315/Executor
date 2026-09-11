//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type contractExitError int

func (e contractExitError) Error() string { return "test exit" }
func (e contractExitError) ExitCode() int { return int(e) }

type contractExitProcess struct {
	*delayedOutputProcess
	exitErr error
}

func (p *contractExitProcess) Wait() error { close(p.waitReturned); return p.exitErr }

func TestTerminalContractLifecycleWaitsForFinalCapture(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code any
	}{
		{"success", nil, float64(0)},
		{"nonzero", contractExitError(7), float64(7)},
		{"unknown", errors.New("wait failed"), nil},
		{"signaled", contractExitError(-1), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &contractExitProcess{delayedOutputProcess: &delayedOutputProcess{
				resizeTestProcess: newResizeTestProcess(), waitReturned: make(chan struct{}), releaseOutput: make(chan struct{}),
			}, exitErr: tc.err}
			m := &Manager{launcher: delayedOutputLauncher{p}, sessions: map[string]*sessionState{}}
			session, err := m.Start(context.Background(), SessionSpec{Command: []string{"fake-command"}})
			if err != nil {
				t.Fatal(err)
			}
			<-p.waitReturned
			before, err := m.Read(session.ID, 0)
			close(p.releaseOutput)
			t.Cleanup(func() { _ = m.Kill(session.ID) })
			if err != nil {
				t.Fatal(err)
			}
			fields := contractJSON(t, before)
			if fields["sessionRunning"] != true || fields["commandRunning"] != true || fields["exitCode"] != nil {
				t.Fatalf("pending capture reported premature/ambiguous completion: %#v", fields)
			}
			state, err := m.session(session.ID)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-state.done:
			case <-time.After(2 * time.Second):
				t.Fatal("capture did not finish")
			}
			chunk, err := m.Read(session.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			fields = contractJSON(t, chunk)
			if fields["sessionRunning"] != false || fields["commandRunning"] != false || fields["exitCode"] != tc.code || string(chunk.Data) != "final output" {
				t.Fatalf("finished command result = %#v", fields)
			}
			listed := contractJSON(t, m.List()[0])
			if listed["commandRunning"] != false || listed["exitCode"] != tc.code {
				t.Fatalf("listed command result = %#v", listed)
			}
		})
	}
}

func TestTerminalContractRealCommandReportsExitSeven(t *testing.T) {
	m := NewManager()
	text := "literal ; 'quoted' $(pwd)"
	session, err := m.Start(context.Background(), SessionSpec{Command: []string{"/bin/sh", "-c", "printf '%s' \"$1\"; exit 7", "executor-test", text}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(session.ID) })
	state, err := m.session(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-state.done:
	case <-time.After(5 * time.Second):
		t.Fatal("real command did not complete")
	}
	chunk, err := m.Read(session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := contractJSON(t, chunk)
	if fields["mode"] != "command" || fields["commandRunning"] != false || fields["exitCode"] != float64(7) || !strings.Contains(string(chunk.Data), text) {
		t.Fatalf("real command result = %#v", fields)
	}
}

func TestTerminalContractInteractiveShellNeverFabricatesCommandState(t *testing.T) {
	m := NewManager()
	session, err := m.Start(context.Background(), SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(session.ID) })
	if err := m.Write(session.ID, []byte("printf 'SHELL_CHILD_DONE\\n'\n")); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, m, session.ID, 0, "SHELL_CHILD_DONE")
	chunk, err := m.Read(session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := contractJSON(t, chunk)
	for _, key := range []string{"commandRunning", "exitCode"} {
		if value, exists := fields[key]; !exists || value != nil {
			t.Fatalf("interactive %s = %#v; want explicit null", key, value)
		}
	}
	if fields["mode"] != "interactive" || fields["sessionRunning"] != true || !chunk.Running {
		t.Fatalf("interactive status = %#v", fields)
	}
}

type contractShortWriteProcess struct{ *resizeTestProcess }

func (p *contractShortWriteProcess) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestTerminalContractShortWriteDoesNotClaimAccepted(t *testing.T) {
	p := &contractShortWriteProcess{newResizeTestProcess()}
	m := &Manager{launcher: delayedOutputLauncher{p}, sessions: map[string]*sessionState{}}
	session, err := m.Start(context.Background(), SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(session.ID) })
	if err := m.Write(session.ID, []byte("hello")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = %v", err)
	}
}

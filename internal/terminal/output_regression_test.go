//go:build darwin || linux

package terminal

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type delayedOutputProcess struct {
	*resizeTestProcess
	waitReturned  chan struct{}
	releaseOutput chan struct{}
}

func (p *delayedOutputProcess) Wait() error { close(p.waitReturned); return nil }
func (p *delayedOutputProcess) Read(data []byte) (int, error) {
	<-p.releaseOutput
	return copy(data, "final output"), io.EOF
}

type delayedOutputLauncher struct{ process terminalProcess }

func (l delayedOutputLauncher) Start(SessionSpec) (terminalProcess, error) { return l.process, nil }

func TestSessionDoesNotFinishBeforeOutputIsCaptured(t *testing.T) {
	p := &delayedOutputProcess{resizeTestProcess: newResizeTestProcess(), waitReturned: make(chan struct{}), releaseOutput: make(chan struct{})}
	m := &Manager{launcher: delayedOutputLauncher{p}, sessions: map[string]*sessionState{}}
	session, err := m.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatal(err)
	}
	<-p.waitReturned
	time.Sleep(20 * time.Millisecond)
	chunk, err := m.Read(session.ID, 0)
	close(p.releaseOutput)
	t.Cleanup(func() { _ = m.Kill(session.ID) })
	if err != nil {
		t.Fatal(err)
	}
	if !chunk.Running {
		t.Fatal("reported completion while final output was still pending")
	}
}

func TestExitedSessionIncludesAllFinalOutput(t *testing.T) {
	manager := NewManager()
	session, err := manager.Start(context.Background(), SessionSpec{Command: []string{
		"/bin/sh", "-c", "head -c 1048576 /dev/zero | tr '\\000' x; printf FINAL_OUTPUT",
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Kill(session.ID) })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		chunk, err := manager.Read(session.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !chunk.Running {
			if len(chunk.Data) != 1048576+len("FINAL_OUTPUT") || !strings.HasSuffix(string(chunk.Data), "FINAL_OUTPUT") {
				t.Fatalf("finished session lost output: received %d bytes", len(chunk.Data))
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session did not exit")
}

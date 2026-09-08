//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"testing"
)

type failedKillProcess struct {
	*resizeTestProcess
	err error
}

func (p *failedKillProcess) Kill() error { return p.err }

func TestFailedKillKeepsSessionAvailableForRetry(t *testing.T) {
	p := &failedKillProcess{resizeTestProcess: newResizeTestProcess(), err: errors.New("termination denied")}
	m := &Manager{launcher: delayedOutputLauncher{p}, sessions: map[string]*sessionState{}}
	session, err := m.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.resizeTestProcess.Kill() })
	if err := m.Kill(session.ID); !errors.Is(err, p.err) {
		t.Fatalf("kill: %v", err)
	}
	if _, err := m.Read(session.ID, 0); err != nil {
		t.Fatalf("failed termination orphaned session: %v", err)
	}
}

func TestUnconfirmedKillReturnsErrorAndKeepsSession(t *testing.T) {
	p := &failedKillProcess{resizeTestProcess: newResizeTestProcess()}
	m := &Manager{launcher: delayedOutputLauncher{p}, sessions: map[string]*sessionState{}}
	session, err := m.Start(context.Background(), SessionSpec{Command: []string{"fake"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.resizeTestProcess.Kill() })
	if err := m.Kill(session.ID); err == nil {
		t.Error("unconfirmed termination reported success")
	}
	if _, err := m.Read(session.ID, 0); err != nil {
		t.Fatalf("unconfirmed termination orphaned session: %v", err)
	}
}

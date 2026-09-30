//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestManager_PipeKillAfterCommandExit(t *testing.T) {
	m := NewManager()
	tty := false
	s, err := m.Start(context.Background(), SessionSpec{Command: []string{"/bin/sh", "-c", "sleep 30 & printf 'ORPHAN:%d\\n' $!"}, TTY: &tty})
	if err != nil {
		t.Fatal(err)
	}
	childPID := waitForBackgroundPID(t, m, s.ID, "ORPHAN:")
	t.Cleanup(func() {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		_ = m.Kill(s.ID)
	})
	state, err := m.session(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !state.process.(*pipeProcess).exited.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !state.process.(*pipeProcess).exited.Load() {
		t.Fatal("fixture root did not exit")
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatalf("kill session after root exit: %v", err)
	}
	waitForPIDExit(t, childPID)
}

func TestPipeKillRejectsReusedRootPID(t *testing.T) {
	for _, replacedAt := range []int{1, 2} {
		checks, treeKills, groupKills := 0, 0, 0
		p := unixPipeControl{
			rootReplaced: func() (bool, error) { checks++; return checks == replacedAt, nil },
			killTree:     func() error { treeKills++; return nil },
			killGroup:    func() error { groupKills++; return nil },
		}
		if err := p.Kill(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("check %d: reused PID kill = %v", replacedAt, err)
		}
		if treeKills != replacedAt-1 || groupKills != 0 {
			t.Fatalf("check %d: reused PID received tree/group kill %d/%d", replacedAt, treeKills, groupKills)
		}
	}
}

func TestPipeKillRetainsIdentityAndTeardownErrors(t *testing.T) {
	identityErr := errors.New("identity lookup denied")
	groupCalls := 0
	p := unixPipeControl{
		rootReplaced: func() (bool, error) { return false, identityErr },
		killTree:     func() error { t.Fatal("unknown root identity reached tree kill"); return nil },
		killGroup:    func() error { groupCalls++; return nil },
	}
	if err := p.Kill(); !errors.Is(err, identityErr) || groupCalls != 0 {
		t.Fatalf("identity failure = %v, group calls=%d", err, groupCalls)
	}
	groupErr := errors.New("group termination denied")
	p.rootReplaced = func() (bool, error) { return false, nil }
	p.killTree = func() error { return nil }
	p.killGroup = func() error { return groupErr }
	if err := p.Kill(); !errors.Is(err, groupErr) {
		t.Fatalf("group failure = %v", err)
	}
	p.killGroup = func() error { return syscall.ESRCH }
	if err := p.Kill(); err != nil {
		t.Fatalf("already finished group = %v", err)
	}
}

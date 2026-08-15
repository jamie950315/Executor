//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsServiceHandlerReportsLifecycleAndCancelsRuntime(t *testing.T) {
	cancelled := make(chan struct{})
	handler := windowsServiceHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		return nil
	}}
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 3)
	result := make(chan uint32, 1)
	go func() {
		_, exitCode := handler.Execute(nil, requests, changes)
		result <- exitCode
	}()

	assertWindowsServiceStatus(t, changes, svc.StartPending)
	running := assertWindowsServiceStatus(t, changes, svc.Running)
	if running.Accepts&svc.AcceptStop == 0 || running.Accepts&svc.AcceptShutdown == 0 {
		t.Fatalf("running accepts = %v, want stop and shutdown", running.Accepts)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop, CurrentStatus: running}
	assertWindowsServiceStatus(t, changes, svc.StopPending)

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("service stop did not cancel runtime")
	}
	select {
	case exitCode := <-result:
		if exitCode != 0 {
			t.Fatalf("service exit code = %d, want 0", exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service handler did not exit")
	}
}

func assertWindowsServiceStatus(t *testing.T, changes <-chan svc.Status, want svc.State) svc.Status {
	t.Helper()
	select {
	case status := <-changes:
		if status.State != want {
			t.Fatalf("service state = %v, want %v", status.State, want)
		}
		return status
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for service state %v", want)
		return svc.Status{}
	}
}

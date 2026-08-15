//go:build windows

package main

import (
	"context"
	"time"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceStopTimeout = 15 * time.Second

func runManagedRuntime(ctx context.Context, serviceName string, run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return run(ctx)
	}
	return svc.Run(serviceName, windowsServiceHandler{parent: ctx, run: run})
}

type windowsServiceHandler struct {
	parent context.Context
	run    func(context.Context) error
}

func (h windowsServiceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending, WaitHint: 5000}
	parent := h.parent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()

	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	changes <- running
	for {
		select {
		case err := <-done:
			if err != nil {
				return false, 1
			}
			return false, 0
		case <-parent.Done():
			return h.stop(cancel, done, changes)
		case request, ok := <-requests:
			if !ok {
				return h.stop(cancel, done, changes)
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- running
			case svc.Stop, svc.Shutdown:
				return h.stop(cancel, done, changes)
			}
		}
	}
}

func (windowsServiceHandler) stop(cancel context.CancelFunc, done <-chan error, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StopPending, WaitHint: uint32(windowsServiceStopTimeout / time.Millisecond)}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			return false, 1
		}
		return false, 0
	case <-time.After(windowsServiceStopTimeout):
		return false, 1
	}
}

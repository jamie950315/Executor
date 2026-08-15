package lifecycle

import (
	"context"
	"errors"
)

type Controller interface {
	Quiesce(context.Context) error
	StopTunnel(context.Context) error
	KillSessions(context.Context) error
	StopDesktop(context.Context) error
	StopBroker(context.Context) error
	RevokeOAuth(context.Context) error
	RotateCredentials(context.Context) error
	StopAgent(context.Context) error
	StartBroker(context.Context) error
	StartDesktop(context.Context) error
	StartAgent(context.Context) error
	StartTunnel(context.Context) error
}

type Manager struct {
	controller Controller
}

func New(controller Controller) *Manager {
	return &Manager{controller: controller}
}

func (m *Manager) Kill(ctx context.Context) error {
	steps := []func(context.Context) error{
		m.controller.Quiesce,
		m.controller.StopTunnel,
		m.controller.KillSessions,
		m.controller.StopDesktop,
		m.controller.StopBroker,
		m.controller.RevokeOAuth,
		m.controller.RotateCredentials,
		m.controller.StopAgent,
	}
	var errs []error
	for _, step := range steps {
		if err := step(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Resume(ctx context.Context) error {
	steps := []func(context.Context) error{
		m.controller.StartBroker,
		m.controller.StartDesktop,
		m.controller.StartAgent,
		m.controller.StartTunnel,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

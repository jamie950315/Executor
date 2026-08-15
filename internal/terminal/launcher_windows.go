//go:build windows

package terminal

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type ptyLauncher interface {
	Start(ctx context.Context, spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error)
	Kill(cmd *exec.Cmd) error
}

type unavailableLauncher struct{}

func newPTYLauncher() ptyLauncher {
	return unavailableLauncher{}
}

func (unavailableLauncher) Start(ctx context.Context, spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	return nil, nil, nil, errors.New("windows PTY launcher is not implemented yet")
}

func (unavailableLauncher) Kill(cmd *exec.Cmd) error {
	return nil
}

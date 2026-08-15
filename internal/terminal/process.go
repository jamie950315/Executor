package terminal

import (
	"errors"
	"io"
)

const (
	defaultTerminalColumns = 120
	defaultTerminalRows    = 30
	MaxDimension           = 32767
)

var ErrResizeUnsupported = errors.New("terminal resize is unsupported on this platform")

type terminalProcess interface {
	io.Reader
	io.Writer
	PID() int
	Wait() error
	CloseInput() error
	Kill() error
	Signal(Signal) error
	Resize(columns, rows int) error
}

type ptyLauncher interface {
	Start(spec SessionSpec) (terminalProcess, error)
}

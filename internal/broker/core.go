package broker

import (
	"context"
	"errors"
	"io/fs"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
	"github.com/jamie950315/executor/internal/terminal"
)

var ErrUnknownPrivilege = errors.New("unknown privilege")

type Privilege string

const (
	PrivilegeOwner Privilege = "owner"
	PrivilegeAdmin Privilege = "admin"
)

type TerminalExecutor interface {
	Start(ctx context.Context, spec terminal.SessionSpec) (terminal.Session, error)
	Write(sessionID string, input []byte) error
	Read(sessionID string, cursor int64) (terminal.OutputChunk, error)
	List() []terminal.SessionInfo
	Close(sessionID string) error
	Kill(sessionID string) error
	KillAll() error
	Signal(sessionID string, signal terminal.Signal) error
}

type DesktopController interface {
	Screenshot(ctx context.Context, path string) error
	Windows(ctx context.Context) ([]desktop.Window, error)
	Accessibility(ctx context.Context) (desktop.AccessibilityTree, error)
	Mouse(ctx context.Context, action desktop.MouseAction) error
	Keyboard(ctx context.Context, action desktop.KeyboardAction) error
	App(ctx context.Context, action desktop.AppAction) error
}

type Core struct {
	owner   TerminalExecutor
	admin   TerminalExecutor
	files   filesystem.Service
	desktop DesktopController
}

func NewCore(owner TerminalExecutor, admin TerminalExecutor, files filesystem.Service, desktop DesktopController) *Core {
	return &Core{
		owner:   owner,
		admin:   admin,
		files:   files,
		desktop: desktop,
	}
}

func (c *Core) StartSession(ctx context.Context, privilege Privilege, spec terminal.SessionSpec) (terminal.Session, error) {
	executor, err := c.executor(privilege)
	if err != nil {
		return terminal.Session{}, err
	}
	return executor.Start(ctx, spec)
}

func (c *Core) WriteSession(sessionID string, input []byte, privilege Privilege) error {
	executor, err := c.executor(privilege)
	if err != nil {
		return err
	}
	return executor.Write(sessionID, input)
}

func (c *Core) ReadSession(sessionID string, cursor int64, privilege Privilege) (terminal.OutputChunk, error) {
	executor, err := c.executor(privilege)
	if err != nil {
		return terminal.OutputChunk{}, err
	}
	return executor.Read(sessionID, cursor)
}

func (c *Core) KillSession(sessionID string, privilege Privilege) error {
	executor, err := c.executor(privilege)
	if err != nil {
		return err
	}
	return executor.Kill(sessionID)
}

func (c *Core) ListSessions(privilege Privilege) ([]terminal.SessionInfo, error) {
	executor, err := c.executor(privilege)
	if err != nil {
		return nil, err
	}
	return executor.List(), nil
}

func (c *Core) CloseSession(sessionID string, privilege Privilege) error {
	executor, err := c.executor(privilege)
	if err != nil {
		return err
	}
	return executor.Close(sessionID)
}

func (c *Core) KillAllSessions(privilege Privilege) error {
	executor, err := c.executor(privilege)
	if err != nil {
		return err
	}
	return executor.KillAll()
}

func (c *Core) ReadFile(path string) ([]byte, error) {
	return c.files.ReadFile(path)
}

func (c *Core) List(path string) ([]filesystem.Entry, error) {
	return c.files.List(path)
}

func (c *Core) Glob(pattern string) ([]string, error) {
	return c.files.Glob(pattern)
}

func (c *Core) Stat(path string) (filesystem.FileInfo, error) {
	return c.files.Stat(path)
}

func (c *Core) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return c.files.WriteFile(path, data, perm)
}

func (c *Core) Move(src, dst string) error {
	return c.files.Move(src, dst)
}

func (c *Core) Delete(path string) error {
	return c.files.Delete(path)
}

func (c *Core) Screenshot(ctx context.Context, path string) error {
	return c.desktop.Screenshot(ctx, path)
}

func (c *Core) Windows(ctx context.Context) ([]desktop.Window, error) {
	return c.desktop.Windows(ctx)
}

func (c *Core) Accessibility(ctx context.Context) (desktop.AccessibilityTree, error) {
	return c.desktop.Accessibility(ctx)
}

func (c *Core) Mouse(ctx context.Context, action desktop.MouseAction) error {
	return c.desktop.Mouse(ctx, action)
}

func (c *Core) Keyboard(ctx context.Context, action desktop.KeyboardAction) error {
	return c.desktop.Keyboard(ctx, action)
}

func (c *Core) App(ctx context.Context, action desktop.AppAction) error {
	return c.desktop.App(ctx, action)
}

func (c *Core) executor(privilege Privilege) (TerminalExecutor, error) {
	switch privilege {
	case PrivilegeOwner:
		return c.owner, nil
	case PrivilegeAdmin:
		return c.admin, nil
	default:
		return nil, ErrUnknownPrivilege
	}
}

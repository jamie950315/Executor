package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"
)

const (
	defaultOutputBufferSize = 8 << 20
	sessionShutdownTimeout  = 3 * time.Second
)

type SessionSpec struct {
	Command []string
	Dir     string
	Env     map[string]string
}

type Session struct {
	ID      string
	PID     int
	Command []string
	Dir     string
}

type SessionInfo struct {
	Session Session
	Running bool
}

type OutputChunk struct {
	Data        []byte
	StartCursor int64
	NextCursor  int64
	Running     bool
	Truncated   bool
}

type sessionState struct {
	meta          Session
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdinClose    sync.Once
	mu            sync.RWMutex
	output        *outputBuffer
	running       bool
	waitErr       error
	done          chan struct{}
	doneCloseOnce sync.Once
}

type Manager struct {
	launcher ptyLauncher
	mu       sync.RWMutex
	sessions map[string]*sessionState
}

func NewManager() *Manager {
	return &Manager{
		launcher: newPTYLauncher(),
		sessions: make(map[string]*sessionState),
	}
}

func (m *Manager) Start(ctx context.Context, spec SessionSpec) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if len(spec.Command) == 0 {
		spec.Command = []string{defaultShell()}
	}

	cmd, stdin, stdout, err := m.launcher.Start(spec)
	if err != nil {
		return Session{}, err
	}

	id, err := newSessionID()
	if err != nil {
		_ = m.launcher.Kill(cmd)
		return Session{}, err
	}
	state := &sessionState{
		meta: Session{
			ID:      id,
			PID:     cmd.Process.Pid,
			Command: append([]string(nil), spec.Command...),
			Dir:     spec.Dir,
		},
		cmd:     cmd,
		stdin:   stdin,
		output:  newOutputBuffer(defaultOutputBufferSize),
		running: true,
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = state
	m.mu.Unlock()

	go state.capture(stdout)
	go m.awaitExit(state)

	return state.meta, nil
}

func (m *Manager) Write(sessionID string, input []byte) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.running {
		return errors.New("session is not running")
	}
	_, err = state.stdin.Write(input)
	return err
}

func (m *Manager) Read(sessionID string, cursor int64) (OutputChunk, error) {
	state, err := m.session(sessionID)
	if err != nil {
		return OutputChunk{}, err
	}

	state.mu.RLock()
	defer state.mu.RUnlock()
	chunk := state.output.Read(cursor)
	chunk.Running = state.running
	return chunk, nil
}

func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(m.sessions))
	for _, state := range m.sessions {
		state.mu.RLock()
		sessions = append(sessions, SessionInfo{
			Session: state.meta,
			Running: state.running,
		})
		state.mu.RUnlock()
	}
	slices.SortFunc(sessions, func(a, b SessionInfo) int {
		switch {
		case a.Session.ID < b.Session.ID:
			return -1
		case a.Session.ID > b.Session.ID:
			return 1
		default:
			return 0
		}
	})
	return sessions
}

func (m *Manager) Close(sessionID string) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}

	state.closeInput()
	if !waitForDone(state.done, sessionShutdownTimeout) {
		if killErr := m.launcher.Kill(state.cmd); killErr != nil {
			return killErr
		}
		_ = waitForDone(state.done, sessionShutdownTimeout)
	}
	m.deleteSession(sessionID)
	return nil
}

func (m *Manager) Kill(sessionID string) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	if err := m.launcher.Kill(state.cmd); err != nil {
		return err
	}
	_ = waitForDone(state.done, sessionShutdownTimeout)
	m.deleteSession(sessionID)
	return nil
}

func (m *Manager) KillAll() error {
	sessions := m.List()
	var errs []error
	for _, session := range sessions {
		if err := m.Kill(session.Session.ID); err != nil && !errors.Is(err, ErrSessionNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var ErrSessionNotFound = errors.New("session not found")

func (m *Manager) session(sessionID string) (*sessionState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, sessionID)
	}
	return state, nil
}

func (m *Manager) deleteSession(sessionID string) {
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
}

func (m *Manager) awaitExit(state *sessionState) {
	err := state.cmd.Wait()
	state.mu.Lock()
	state.running = false
	state.waitErr = err
	state.closeInput()
	state.mu.Unlock()
	state.doneCloseOnce.Do(func() {
		close(state.done)
	})
}

func (s *sessionState) capture(stdout io.ReadCloser) {
	defer stdout.Close()
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.output.Append(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *sessionState) closeInput() {
	s.stdinClose.Do(func() {
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
	})
}

func waitForDone(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func newSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

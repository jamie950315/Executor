package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	Columns int
	Rows    int
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

type Signal string

const (
	SignalInterrupt Signal = "interrupt"
	SignalTerminate Signal = "terminate"
	SignalKill      Signal = "kill"
)

type OutputChunk struct {
	Data        []byte
	StartCursor int64
	NextCursor  int64
	Running     bool
	Truncated   bool
}

type sessionState struct {
	meta          Session
	process       terminalProcess
	inputMu       sync.Mutex
	stdinClose    sync.Once
	mu            sync.RWMutex
	output        *outputBuffer
	running       bool
	waitErr       error
	done          chan struct{}
	captureDone   chan struct{}
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
	if spec.Columns < 0 || spec.Rows < 0 || spec.Columns > MaxDimension || spec.Rows > MaxDimension {
		return Session{}, fmt.Errorf("terminal dimensions must be between 1 and %d when provided", MaxDimension)
	}

	process, err := m.launcher.Start(spec)
	if err != nil {
		return Session{}, err
	}

	id, err := newSessionID()
	if err != nil {
		_ = process.Kill()
		return Session{}, err
	}
	state := &sessionState{
		meta: Session{
			ID:      id,
			PID:     process.PID(),
			Command: append([]string(nil), spec.Command...),
			Dir:     spec.Dir,
		},
		process:     process,
		output:      newOutputBuffer(defaultOutputBufferSize),
		running:     true,
		done:        make(chan struct{}),
		captureDone: make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = state
	m.mu.Unlock()

	go state.capture(process)
	go m.awaitExit(state)

	return state.meta, nil
}

func (m *Manager) Write(sessionID string, input []byte) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	state.mu.RLock()
	running := state.running
	process := state.process
	state.mu.RUnlock()
	if !running {
		return errors.New("session is not running")
	}
	state.inputMu.Lock()
	defer state.inputMu.Unlock()
	_, err = process.Write(input)
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
		if killErr := state.process.Kill(); killErr != nil {
			return killErr
		}
		if !waitForDone(state.done, sessionShutdownTimeout) {
			return errors.New("timed out waiting for terminal shutdown; session retained for retry")
		}
	}
	m.deleteSession(sessionID)
	return nil
}

func (m *Manager) Kill(sessionID string) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	if err := state.process.Kill(); err != nil {
		return err
	}
	if !waitForDone(state.done, sessionShutdownTimeout) {
		return errors.New("timed out waiting for terminal shutdown; session retained for retry")
	}
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

func (m *Manager) Signal(sessionID string, signal Signal) error {
	if signal != SignalInterrupt && signal != SignalTerminate && signal != SignalKill {
		return fmt.Errorf("unsupported terminal signal %q", signal)
	}
	if signal == SignalInterrupt {
		return m.Write(sessionID, []byte{3})
	}
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	state.mu.RLock()
	running := state.running
	process := state.process
	state.mu.RUnlock()
	if !running {
		return errors.New("session is not running")
	}
	return process.Signal(signal)
}

func (m *Manager) Resize(sessionID string, columns, rows int) error {
	if columns < 1 || rows < 1 || columns > MaxDimension || rows > MaxDimension {
		return fmt.Errorf("terminal dimensions must be between 1 and %d", MaxDimension)
	}
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	state.mu.RLock()
	running := state.running
	process := state.process
	state.mu.RUnlock()
	if !running {
		return errors.New("session is not running")
	}
	return process.Resize(columns, rows)
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
	err := state.process.Wait()
	state.closeInput()
	<-state.captureDone
	state.mu.Lock()
	state.running = false
	state.waitErr = err
	state.mu.Unlock()
	state.doneCloseOnce.Do(func() {
		close(state.done)
	})
}

func (s *sessionState) capture(output terminalProcess) {
	defer close(s.captureDone)
	buf := make([]byte, 4096)
	for {
		n, err := output.Read(buf)
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
		if s.process != nil {
			_ = s.process.CloseInput()
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

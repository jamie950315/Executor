package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
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

type OutputChunk struct {
	Data       []byte
	NextCursor int64
	Running    bool
}

type sessionState struct {
	meta   Session
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.RWMutex
	output bytes.Buffer
	running bool
}

type Manager struct {
	launcher ptyLauncher
	seq      atomic.Uint64
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
	if len(spec.Command) == 0 {
		spec.Command = []string{defaultShell()}
	}

	cmd, stdin, stdout, err := m.launcher.Start(ctx, spec)
	if err != nil {
		return Session{}, err
	}

	id := strconv.FormatUint(m.seq.Add(1), 10)
	state := &sessionState{
		meta: Session{
			ID:      id,
			PID:     cmd.Process.Pid,
			Command: append([]string(nil), spec.Command...),
			Dir:     spec.Dir,
		},
		cmd:     cmd,
		stdin:   stdin,
		running: true,
	}

	m.mu.Lock()
	m.sessions[id] = state
	m.mu.Unlock()

	go state.capture(stdout)
	go m.awaitExit(id, state)

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
	data := state.output.Bytes()
	if cursor < 0 {
		cursor = 0
	}
	if cursor > int64(len(data)) {
		cursor = int64(len(data))
	}

	chunk := append([]byte(nil), data[cursor:]...)
	return OutputChunk{
		Data:       chunk,
		NextCursor: int64(len(data)),
		Running:    state.running,
	}, nil
}

func (m *Manager) Kill(sessionID string) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	return m.launcher.Kill(state.cmd)
}

func (m *Manager) session(sessionID string) (*sessionState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	return state, nil
}

func (m *Manager) awaitExit(sessionID string, state *sessionState) {
	_ = state.cmd.Wait()
	state.mu.Lock()
	state.running = false
	_ = state.stdin.Close()
	state.mu.Unlock()
}

func (s *sessionState) capture(stdout io.ReadCloser) {
	defer stdout.Close()
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.output.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func defaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

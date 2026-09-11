package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	TTY     *bool `json:"tty,omitempty"`
}

type Session struct {
	ID      string
	PID     int
	Command []string
	Dir     string
	Mode    SessionMode `json:"mode"`
	TTY     bool        `json:"tty"`
}

type SessionInfo struct {
	Session        Session
	Running        bool
	SessionRunning bool  `json:"sessionRunning"`
	CommandRunning *bool `json:"commandRunning"`
	ExitCode       *int  `json:"exitCode"`
}

type Signal string

const (
	SignalInterrupt Signal = "interrupt"
	SignalTerminate Signal = "terminate"
	SignalKill      Signal = "kill"
)

type OutputChunk struct {
	Data           []byte
	StartCursor    int64
	NextCursor     int64
	Running        bool
	Truncated      bool
	Encoding       string      `json:"encoding"`
	ReturnedBytes  int         `json:"returnedBytes"`
	HasMore        bool        `json:"hasMore"`
	Mode           SessionMode `json:"mode"`
	SessionRunning bool        `json:"sessionRunning"`
	CommandRunning *bool       `json:"commandRunning"`
	ExitCode       *int        `json:"exitCode"`
	TTY            bool        `json:"tty"`
	Stream         string      `json:"stream"`
}

type sessionState struct {
	meta          Session
	process       terminalProcess
	inputMu       sync.Mutex
	stdinClose    sync.Once
	stdinErr      error
	mu            sync.RWMutex
	output        *outputBuffer
	stdout        *outputBuffer
	stderr        *outputBuffer
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
	if err := ValidateSessionSpec(spec); err != nil {
		return Session{}, err
	}
	if err := validateWorkingDirectory(spec.Dir); err != nil {
		return Session{}, err
	}
	mode := ModeCommand
	if len(spec.Command) == 0 {
		mode = ModeInteractive
		spec.Command = []string{defaultShell()}
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
			Mode:    mode,
			TTY:     usesTTY(spec),
		},
		process:     process,
		output:      newOutputBuffer(defaultOutputBufferSize),
		running:     true,
		done:        make(chan struct{}),
		captureDone: make(chan struct{}),
	}
	if !state.meta.TTY {
		state.stdout = newOutputBuffer(defaultOutputBufferSize / 2)
		state.stderr = newOutputBuffer(defaultOutputBufferSize / 2)
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
	written, err := process.Write(input)
	if err == nil && written != len(input) {
		return io.ErrShortWrite
	}
	return err
}

func (m *Manager) Read(sessionID string, cursor int64) (OutputChunk, error) {
	return m.read(sessionID, cursor, 0)
}

// ReadLimited returns at most limit raw bytes. A zero limit selects the default;
// Read retains the complete-tail behavior of existing in-process callers.
func (m *Manager) ReadLimited(sessionID string, cursor int64, limit int) (OutputChunk, error) {
	if cursor < 0 {
		return OutputChunk{}, errors.New("terminal cursor must be a non-negative byte offset")
	}
	limit, err := OutputLimit(limit)
	if err != nil {
		return OutputChunk{}, err
	}
	return m.read(sessionID, cursor, limit)
}

func (m *Manager) read(sessionID string, cursor int64, limit int) (OutputChunk, error) {
	return m.readStream(sessionID, cursor, limit, "combined")
}

// ReadStream uses an independent raw-byte cursor for each retained stream.
// PTY output is inherently merged; separate streams require tty=false.
func (m *Manager) ReadStream(sessionID string, cursor int64, limit int, stream string) (OutputChunk, error) {
	if cursor < 0 {
		return OutputChunk{}, errors.New("terminal cursor must be non-negative bytes")
	}
	limit, err := OutputLimit(limit)
	if err != nil {
		return OutputChunk{}, err
	}
	return m.readStream(sessionID, cursor, limit, stream)
}

func (m *Manager) readStream(sessionID string, cursor int64, limit int, stream string) (OutputChunk, error) {
	if stream == "" {
		stream = "combined"
	}
	if stream != "combined" && stream != "stdout" && stream != "stderr" {
		return OutputChunk{}, errors.New("stream must be combined, stdout or stderr")
	}
	state, err := m.session(sessionID)
	if err != nil {
		return OutputChunk{}, err
	}

	state.mu.RLock()
	defer state.mu.RUnlock()
	buffer := state.output
	if stream != "combined" {
		if state.meta.TTY || state.stdout == nil || state.stderr == nil {
			return OutputChunk{}, errors.New("STREAM_UNAVAILABLE: separate stdout/stderr require tty=false")
		}
		buffer = state.stdout
		if stream == "stderr" {
			buffer = state.stderr
		}
	}
	chunk := buffer.read(cursor, limit)
	chunk.Running = state.running
	chunk.SessionRunning = state.running
	chunk.Mode, chunk.CommandRunning, chunk.ExitCode = state.lifecycle()
	chunk.TTY, chunk.Stream = state.meta.TTY, stream
	return chunk, nil
}

func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(m.sessions))
	for _, state := range m.sessions {
		state.mu.RLock()
		_, commandRunning, exitCode := state.lifecycle()
		sessions = append(sessions, SessionInfo{
			Session:        state.meta,
			Running:        state.running,
			SessionRunning: state.running,
			CommandRunning: commandRunning,
			ExitCode:       exitCode,
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
	state.mu.RLock()
	running := state.running
	state.mu.RUnlock()
	// Completed sessions are retained for reading; their PIDs may be reused.
	if !running {
		m.deleteSession(sessionID)
		return nil
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
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	state.mu.RLock()
	running := state.running
	process := state.process
	tty := state.meta.TTY
	state.mu.RUnlock()
	if !running {
		return errors.New("session is not running")
	}
	if signal == SignalInterrupt && tty {
		return m.Write(sessionID, []byte{3})
	}
	return process.Signal(signal)
}

// CloseStdin sends EOF to a pipe-mode process and retains the session/output.
func (m *Manager) CloseStdin(sessionID string) error {
	state, err := m.session(sessionID)
	if err != nil {
		return err
	}
	if state.meta.TTY {
		return errors.New("STDIN_EOF_UNAVAILABLE: close_stdin requires tty=false")
	}
	return state.closeInput()
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
	if separate, ok := output.(interface{ StderrReader() io.Reader }); ok && s.stderr != nil {
		done := make(chan struct{})
		go func() { defer close(done); s.captureReader(separate.StderrReader(), s.stderr) }()
		s.captureReader(output, s.stdout)
		<-done
		return
	}
	s.captureReader(output, nil)
}

func (s *sessionState) captureReader(output io.Reader, separate *outputBuffer) {
	buf := make([]byte, 4096)
	for {
		n, err := output.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.output.Append(buf[:n])
			if separate != nil {
				separate.Append(buf[:n])
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *sessionState) closeInput() error {
	s.stdinClose.Do(func() {
		if s.process != nil {
			s.stdinErr = s.process.CloseInput()
		}
	})
	return s.stdinErr
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

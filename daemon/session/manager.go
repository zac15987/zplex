package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrSessionNotFound is returned when a requested session ID does not exist.
var ErrSessionNotFound = errors.New("session not found")

// SessionManager owns all active sessions and provides CRUD operations.
// It is safe for concurrent use.
type SessionManager struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	shell      string // default shell from config
	bufferSize int    // ring buffer capacity from config
}

// NewSessionManager creates a SessionManager configured with the given
// default shell and per-session ring buffer size.
func NewSessionManager(defaultShell string, bufferSize int) *SessionManager {
	slog.Info("manager: initialized",
		slog.String("shell", defaultShell),
		slog.Int("buffer_size", bufferSize),
	)
	return &SessionManager{
		sessions:   make(map[string]*Session),
		shell:      defaultShell,
		bufferSize: bufferSize,
	}
}

// Create spawns a new PTY-backed session with the given title and adds it
// to the manager's session map. The session ID is generated randomly.
func (m *SessionManager) Create(title string) (*Session, error) {
	slog.Info("manager.Create: creating session", slog.String("title", title))

	id := "s-" + generateID()

	sess, err := NewSession(id, title, m.shell, nil, "", nil, "", m.bufferSize)
	if err != nil {
		slog.Error("manager.Create: failed to create session",
			slog.String("title", title),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	slog.Info("manager.Create: session created", slog.String("session_id", id))
	return sess, nil
}

// CreateOptions holds parameters for creating a new session.
type CreateOptions struct {
	Shell string
	Title string
	Cols  int
	Rows  int
	Args  []string
	Env   []string
	Cwd   string
	Kind  string
	// Source, ProjectID, IssueID, Role are immutable agent-identity metadata
	// set at creation time and not modified afterward.
	Source    string
	ProjectID string
	IssueID   string
	Role      string
	// AgentState is the initial agent loop state: "" | "active" | "waiting" | "done".
	AgentState string
}

// CreateSession spawns a new PTY-backed session with the given options.
// If Shell is empty, the manager's default shell is used.
// If Cols/Rows are non-zero, the PTY is resized after creation.
func (m *SessionManager) CreateSession(opts CreateOptions) (*Session, error) {
	slog.Info("manager.CreateSession: creating session",
		slog.String("title", opts.Title),
		slog.String("shell", opts.Shell),
		slog.Int("cols", opts.Cols),
		slog.Int("rows", opts.Rows),
		slog.String("kind", opts.Kind),
		slog.String("source", opts.Source),
		slog.String("role", opts.Role),
		slog.String("agent_state", opts.AgentState),
	)

	shell := opts.Shell
	if shell == "" {
		shell = m.shell
	}

	id := "s-" + generateID()

	sess, err := NewSession(id, opts.Title, shell, opts.Args, opts.Cwd, opts.Env, opts.Kind, m.bufferSize)
	if err != nil {
		slog.Error("manager.CreateSession: failed to create session",
			slog.String("title", opts.Title),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	// Apply agent-identity metadata. The session is not yet shared (not in the
	// manager map), so these assignments are safe without holding the lock.
	sess.Source = opts.Source
	sess.ProjectID = opts.ProjectID
	sess.IssueID = opts.IssueID
	sess.Role = opts.Role
	sess.AgentState = opts.AgentState

	// Resize if cols/rows specified.
	if opts.Cols > 0 && opts.Rows > 0 {
		if err := sess.Resize(opts.Cols, opts.Rows); err != nil {
			slog.Warn("manager.CreateSession: failed to resize session",
				slog.String("session_id", id),
				slog.String("error", err.Error()),
			)
			// Non-fatal: session is still usable at default size.
		}
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	slog.Info("manager.CreateSession: session created", slog.String("session_id", id))
	return sess, nil
}

// Count returns the number of active sessions.
func (m *SessionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// Get returns the session with the given ID. If no session exists for that ID,
// it returns ErrSessionNotFound.
func (m *SessionManager) Get(id string) (*Session, error) {
	slog.Info("manager.Get: looking up session", slog.String("session_id", id))

	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		slog.Warn("manager.Get: session not found", slog.String("session_id", id))
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return sess, nil
}

// List returns a snapshot of metadata for every session the manager owns.
func (m *SessionManager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	slog.Info("manager.List: listing sessions", slog.Int("count", len(m.sessions)))

	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, sess := range m.sessions {
		infos = append(infos, sess.Info())
	}
	return infos
}

// Kill removes a session from the manager and closes it. If the session ID
// does not exist, it returns ErrSessionNotFound.
func (m *SessionManager) Kill(id string) error {
	slog.Info("manager.Kill: killing session", slog.String("session_id", id))

	m.mu.Lock()
	sess, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		slog.Warn("manager.Kill: session not found", slog.String("session_id", id))
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	if err := sess.Close(); err != nil {
		slog.Warn("manager.Kill: error closing session",
			slog.String("session_id", id),
			slog.String("error", err.Error()),
		)
		return err
	}

	slog.Info("manager.Kill: session killed", slog.String("session_id", id))
	return nil
}

// Shutdown closes every session the manager owns and clears the session map.
// This is intended for graceful daemon shutdown.
func (m *SessionManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	slog.Info("manager.Shutdown: shutting down all sessions", slog.Int("count", len(m.sessions)))

	for id, sess := range m.sessions {
		if err := sess.Close(); err != nil {
			slog.Warn("manager.Shutdown: error closing session",
				slog.String("session_id", id),
				slog.String("error", err.Error()),
			)
		}
	}

	// Clear the map so the manager holds no stale references.
	m.sessions = make(map[string]*Session)

	slog.Info("manager.Shutdown: all sessions closed")
}

// FixedSession returns the single Kind=="fixed" session if one exists.
// The bool is false when no fixed session is currently registered.
func (m *SessionManager) FixedSession() (*Session, bool) {
	slog.Info("manager.FixedSession: looking up fixed session")

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, sess := range m.sessions {
		if sess.Kind == "fixed" {
			slog.Info("manager.FixedSession: fixed session found",
				slog.String("session_id", sess.ID),
			)
			return sess, true
		}
	}

	slog.Info("manager.FixedSession: no fixed session found")
	return nil, false
}

// generateID produces a 16-character hex string from 8 random bytes.
// In the extremely unlikely event that crypto/rand fails, it falls back
// to a nanosecond timestamp.
func generateID() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

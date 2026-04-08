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

	sess, err := NewSession(id, title, m.shell, m.bufferSize)
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

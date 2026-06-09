// Package session manages PTY-backed terminal sessions for the zplex daemon.
// Each session owns a pseudo-terminal, a subprocess, and a ring buffer that
// stores recent output for reconnect replay.
package session

import (
	"io"
	"log/slog"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// SessionInfo is a JSON-serializable snapshot of session metadata.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	PID       int       `json:"pid"`
	ExitCode  int       `json:"exit_code"`
	Kind      string    `json:"kind"`
	// Identity fields — immutable, set at creation.
	Source    string `json:"source"`
	ProjectID string `json:"project_id"`
	IssueID   string `json:"issue_id"`
	Role      string `json:"role"`
	// AgentState is the mutable agent loop state: "" | "active" | "waiting" | "done".
	AgentState string `json:"agent_state"`
}

// Session represents a single PTY-backed terminal session.
// It owns a pseudo-terminal, a running command, and a ring buffer that
// accumulates recent output for reconnect replay.
type Session struct {
	ID        string
	Title     string
	Status    string // "running" or "exited"
	CreatedAt time.Time
	PID       int
	ExitCode  int
	Kind      string // "" = normal/agent session; "fixed" = zpit fixed panel
	// Identity fields — immutable, set at creation.
	Source    string
	ProjectID string
	IssueID   string
	Role      string
	// AgentState is the mutable agent loop state: "" | "active" | "waiting" | "done".
	AgentState string

	pty      pty.Pty     // PTY handle (ConPTY on Windows, /dev/ptmx on Unix)
	cmd      *pty.Cmd    // the running subprocess
	ringBuf  *ringBuffer // circular buffer for replay
	mu       sync.Mutex  // protects Status, ExitCode, ringBuf writes, subscribers
	done     chan struct{}
	closePTY sync.Once // ensures PTY is closed exactly once

	// subscribers receive copies of live PTY output from the readLoop.
	// Each subscriber is a buffered channel; slow consumers are skipped
	// to avoid blocking the readLoop.
	subscribers []chan []byte
}

// readBufSize is the size of the temporary buffer used when reading PTY output
// in the background goroutine.
const readBufSize = 8192

// NewSession spawns a new PTY-backed shell session.
// The caller provides a unique id, a human-readable title, the shell executable
// to run, optional extra args, an optional working directory (cwd), optional
// environment variables (env), a kind tag ("" for normal/agent, "fixed" for the
// zpit fixed panel), and the ring-buffer capacity in bytes.
func NewSession(id, title, shell string, args []string, cwd string, env []string, kind string, bufferSize int) (*Session, error) {
	slog.Info("session.NewSession: creating session",
		slog.String("session_id", id),
		slog.String("title", title),
		slog.String("shell", shell),
		slog.String("kind", kind),
		slog.Int("args_count", len(args)),
		slog.Int("buffer_size", bufferSize),
	)

	ptmx, err := pty.New()
	if err != nil {
		slog.Error("session.NewSession: failed to create PTY",
			slog.String("session_id", id),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	cmd := ptmx.Command(shell, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if env != nil {
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		slog.Error("session.NewSession: failed to start command",
			slog.String("session_id", id),
			slog.String("shell", shell),
			slog.String("error", err.Error()),
		)
		ptmx.Close()
		return nil, err
	}

	pid := cmd.Process.Pid

	s := &Session{
		ID:        id,
		Title:     title,
		Status:    "running",
		CreatedAt: time.Now(),
		PID:       pid,
		Kind:      kind,
		pty:       ptmx,
		cmd:       cmd,
		ringBuf:   newRingBuffer(bufferSize),
		done:      make(chan struct{}),
	}

	go s.readLoop()

	slog.Info("session.NewSession: session created",
		slog.String("session_id", id),
		slog.Int("pid", pid),
	)
	return s, nil
}

// readLoop continuously reads PTY output, writes it into the ring buffer,
// and waits for the subprocess to exit.
//
// On Windows ConPTY, pty.Read() does not return EOF when the child process
// exits — it blocks indefinitely. To handle this, we run cmd.Wait() in a
// separate goroutine. When the process exits, that goroutine closes the PTY,
// which unblocks the Read call with an error so the loop can terminate.
func (s *Session) readLoop() {
	// waitDone is closed once cmd.Wait() completes and the exit code is captured.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		waitErr := s.cmd.Wait()
		exitCode := 0
		if s.cmd.ProcessState != nil {
			exitCode = s.cmd.ProcessState.ExitCode()
		} else if waitErr != nil {
			exitCode = -1
		}

		s.mu.Lock()
		s.Status = "exited"
		s.ExitCode = exitCode
		s.mu.Unlock()

		slog.Info("session.readLoop: process exited",
			slog.String("session_id", s.ID),
			slog.Int("exit_code", exitCode),
		)

		// Close the PTY to unblock any pending Read call.
		// Uses sync.Once so it's safe if Close() also triggers this.
		s.closePTY.Do(func() { s.pty.Close() })
	}()

	buf := make([]byte, readBufSize)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			// Make a copy for subscribers before taking the lock so we
			// can send without holding the mutex longer than needed.
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.Lock()
			s.ringBuf.Write(data)
			for _, sub := range s.subscribers {
				select {
				case sub <- data:
				default:
					// Subscriber too slow — drop this chunk to avoid
					// blocking the readLoop.
					slog.Warn("session.readLoop: dropped output for slow subscriber",
						slog.String("session_id", s.ID),
					)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				slog.Debug("session.readLoop: PTY read ended",
					slog.String("session_id", s.ID),
					slog.String("error", err.Error()),
				)
			}
			break
		}
	}

	// Wait for the cmd.Wait goroutine to finish capturing exit state.
	<-waitDone

	// Close all subscriber channels so that consumers (e.g., WebSocket
	// writer goroutines using range) terminate cleanly.
	s.mu.Lock()
	for _, sub := range s.subscribers {
		close(sub)
	}
	s.subscribers = nil
	s.mu.Unlock()

	close(s.done)

	slog.Info("session.readLoop: session fully stopped",
		slog.String("session_id", s.ID),
	)
}

// subscriberBufSize is the channel buffer size for output subscribers.
// A generous buffer prevents the readLoop from dropping data for
// subscribers that are briefly slow.
const subscriberBufSize = 256

// Subscribe returns a channel that receives copies of live PTY output.
// The caller must call Unsubscribe when done to avoid resource leaks.
// Each message on the channel is an independent byte slice that the
// subscriber owns (safe to retain).
func (s *Session) Subscribe() chan []byte {
	ch := make(chan []byte, subscriberBufSize)
	s.mu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.mu.Unlock()
	slog.Info("session.Subscribe: subscriber added",
		slog.String("session_id", s.ID),
	)
	return ch
}

// Unsubscribe removes a subscriber channel and closes it. After this call
// the channel will receive no further messages. If the session has already
// exited (readLoop closed all subscribers), this is a safe no-op.
func (s *Session) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	for i, sub := range s.subscribers {
		if sub == ch {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			close(ch)
			s.mu.Unlock()
			slog.Info("session.Unsubscribe: subscriber removed",
				slog.String("session_id", s.ID),
			)
			return
		}
	}
	s.mu.Unlock()
	// Channel was already closed by readLoop on session exit — nothing to do.
	slog.Info("session.Unsubscribe: subscriber already removed (session exited)",
		slog.String("session_id", s.ID),
	)
}

// Read reads PTY output directly. This is a low-level interface; prefer
// Subscribe for WebSocket consumers that need live output without competing
// with the background readLoop.
func (s *Session) Read(p []byte) (int, error) {
	return s.pty.Read(p)
}

// Write sends data to the PTY's stdin, which is forwarded to the subprocess.
func (s *Session) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

// Close kills the subprocess (if still running), closes the PTY, and waits
// for the background read goroutine to finish.
func (s *Session) Close() error {
	slog.Info("session.Close: closing session", slog.String("session_id", s.ID))

	s.mu.Lock()
	isRunning := s.Status == "running"
	s.mu.Unlock()

	if isRunning {
		if err := s.cmd.Process.Kill(); err != nil {
			slog.Warn("session.Close: failed to kill process",
				slog.String("session_id", s.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	// Close the PTY. The readLoop's Wait goroutine may have already closed
	// it after the process exited naturally; sync.Once ensures no double close.
	s.closePTY.Do(func() { s.pty.Close() })

	// Wait for the readLoop goroutine to finish.
	<-s.done

	slog.Info("session.Close: session closed", slog.String("session_id", s.ID))
	return nil
}

// Done returns a channel that is closed when the session's subprocess exits.
// External code can select on this to react to session termination.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Info returns a JSON-serializable snapshot of the session's metadata.
// The snapshot is taken under the session's lock to ensure consistency.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID:         s.ID,
		Title:      s.Title,
		Status:     s.Status,
		CreatedAt:  s.CreatedAt,
		PID:        s.PID,
		ExitCode:   s.ExitCode,
		Kind:       s.Kind,
		Source:     s.Source,
		ProjectID:  s.ProjectID,
		IssueID:    s.IssueID,
		Role:       s.Role,
		AgentState: s.AgentState,
	}
}

// Resize changes the PTY window size. It delegates to the underlying PTY's
// Resize method. This is called when the frontend sends a resize message.
func (s *Session) Resize(cols, rows int) error {
	slog.Info("session.Resize: resizing PTY",
		slog.String("session_id", s.ID),
		slog.Int("cols", cols),
		slog.Int("rows", rows),
	)
	return s.pty.Resize(cols, rows)
}

// UpdateMeta updates the session's mutable metadata fields.
// Empty strings are ignored (only non-empty values are applied).
// agentState accepts "" | "active" | "waiting" | "done"; value validation
// is performed at the API layer, not here.
func (s *Session) UpdateMeta(title, status, agentState string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if title != "" {
		s.Title = title
	}
	if status != "" {
		s.Status = status
	}
	if agentState != "" {
		s.AgentState = agentState
	}
	slog.Info("session.UpdateMeta: metadata updated",
		slog.String("session_id", s.ID),
		slog.String("title", s.Title),
		slog.String("status", s.Status),
		slog.String("agent_state", s.AgentState),
	)
}

// BufferData returns a copy of all data currently stored in the ring buffer,
// ordered from oldest to newest. This is used for reconnect replay.
func (s *Session) BufferData() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ringBuf.ReadAll()
}

// ---------------------------------------------------------------------------
// ringBuffer — a fixed-capacity circular buffer that implements io.Writer.
// When the buffer is full, new writes overwrite the oldest data.
// ---------------------------------------------------------------------------

// ringBuffer is a fixed-size circular byte buffer. It stores the most recent
// N bytes of data, silently discarding older content when capacity is exceeded.
type ringBuffer struct {
	buf  []byte // backing storage
	size int    // capacity (len(buf))
	w    int    // next write position
	full bool   // true once the buffer has wrapped at least once
}

// Compile-time check that ringBuffer implements io.Writer.
var _ io.Writer = (*ringBuffer)(nil)

// newRingBuffer allocates a ring buffer with the given capacity.
// A capacity of zero or negative is clamped to 1 to avoid panics.
func newRingBuffer(size int) *ringBuffer {
	if size <= 0 {
		size = 1
	}
	return &ringBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write appends p to the ring buffer. If p is larger than the buffer's
// capacity, only the last `size` bytes of p are retained. Write always
// returns len(p), nil — it never fails.
func (rb *ringBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}

	// If the incoming data is larger than the entire buffer, only keep
	// the trailing portion that fits.
	if n >= rb.size {
		copy(rb.buf, p[n-rb.size:])
		rb.w = 0
		rb.full = true
		return n, nil
	}

	// How many bytes fit before we wrap?
	remaining := rb.size - rb.w
	if n <= remaining {
		// Fits without wrapping.
		copy(rb.buf[rb.w:], p)
		rb.w += n
		if rb.w == rb.size {
			rb.w = 0
			rb.full = true
		}
	} else {
		// Split write: fill to end, then wrap to beginning.
		copy(rb.buf[rb.w:], p[:remaining])
		copy(rb.buf, p[remaining:])
		rb.w = n - remaining
		rb.full = true
	}

	return n, nil
}

// ReadAll returns a copy of all buffered data, ordered oldest to newest.
// If the buffer has never wrapped, this is buf[0:w].
// If wrapped, the result is buf[w:] + buf[:w] (oldest portion first).
func (rb *ringBuffer) ReadAll() []byte {
	if !rb.full {
		out := make([]byte, rb.w)
		copy(out, rb.buf[:rb.w])
		return out
	}

	out := make([]byte, rb.size)
	// Oldest data starts at the write position.
	firstLen := rb.size - rb.w
	copy(out, rb.buf[rb.w:])
	copy(out[firstLen:], rb.buf[:rb.w])
	return out
}

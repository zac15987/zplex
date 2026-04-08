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

	pty     pty.Pty     // PTY handle (ConPTY on Windows, /dev/ptmx on Unix)
	cmd     *pty.Cmd    // the running subprocess
	ringBuf *ringBuffer // circular buffer for replay
	mu      sync.Mutex  // protects Status, ExitCode, ringBuf writes
	done    chan struct{}
}

// readBufSize is the size of the temporary buffer used when reading PTY output
// in the background goroutine.
const readBufSize = 8192

// NewSession spawns a new PTY-backed shell session.
// The caller provides a unique id, a human-readable title, the shell executable
// to run, and the ring-buffer capacity in bytes.
func NewSession(id, title, shell string, bufferSize int) (*Session, error) {
	slog.Info("session.NewSession: creating session",
		slog.String("session_id", id),
		slog.String("title", title),
		slog.String("shell", shell),
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

	cmd := ptmx.Command(shell)
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
func (s *Session) readLoop() {
	buf := make([]byte, readBufSize)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.ringBuf.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			// EOF or read error — process has likely exited.
			if err != io.EOF {
				slog.Warn("session.readLoop: PTY read error",
					slog.String("session_id", s.ID),
					slog.String("error", err.Error()),
				)
			}
			break
		}
	}

	// Wait for the subprocess to finish and capture the exit code.
	waitErr := s.cmd.Wait()
	exitCode := 0
	if s.cmd.ProcessState != nil {
		exitCode = s.cmd.ProcessState.ExitCode()
	} else if waitErr != nil {
		// If ProcessState is nil but Wait returned an error, mark as -1.
		exitCode = -1
	}

	s.mu.Lock()
	s.Status = "exited"
	s.ExitCode = exitCode
	s.mu.Unlock()

	close(s.done)

	slog.Info("session.readLoop: session exited",
		slog.String("session_id", s.ID),
		slog.Int("exit_code", exitCode),
	)
}

// Read reads PTY output directly. This is the primary interface for
// WebSocket consumers that forward live output to the frontend.
// The ring buffer is populated separately by the background goroutine.
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

	err := s.pty.Close()

	// Wait for the readLoop goroutine to finish.
	<-s.done

	slog.Info("session.Close: session closed", slog.String("session_id", s.ID))
	return err
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
		ID:        s.ID,
		Title:     s.Title,
		Status:    s.Status,
		CreatedAt: s.CreatedAt,
		PID:       s.PID,
		ExitCode:  s.ExitCode,
	}
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

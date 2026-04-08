package session

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Ring buffer tests — verify the unexported ringBuffer directly.
// ---------------------------------------------------------------------------

func TestRingBuffer_Empty(t *testing.T) {
	rb := newRingBuffer(100)
	got := rb.ReadAll()
	if len(got) != 0 {
		t.Errorf("expected empty buffer, got %d bytes", len(got))
	}
}

func TestRingBuffer_BasicWriteRead(t *testing.T) {
	rb := newRingBuffer(100)
	rb.Write([]byte("hello"))
	got := rb.ReadAll()
	if string(got) != "hello" {
		t.Errorf("expected %q, got %q", "hello", string(got))
	}
}

func TestRingBuffer_MultipleWrites(t *testing.T) {
	rb := newRingBuffer(100)
	rb.Write([]byte("hello "))
	rb.Write([]byte("world"))
	got := rb.ReadAll()
	if string(got) != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", string(got))
	}
}

func TestRingBuffer_ExactCapacity(t *testing.T) {
	rb := newRingBuffer(5)
	rb.Write([]byte("abcde"))
	got := rb.ReadAll()
	if string(got) != "abcde" {
		t.Errorf("expected %q, got %q", "abcde", string(got))
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := newRingBuffer(10)
	// Write 15 bytes — only last 10 should be retained.
	rb.Write([]byte("abcdefghijklmno"))
	got := rb.ReadAll()
	if string(got) != "fghijklmno" {
		t.Errorf("expected %q, got %q", "fghijklmno", string(got))
	}
}

func TestRingBuffer_WrapAround(t *testing.T) {
	rb := newRingBuffer(10)
	rb.Write([]byte("12345678")) // 8 bytes, w=8, full=false
	rb.Write([]byte("abcd"))     // 4 bytes, wraps around
	// After wrap: buf=[c,d,3,4,5,6,7,8,a,b], w=2, full=true
	// ReadAll: buf[2:] + buf[:2] = "345678ab" + "cd" = "345678abcd"
	got := rb.ReadAll()
	if string(got) != "345678abcd" {
		t.Errorf("expected %q, got %q", "345678abcd", string(got))
	}
}

func TestRingBuffer_ZeroCapacityClamped(t *testing.T) {
	// Zero capacity should be clamped to 1, not panic.
	rb := newRingBuffer(0)
	rb.Write([]byte("abc"))
	got := rb.ReadAll()
	// Only the last byte fits in a 1-byte buffer.
	if string(got) != "c" {
		t.Errorf("expected %q, got %q", "c", string(got))
	}
}

func TestRingBuffer_WriteReturnsFullLength(t *testing.T) {
	rb := newRingBuffer(5)
	n, err := rb.Write([]byte("long data exceeding buffer"))
	if err != nil {
		t.Fatalf("Write returned unexpected error: %v", err)
	}
	if n != len("long data exceeding buffer") {
		t.Errorf("expected Write to return %d, got %d", len("long data exceeding buffer"), n)
	}
}

func TestRingBuffer_EmptyWrite(t *testing.T) {
	rb := newRingBuffer(10)
	n, err := rb.Write([]byte{})
	if err != nil {
		t.Fatalf("empty Write returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 from empty Write, got %d", n)
	}
	got := rb.ReadAll()
	if len(got) != 0 {
		t.Errorf("expected empty buffer after empty write, got %d bytes", len(got))
	}
}

// ---------------------------------------------------------------------------
// Session tests — spawn real PTY processes.
// ---------------------------------------------------------------------------

func TestSessionCreate_PTYIO(t *testing.T) {
	sess, err := NewSession("test-io", "test IO session", "powershell", 102400)
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	defer sess.Close()

	if sess.Status != "running" {
		t.Errorf("expected status %q, got %q", "running", sess.Status)
	}
	if sess.PID <= 0 {
		t.Errorf("expected positive PID, got %d", sess.PID)
	}

	// Write a command that produces recognizable output.
	_, err = sess.Write([]byte("echo hello-zplex\r\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Give the shell time to process the command and produce output.
	time.Sleep(2 * time.Second)

	// The ring buffer should have captured some output.
	data := sess.BufferData()
	if len(data) == 0 {
		t.Error("expected ring buffer to contain data after command, got empty")
	}

	// The output should contain our echo string.
	if !strings.Contains(string(data), "hello-zplex") {
		t.Errorf("expected buffer to contain %q, got %q", "hello-zplex", string(data))
	}
}

func TestSessionInfo(t *testing.T) {
	sess, err := NewSession("test-info", "info test", "powershell", 102400)
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}
	defer sess.Close()

	info := sess.Info()
	if info.ID != "test-info" {
		t.Errorf("expected ID %q, got %q", "test-info", info.ID)
	}
	if info.Title != "info test" {
		t.Errorf("expected Title %q, got %q", "info test", info.Title)
	}
	if info.Status != "running" {
		t.Errorf("expected Status %q, got %q", "running", info.Status)
	}
	if info.PID <= 0 {
		t.Errorf("expected positive PID, got %d", info.PID)
	}
	if info.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

// ---------------------------------------------------------------------------
// Session auto-detect exit — verify Done() channel and status change.
// ---------------------------------------------------------------------------

func TestSession_CloseSignalsDone(t *testing.T) {
	// Test that Close() terminates the subprocess, closes the Done
	// channel, and transitions Status to "exited".
	//
	// NOTE: On Windows ConPTY, pty.Read() does not return EOF when the
	// child process exits naturally — it blocks indefinitely. This means
	// the readLoop never reaches cmd.Wait() for a naturally exiting
	// process. Auto-detect of natural exit requires a design change in
	// readLoop (e.g. calling cmd.Wait in a separate goroutine). For now
	// we test the Close()-driven exit path, which is the primary
	// mechanism used by Kill() and Shutdown().
	sess, err := NewSession("test-close-done", "close done test", "cmd.exe", 102400)
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	// Verify session starts in running state.
	info := sess.Info()
	if info.Status != "running" {
		t.Fatalf("expected initial status %q, got %q", "running", info.Status)
	}

	// Close the session — this kills the process and closes the PTY.
	err = sess.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Done channel should be closed after Close returns.
	select {
	case <-sess.Done():
		// Good — the channel is closed.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Done channel after Close")
	}

	// Status should now be "exited".
	info = sess.Info()
	if info.Status != "exited" {
		t.Errorf("expected status %q after Close, got %q", "exited", info.Status)
	}
}

// ---------------------------------------------------------------------------
// Manager CRUD tests — exercise the SessionManager's full lifecycle.
// ---------------------------------------------------------------------------

func TestManager_CRUD(t *testing.T) {
	mgr := NewSessionManager("powershell", 102400)
	defer mgr.Shutdown()

	// Create
	sess, err := mgr.Create("crud test")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.ID == "" {
		t.Error("session ID should not be empty")
	}
	if sess.Status != "running" {
		t.Errorf("expected status %q, got %q", "running", sess.Status)
	}

	// Get — existing session
	got, err := mgr.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("Get returned wrong session: %q != %q", got.ID, sess.ID)
	}

	// Get — nonexistent session
	_, err = mgr.Get("nonexistent-id")
	if err == nil {
		t.Error("Get should return an error for a nonexistent session ID")
	}

	// List — should contain exactly one session
	infos := mgr.List()
	if len(infos) != 1 {
		t.Fatalf("expected 1 session in list, got %d", len(infos))
	}
	if infos[0].ID != sess.ID {
		t.Errorf("listed session ID %q does not match created ID %q", infos[0].ID, sess.ID)
	}

	// Kill
	id := sess.ID
	err = mgr.Kill(id)
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Get after Kill — should fail
	_, err = mgr.Get(id)
	if err == nil {
		t.Error("Get should fail after session is killed")
	}

	// List after Kill — should be empty
	infos = mgr.List()
	if len(infos) != 0 {
		t.Errorf("expected 0 sessions after kill, got %d", len(infos))
	}

	// Kill nonexistent — should return error
	err = mgr.Kill("nonexistent-id")
	if err == nil {
		t.Error("Kill should return an error for a nonexistent session ID")
	}
}

func TestManager_MultipleSessions(t *testing.T) {
	mgr := NewSessionManager("powershell", 102400)
	defer mgr.Shutdown()

	// Create multiple sessions.
	s1, err := mgr.Create("session one")
	if err != nil {
		t.Fatalf("Create session one failed: %v", err)
	}
	s2, err := mgr.Create("session two")
	if err != nil {
		t.Fatalf("Create session two failed: %v", err)
	}

	if s1.ID == s2.ID {
		t.Errorf("two sessions should have distinct IDs, both got %q", s1.ID)
	}

	infos := mgr.List()
	if len(infos) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(infos))
	}

	// Kill one, verify the other remains.
	err = mgr.Kill(s1.ID)
	if err != nil {
		t.Fatalf("Kill session one failed: %v", err)
	}

	infos = mgr.List()
	if len(infos) != 1 {
		t.Fatalf("expected 1 session after killing one, got %d", len(infos))
	}
	if infos[0].ID != s2.ID {
		t.Errorf("remaining session should be %q, got %q", s2.ID, infos[0].ID)
	}
}

func TestManager_Shutdown(t *testing.T) {
	mgr := NewSessionManager("powershell", 102400)

	_, err := mgr.Create("shutdown test 1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	_, err = mgr.Create("shutdown test 2")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	infos := mgr.List()
	if len(infos) != 2 {
		t.Fatalf("expected 2 sessions before shutdown, got %d", len(infos))
	}

	mgr.Shutdown()

	infos = mgr.List()
	if len(infos) != 0 {
		t.Errorf("expected 0 sessions after shutdown, got %d", len(infos))
	}
}

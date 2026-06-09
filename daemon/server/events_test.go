package server

import (
	"sync"
	"testing"
	"time"

	"github.com/zac15987/zplex/daemon/session"
)

// testEvent builds a minimal Event for use in Hub tests.
func testEvent(id string) Event {
	return Event{Type: "session.created", Session: session.SessionInfo{ID: id}}
}

// ---------------------------------------------------------------------------
// TestHub_SubscribeBroadcastReceive — single subscriber receives the event.
// ---------------------------------------------------------------------------

func TestHub_SubscribeBroadcastReceive(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()

	ev := testEvent("sess-1")
	h.Broadcast(ev)

	select {
	case got := <-ch:
		if got.Type != ev.Type {
			t.Errorf("expected Type %q, got %q", ev.Type, got.Type)
		}
		if got.Session.ID != ev.Session.ID {
			t.Errorf("expected Session.ID %q, got %q", ev.Session.ID, got.Session.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event on subscriber channel")
	}
}

// ---------------------------------------------------------------------------
// TestHub_BroadcastToMultipleSubscribers — all 3 subscribers receive the event.
// ---------------------------------------------------------------------------

func TestHub_BroadcastToMultipleSubscribers(t *testing.T) {
	h := NewHub()

	const numSubscribers = 3
	channels := make([]chan Event, numSubscribers)
	for i := range channels {
		channels[i] = h.Subscribe()
	}

	ev := testEvent("sess-multi")
	h.Broadcast(ev)

	for i, ch := range channels {
		select {
		case got := <-ch:
			if got.Session.ID != ev.Session.ID {
				t.Errorf("subscriber %d: expected Session.ID %q, got %q", i, ev.Session.ID, got.Session.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TestHub_SlowSubscriberDropped — Broadcast never blocks when channel is full.
// ---------------------------------------------------------------------------

func TestHub_SlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe() // buffer = subscriberBufferSize (16)

	// Broadcast more events than the buffer can hold without draining.
	// The key assertion is that Broadcast returns without blocking.
	const broadcasts = 20
	for i := 0; i < broadcasts; i++ {
		h.Broadcast(testEvent("drop-test"))
	}
	// If we reach this line, Broadcast never blocked — assertion passes.

	// Drain the channel and verify the count is within buffer capacity.
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto drained
		}
	}
drained:
	if count > subscriberBufferSize {
		t.Errorf("drained %d events, expected at most %d (buffer capacity)", count, subscriberBufferSize)
	}
}

// ---------------------------------------------------------------------------
// TestHub_DoubleUnsubscribeSafe — second Unsubscribe is a safe no-op.
// ---------------------------------------------------------------------------

func TestHub_DoubleUnsubscribeSafe(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()

	// First Unsubscribe — should close the channel.
	h.Unsubscribe(ch)

	// Verify the channel is closed: a receive returns ok==false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after Unsubscribe, but it was still open")
	}

	// Second Unsubscribe — must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Unsubscribe panicked: %v", r)
		}
	}()
	h.Unsubscribe(ch)
}

// ---------------------------------------------------------------------------
// TestHub_ConcurrentAccess — races detected by the Go race detector.
// ---------------------------------------------------------------------------

func TestHub_ConcurrentAccess(t *testing.T) {
	h := NewHub()

	const (
		numSubscribers  = 10
		broadcastsPerGo = 20
	)

	var wg sync.WaitGroup

	// Goroutines that subscribe and then unsubscribe.
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := h.Subscribe()
			// Drain the channel in a separate goroutine so we do not block
			// the broadcaster (Broadcast is non-blocking, but cleanly emptying
			// prevents the test goroutine from leaking a subscriber on return).
			var drainWg sync.WaitGroup
			drainWg.Add(1)
			go func() {
				defer drainWg.Done()
				for range ch {
					// discard
				}
			}()
			h.Unsubscribe(ch)
			drainWg.Wait()
		}()
	}

	// Goroutines that broadcast concurrently.
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < broadcastsPerGo; j++ {
				h.Broadcast(testEvent("concurrent"))
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines finished — no deadlock or panic.
	case <-time.After(10 * time.Second):
		t.Fatal("TestHub_ConcurrentAccess timed out — possible deadlock")
	}
}

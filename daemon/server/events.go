package server

import (
	"log/slog"
	"sync"
	"time"

	"github.com/zac15987/zplex/daemon/session"
)

// subscriberBufferSize is the capacity of each subscriber's event channel.
// A channel that fills past this limit causes the event to be dropped for
// that subscriber rather than blocking the broadcaster.
const subscriberBufferSize = 16

// HeartbeatInterval is how often the SSE handler writes a heartbeat comment.
const HeartbeatInterval = 30 * time.Second

// Event is a single SSE event broadcast to all subscribers.
type Event struct {
	Type    string // "session.created" | "session.closed" | "session.updated"
	Session session.SessionInfo
}

// Hub manages a set of SSE subscriber channels and distributes events to them.
// All exported methods are safe for concurrent use.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// NewHub creates a Hub with an empty subscriber set.
func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[chan Event]struct{}),
	}
}

// Subscribe creates a buffered event channel, registers it with the Hub, and
// returns it to the caller. The caller must call Unsubscribe when done to
// release the channel and prevent goroutine leaks.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, subscriberBufferSize)

	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	n := len(h.subscribers)
	h.mu.Unlock()

	slog.Info("server.handleEvents: subscriber connected",
		slog.Int("subscribers", n),
	)
	return ch
}

// Unsubscribe removes the channel from the Hub and closes it exactly once.
// Calling Unsubscribe with a channel that was never subscribed, or calling it
// a second time for the same channel, is a safe no-op.
func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	_, ok := h.subscribers[ch]
	if ok {
		delete(h.subscribers, ch)
		close(ch)
	}
	n := len(h.subscribers)
	h.mu.Unlock()

	if ok {
		slog.Info("server.events: subscriber removed",
			slog.Int("subscribers", n),
		)
	}
}

// Broadcast sends ev to every registered subscriber using a non-blocking send.
// If a subscriber's channel is full, the event is dropped for that subscriber
// and a warning is logged. Broadcast never blocks on a slow consumer.
func (h *Hub) Broadcast(ev Event) {
	slog.Info("server.events: broadcasting",
		slog.String("type", ev.Type),
		slog.String("session_id", ev.Session.ID),
	)

	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
			slog.Warn("server.events: dropped event for slow subscriber",
				slog.String("type", ev.Type),
			)
		}
	}
}

package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/zac15987/zplex/daemon/session"
)

// upgrader upgrades HTTP connections to WebSocket. The origin check allows
// localhost origins (for Electron dev mode) and requests with no Origin header
// (e.g., curl, Postman). Non-localhost origins are rejected.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return localhostOriginPattern.MatchString(origin) || origin == ""
	},
}

// wsMessage is the envelope for all client-to-server WebSocket messages.
type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// wsOutputMessage is sent server-to-client with PTY output.
type wsOutputMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// wsExitMessage is sent server-to-client when the PTY process exits.
type wsExitMessage struct {
	Type string `json:"type"`
	Code int    `json:"code"`
}

// handleWebSocket upgrades an HTTP request to a WebSocket connection and relays
// I/O between the WebSocket client and the session's PTY. On connect, any data
// in the session's ring buffer is replayed to the client so that a reconnecting
// frontend sees recent output.
//
// Concurrency contract for gorilla/websocket:
//   - The writer goroutine is the only goroutine that calls conn.WriteJSON.
//     The exit monitor waits for writerDone before sending its exit message.
//   - The main goroutine (reader loop) is the only goroutine that calls
//     conn.ReadJSON.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")

	// Look up session BEFORE upgrading so we can return a proper HTTP error
	// (404) instead of completing the handshake and immediately closing.
	sess, err := s.mgr.Get(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("server.handleWebSocket: upgrade failed",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return // Upgrade already wrote the error response
	}
	defer conn.Close()

	slog.Info("server.handleWebSocket: client connected",
		slog.String("session_id", sessionID),
	)

	// Step 1: Replay ring buffer content so reconnecting clients see recent output.
	replayData := sess.BufferData()
	if len(replayData) > 0 {
		msg := wsOutputMessage{Type: "output", Data: string(replayData)}
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("server.handleWebSocket: replay failed",
				slog.String("session_id", sessionID),
				slog.String("error", err.Error()),
			)
			return
		}
		slog.Info("server.handleWebSocket: replay sent",
			slog.String("session_id", sessionID),
			slog.Int("bytes", len(replayData)),
		)
	}

	// Step 2: Writer goroutine — subscribes to live PTY output from the
	// session's readLoop and forwards each chunk to the WebSocket client.
	outputCh := sess.Subscribe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer sess.Unsubscribe(outputCh)
		for data := range outputCh {
			msg := wsOutputMessage{Type: "output", Data: string(data)}
			if writeErr := conn.WriteJSON(msg); writeErr != nil {
				slog.Debug("server.handleWebSocket: write to WS failed",
					slog.String("session_id", sessionID),
					slog.String("error", writeErr.Error()),
				)
				return
			}
		}
	}()

	// Step 3: Exit monitor — waits for the session to exit, then sends the
	// exit message after the writer goroutine has drained. This avoids
	// concurrent WriteJSON calls.
	go func() {
		select {
		case <-sess.Done():
			// Wait for the writer goroutine to finish draining PTY output
			// before sending the exit message. This ensures the client
			// receives all output before the exit notification.
			<-writerDone
			info := sess.Info()
			exitMsg := wsExitMessage{Type: "exit", Code: info.ExitCode}
			if err := conn.WriteJSON(exitMsg); err != nil {
				slog.Debug("server.handleWebSocket: exit message write failed",
					slog.String("session_id", sessionID),
					slog.String("error", err.Error()),
				)
			}
			conn.Close()
		case <-writerDone:
			// Writer exited for another reason (WS write error, etc.)
			return
		}
	}()

	// Step 4: Reader loop — reads client messages and dispatches to PTY.
	// This runs in the current goroutine (the HTTP handler goroutine).
	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway) {
				slog.Warn("server.handleWebSocket: unexpected close",
					slog.String("session_id", sessionID),
					slog.String("error", err.Error()),
				)
			}
			break
		}

		switch msg.Type {
		case "input":
			if _, err := sess.Write([]byte(msg.Data)); err != nil {
				slog.Warn("server.handleWebSocket: PTY write failed",
					slog.String("session_id", sessionID),
					slog.String("error", err.Error()),
				)
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				if err := sess.Resize(msg.Cols, msg.Rows); err != nil {
					slog.Warn("server.handleWebSocket: resize failed",
						slog.String("session_id", sessionID),
						slog.Int("cols", msg.Cols),
						slog.Int("rows", msg.Rows),
						slog.String("error", err.Error()),
					)
				}
			}
		default:
			slog.Warn("server.handleWebSocket: unknown message type",
				slog.String("session_id", sessionID),
				slog.String("type", msg.Type),
			)
		}
	}

	slog.Info("server.handleWebSocket: client disconnected",
		slog.String("session_id", sessionID),
	)
}

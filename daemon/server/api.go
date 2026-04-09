package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Health endpoint
// ---------------------------------------------------------------------------

// healthResponse is the JSON payload returned by GET /api/health.
type healthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Uptime   int    `json:"uptime"`
	Sessions int    `json:"sessions"`
}

// handleHealth responds with daemon health information including version,
// uptime in seconds, and the number of active sessions.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleHealth: processing health check")

	uptimeSeconds := int(time.Since(s.startTime).Seconds())
	sessionCount := len(s.mgr.List())

	resp := healthResponse{
		Status:   "ok",
		Version:  s.version,
		Uptime:   uptimeSeconds,
		Sessions: sessionCount,
	}

	slog.Info("server.handleHealth: health check complete",
		slog.Int("uptime", uptimeSeconds),
		slog.Int("sessions", sessionCount),
	)

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// JSON response helpers
// ---------------------------------------------------------------------------

// writeJSON serializes v as JSON and writes it to w with the given HTTP status
// code. If serialization fails, it falls back to a 500 error.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("server.writeJSON: failed to encode response",
			slog.String("error", err.Error()),
		)
	}
}

// errorResponse is the JSON payload used by writeError.
type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error response with the given HTTP status code and
// human-readable message.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

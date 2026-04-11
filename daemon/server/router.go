// Package server implements the HTTP API layer for the zplex daemon.
// It provides REST endpoints, CORS middleware, and request logging.
package server

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/zac15987/zplex/daemon/session"
)

// localhostOriginPattern matches http://localhost with an optional port.
var localhostOriginPattern = regexp.MustCompile(`^http://localhost(:\d+)?$`)

// allowedMethods lists the HTTP methods permitted in CORS preflight responses.
const allowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

// allowedHeaders lists the request headers permitted in CORS preflight responses.
const allowedHeaders = "Content-Type, Authorization"

// Server holds references shared by all HTTP handlers.
type Server struct {
	mgr       *session.SessionManager
	startTime time.Time
	version   string
	layoutMu  sync.Mutex
	layout    layoutState
}

// NewServer creates a Server wired to the given session manager. The returned
// Server exposes a Handler() method that produces a fully-configured
// http.Handler (mux + middleware).
func NewServer(mgr *session.SessionManager) *Server {
	slog.Info("server.NewServer: initializing HTTP server")
	return &Server{
		mgr:       mgr,
		startTime: time.Now(),
		version:   "0.1.0",
	}
}

// Handler builds the HTTP handler chain: routes -> logging -> CORS.
// The outermost middleware executes first on each request.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handlePatchSession)

	mux.HandleFunc("GET /api/layout", s.handleGetLayout)
	mux.HandleFunc("PUT /api/layout", s.handlePutLayout)

	mux.HandleFunc("GET /ws/{session_id}", s.handleWebSocket)

	// Middleware chain: CORS wraps logging wraps routing.
	return corsMiddleware(loggingMiddleware(mux))
}

// ---------------------------------------------------------------------------
// CORS middleware
// ---------------------------------------------------------------------------

// corsMiddleware adds CORS headers for http://localhost:* origins and handles
// OPTIONS preflight requests. Requests from non-localhost origins are served
// normally but without CORS headers.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if localhostOriginPattern.MatchString(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Logging middleware
// ---------------------------------------------------------------------------

// responseWriter wraps http.ResponseWriter to capture the status code written
// by downstream handlers so the logging middleware can report it.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code before delegating to the underlying
// ResponseWriter.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack delegates to the underlying ResponseWriter's Hijack method if it
// implements http.Hijacker. This is required for WebSocket upgrades, which
// need to take over the raw TCP connection.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// loggingMiddleware logs every HTTP request with method, path, status code,
// and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		slog.Info("server: request completed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.statusCode),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

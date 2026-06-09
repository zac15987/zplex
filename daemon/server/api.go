package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zac15987/zplex/daemon/prefs"
	"github.com/zac15987/zplex/daemon/session"
)

// validAgentStates is the exact set of accepted agent_state values.
// Empty string is valid and means "no agent loop state".
var validAgentStates = map[string]bool{"": true, "active": true, "waiting": true, "done": true}

// isValidAgentState reports whether s is an accepted agent_state value.
func isValidAgentState(s string) bool { return validAgentStates[s] }

// ---------------------------------------------------------------------------
// Layout state types
// ---------------------------------------------------------------------------

// panelLayout represents a single panel's position in the saved layout.
type panelLayout struct {
	SessionID string `json:"session_id"`
	Position  int    `json:"position"`
}

// layoutState represents the complete layout configuration.
// Stored in-memory on the Server struct — not persisted to disk.
type layoutState struct {
	Panels              []panelLayout `json:"panels"`
	GridTemplateColumns string        `json:"grid_template_columns"`
	GridTemplateRows    string        `json:"grid_template_rows"`
}

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
	sessionCount := s.mgr.Count()

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
// Session CRUD endpoints
// ---------------------------------------------------------------------------

// handleListSessions responds with a JSON array of all active sessions.
// An empty manager returns [] (not null).
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleListSessions: listing sessions")

	infos := s.mgr.List()

	slog.Info("server.handleListSessions: returning sessions",
		slog.Int("count", len(infos)),
	)

	writeJSON(w, http.StatusOK, infos)
}

// createSessionRequest is the JSON body accepted by POST /api/sessions.
type createSessionRequest struct {
	Shell      string   `json:"shell"`
	Title      string   `json:"title"`
	Args       []string `json:"args,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Env        []string `json:"env,omitempty"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	Source     string   `json:"source,omitempty"`
	ProjectID  string   `json:"project_id,omitempty"`
	IssueID    string   `json:"issue_id,omitempty"`
	Role       string   `json:"role,omitempty"`
	AgentState string   `json:"agent_state,omitempty"`
}

// createSessionResponse is the JSON payload returned by POST /api/sessions.
type createSessionResponse struct {
	ID    string `json:"id"`
	WsURL string `json:"ws_url"`
}

// handleCreateSession creates a new PTY-backed session. Both shell and title
// are required in the request body.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleCreateSession: processing create request")

	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("server.handleCreateSession: invalid request body",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Shell == "" {
		slog.Warn("server.handleCreateSession: missing required field 'shell'")
		writeError(w, http.StatusBadRequest, "missing required field: shell")
		return
	}
	if req.Title == "" {
		slog.Warn("server.handleCreateSession: missing required field 'title'")
		writeError(w, http.StatusBadRequest, "missing required field: title")
		return
	}

	if !isValidAgentState(req.AgentState) {
		slog.Warn("server.handleCreateSession: invalid agent_state",
			slog.String("agent_state", req.AgentState),
		)
		writeError(w, http.StatusBadRequest, "invalid agent_state")
		return
	}

	sess, err := s.mgr.CreateSession(session.CreateOptions{
		Shell:      req.Shell,
		Title:      req.Title,
		Cols:       req.Cols,
		Rows:       req.Rows,
		Args:       req.Args,
		Cwd:        req.Cwd,
		Env:        req.Env,
		Source:     req.Source,
		ProjectID:  req.ProjectID,
		IssueID:    req.IssueID,
		Role:       req.Role,
		AgentState: req.AgentState,
	})
	if err != nil {
		slog.Error("server.handleCreateSession: failed to create session",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	slog.Info("server.handleCreateSession: session created",
		slog.String("session_id", sess.ID),
	)

	s.hub.Broadcast(Event{Type: "session.created", Session: sess.Info()})
	s.watchSessionExit(sess)

	writeJSON(w, http.StatusCreated, createSessionResponse{
		ID:    sess.ID,
		WsURL: "/ws/" + sess.ID,
	})
}

// handleGetSession responds with full details for a single session.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("server.handleGetSession: looking up session",
		slog.String("session_id", id),
	)

	sess, err := s.mgr.Get(id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	slog.Info("server.handleGetSession: returning session",
		slog.String("session_id", id),
	)

	writeJSON(w, http.StatusOK, sess.Info())
}

// handleDeleteSession kills a session and removes it from the manager.
// Sessions with Kind=="fixed" are protected and cannot be deleted via this
// endpoint — they return HTTP 403 to prevent accidental removal.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("server.handleDeleteSession: deleting session",
		slog.String("session_id", id),
	)

	sess, err := s.mgr.Get(id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	if sess.Info().Kind == "fixed" {
		slog.Warn("server.handleDeleteSession: refusing to delete fixed session",
			slog.String("session_id", id),
		)
		writeError(w, http.StatusForbidden, "cannot delete fixed session")
		return
	}

	if err := s.mgr.Kill(id); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		slog.Error("server.handleDeleteSession: failed to kill session",
			slog.String("session_id", id),
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusInternalServerError, "failed to kill session")
		return
	}

	slog.Info("server.handleDeleteSession: session deleted",
		slog.String("session_id", id),
	)

	s.hub.Broadcast(Event{Type: "session.closed", Session: sess.Info()})

	w.WriteHeader(http.StatusNoContent)
}

// patchSessionRequest is the JSON body accepted by PATCH /api/sessions/{id}.
// Identity fields (source, project_id, issue_id, role) are intentionally
// absent — they are immutable after creation.
type patchSessionRequest struct {
	Title      string `json:"title"`
	Status     string `json:"status"`
	AgentState string `json:"agent_state"`
}

// handlePatchSession updates mutable metadata on an existing session.
func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("server.handlePatchSession: updating session",
		slog.String("session_id", id),
	)

	sess, err := s.mgr.Get(id)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	var req patchSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("server.handlePatchSession: invalid request body",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !isValidAgentState(req.AgentState) {
		slog.Warn("server.handlePatchSession: invalid agent_state",
			slog.String("agent_state", req.AgentState),
		)
		writeError(w, http.StatusBadRequest, "invalid agent_state")
		return
	}

	sess.UpdateMeta(req.Title, req.Status, req.AgentState)

	slog.Info("server.handlePatchSession: session updated",
		slog.String("session_id", id),
	)

	s.hub.Broadcast(Event{Type: "session.updated", Session: sess.Info()})

	writeJSON(w, http.StatusOK, sess.Info())
}

// ---------------------------------------------------------------------------
// zpit integration
// ---------------------------------------------------------------------------

// LaunchZpit creates the single fixed zpit session via the session manager.
// Env is set only when a custom ZPIT_CONFIG path is configured; otherwise the
// child inherits the daemon's environment. Returns the created session or an
// error if the zpit binary cannot start.
func (s *Server) LaunchZpit() (*session.Session, error) {
	slog.Info("server.LaunchZpit: launching zpit fixed session")

	var env []string
	if s.zpitCfg.Config != "" {
		env = append(os.Environ(), "ZPIT_CONFIG="+s.zpitCfg.Config)
	}

	sess, err := s.mgr.CreateSession(session.CreateOptions{
		Shell: s.zpitCfg.Bin,
		Title: "zpit",
		Kind:  "fixed",
		Args:  s.zpitCfg.Args,
		Env:   env,
	})
	if err != nil {
		slog.Error("server.LaunchZpit: failed to launch zpit",
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	slog.Info("server.LaunchZpit: zpit fixed session launched",
		slog.String("session_id", sess.ID),
	)

	s.hub.Broadcast(Event{Type: "session.created", Session: sess.Info()})
	s.watchSessionExit(sess)

	return sess, nil
}

// handleRestartZpit handles POST /api/zpit/restart. It tears down any existing
// fixed session and re-launches zpit, returning the new session's ID and WS URL.
func (s *Server) handleRestartZpit(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleRestartZpit: processing zpit restart request")

	if !s.zpitCfg.Enabled {
		writeError(w, http.StatusConflict, "zpit is disabled")
		return
	}

	if existing, ok := s.mgr.FixedSession(); ok {
		if err := s.mgr.Kill(existing.ID); err != nil {
			slog.Warn("server.handleRestartZpit: failed to kill existing fixed session",
				slog.String("session_id", existing.ID),
				slog.String("error", err.Error()),
			)
			// Non-fatal: continue to launch a new session.
		}
	}

	sess, err := s.LaunchZpit()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to launch zpit")
		return
	}

	slog.Info("server.handleRestartZpit: zpit restarted",
		slog.String("session_id", sess.ID),
	)

	writeJSON(w, http.StatusCreated, createSessionResponse{
		ID:    sess.ID,
		WsURL: "/ws/" + sess.ID,
	})
}

// watchSessionExit starts a goroutine that emits a session.closed event when
// the given session's process exits naturally (its Done() channel closes).
// Emission lives at the server-handler layer so the session package stays
// decoupled from the event Hub.
func (s *Server) watchSessionExit(sess *session.Session) {
	go func() {
		<-sess.Done()
		slog.Info("server.watchSessionExit: session exited, broadcasting closed",
			slog.String("session_id", sess.ID))
		s.hub.Broadcast(Event{Type: "session.closed", Session: sess.Info()})
	}()
}

// handleEvents serves the Server-Sent Events stream. Clients subscribe to
// real-time session lifecycle events (session.created/closed/updated).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleEvents: client connecting")

	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("server.handleEvents: ResponseWriter does not support flushing")
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			slog.Info("server.handleEvents: client disconnected")
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev.Session)
			if err != nil {
				slog.Error("server.handleEvents: marshal failed", slog.String("error", err.Error()))
				continue
			}
			// SSE frame: event: <type>\ndata: <json>\n\n
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				slog.Warn("server.handleEvents: write failed", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		case <-ticker.C:
			// Heartbeat comment keeps the connection alive.
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				slog.Warn("server.handleEvents: heartbeat write failed", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// Layout endpoints
// ---------------------------------------------------------------------------

// handleGetLayout returns the layout state for a workspace.
// The workspace is determined by the ?workspace= query parameter (default: "default").
// Returns HTTP 200 with empty state if no layout has been saved for the workspace.
func (s *Server) handleGetLayout(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace")
	if workspace == "" {
		workspace = "default"
	}
	slog.Info("server.handleGetLayout: retrieving layout",
		slog.String("workspace", workspace),
	)

	s.layoutMu.Lock()
	state, exists := s.layouts[workspace]
	s.layoutMu.Unlock()

	if !exists {
		state = layoutState{}
	}

	// Ensure panels is never null in JSON output
	if state.Panels == nil {
		state.Panels = []panelLayout{}
	}

	slog.Info("server.handleGetLayout: layout retrieved",
		slog.String("workspace", workspace),
		slog.Int("panels", len(state.Panels)),
	)

	writeJSON(w, http.StatusOK, state)
}

// handlePutLayout saves a new layout state for a workspace, replacing any previous state.
// The workspace is determined by the ?workspace= query parameter (default: "default").
func (s *Server) handlePutLayout(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace")
	if workspace == "" {
		workspace = "default"
	}
	slog.Info("server.handlePutLayout: processing layout save",
		slog.String("workspace", workspace),
	)

	var state layoutState
	if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
		slog.Warn("server.handlePutLayout: invalid request body",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Ensure panels is never nil
	if state.Panels == nil {
		state.Panels = []panelLayout{}
	}

	s.layoutMu.Lock()
	s.layouts[workspace] = state
	s.layoutMu.Unlock()

	slog.Info("server.handlePutLayout: layout saved",
		slog.String("workspace", workspace),
		slog.Int("panels", len(state.Panels)),
	)

	writeJSON(w, http.StatusOK, state)
}

// ---------------------------------------------------------------------------
// Workspace endpoints
// ---------------------------------------------------------------------------

// handleListWorkspaces returns the list of workspace IDs that have layout data.
func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleListWorkspaces: listing workspaces")

	s.layoutMu.Lock()
	ids := make([]string, 0, len(s.layouts))
	for id := range s.layouts {
		ids = append(ids, id)
	}
	s.layoutMu.Unlock()

	slog.Info("server.handleListWorkspaces: returning workspaces",
		slog.Int("count", len(ids)),
	)

	writeJSON(w, http.StatusOK, ids)
}

// handleDeleteWorkspace removes the layout data for a specific workspace.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slog.Info("server.handleDeleteWorkspace: deleting workspace layout",
		slog.String("workspace", id),
	)

	s.layoutMu.Lock()
	_, exists := s.layouts[id]
	if exists {
		delete(s.layouts, id)
	}
	s.layoutMu.Unlock()

	if !exists {
		slog.Warn("server.handleDeleteWorkspace: workspace not found",
			slog.String("workspace", id),
		)
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	slog.Info("server.handleDeleteWorkspace: workspace layout deleted",
		slog.String("workspace", id),
	)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Preferences endpoints
// ---------------------------------------------------------------------------

// handleGetPreferences returns the current app-managed user preferences.
func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handleGetPreferences: retrieving preferences")
	writeJSON(w, http.StatusOK, s.prefs.Get())
}

// handlePutPreferences validates and persists the given preferences, then
// echoes the stored value back.
func (s *Server) handlePutPreferences(w http.ResponseWriter, r *http.Request) {
	slog.Info("server.handlePutPreferences: processing preferences save")

	var p prefs.Preferences
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		slog.Warn("server.handlePutPreferences: invalid request body",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.prefs.Set(p); err != nil {
		slog.Warn("server.handlePutPreferences: rejected preferences",
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("server.handlePutPreferences: preferences saved",
		slog.String("panel_close", p.PanelClose),
		slog.String("workspace_close", p.WorkspaceClose),
	)

	writeJSON(w, http.StatusOK, p)
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

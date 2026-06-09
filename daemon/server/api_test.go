package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zac15987/zplex/daemon/config"
	"github.com/zac15987/zplex/daemon/prefs"
	"github.com/zac15987/zplex/daemon/session"
)

// newTestServerWithZpit creates a Server backed by a real SessionManager for
// integration testing, using the given ZpitConfig. The test server and manager
// are torn down automatically when the test finishes.
func newTestServerWithZpit(t *testing.T, zpitCfg config.ZpitConfig) (*Server, *httptest.Server) {
	t.Helper()
	mgr := session.NewSessionManager("pwsh", 102400)
	prefsStore, err := prefs.NewStoreWithPath(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatalf("failed to create prefs store: %v", err)
	}
	srv := NewServer(mgr, prefsStore, zpitCfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		mgr.Shutdown()
	})
	return srv, ts
}

// newTestServer creates a Server backed by a real SessionManager for
// integration testing. The test server and manager are torn down
// automatically when the test finishes.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	return newTestServerWithZpit(t, config.ZpitConfig{Enabled: true, Bin: "pwsh"})
}

// wsURL converts an httptest.Server URL to a WebSocket URL for the given path.
func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

// createTestSession creates a session via the REST API and returns its ID.
// It fails the test immediately if the request or response is malformed.
func createTestSession(t *testing.T, ts *httptest.Server, title string) string {
	t.Helper()

	body := `{"shell":"pwsh","title":"` + title + `"}`
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("createTestSession: POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("createTestSession: expected 201, got %d", resp.StatusCode)
	}

	var result createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("createTestSession: decode failed: %v", err)
	}
	if result.ID == "" {
		t.Fatal("createTestSession: returned empty session ID")
	}
	return result.ID
}

// ---------------------------------------------------------------------------
// Health endpoint
// ---------------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("expected status %q, got %q", "ok", body.Status)
	}
	if body.Version != "0.1.0" {
		t.Errorf("expected version %q, got %q", "0.1.0", body.Version)
	}
	if body.Uptime < 0 {
		t.Errorf("expected uptime >= 0, got %d", body.Uptime)
	}
	if body.Sessions < 0 {
		t.Errorf("expected sessions >= 0, got %d", body.Sessions)
	}
}

// ---------------------------------------------------------------------------
// Session listing
// ---------------------------------------------------------------------------

func TestListSessions_Empty(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Decode into a raw JSON array to verify it is [] and not null.
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "[]" {
		t.Errorf("expected empty array [], got %s", trimmed)
	}
}

func TestListSessions_AfterCreate(t *testing.T) {
	_, ts := newTestServer(t)

	createTestSession(t, ts, "list-test-1")
	createTestSession(t, ts, "list-test-2")

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var infos []session.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		t.Fatalf("failed to decode sessions list: %v", err)
	}

	if len(infos) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(infos))
	}
}

// ---------------------------------------------------------------------------
// Session creation
// ---------------------------------------------------------------------------

func TestCreateSession_Success(t *testing.T) {
	_, ts := newTestServer(t)

	body := `{"shell":"pwsh","title":"create-test"}`
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}

	var result createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	if result.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if !strings.HasPrefix(result.WsURL, "/ws/") {
		t.Errorf("expected ws_url to start with /ws/, got %q", result.WsURL)
	}
}

func TestCreateSession_MissingFields(t *testing.T) {
	_, ts := newTestServer(t)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing shell", body: `{"title":"test"}`},
		{name: "missing title", body: `{"shell":"pwsh"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST /api/sessions failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected status 400, got %d", resp.StatusCode)
			}

			var errResp errorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Session get
// ---------------------------------------------------------------------------

func TestGetSession_Success(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := createTestSession(t, ts, "get-test")

	resp, err := http.Get(ts.URL + "/api/sessions/" + sessionID)
	if err != nil {
		t.Fatalf("GET /api/sessions/%s failed: %v", sessionID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var info session.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode session info: %v", err)
	}

	if info.ID != sessionID {
		t.Errorf("expected ID %q, got %q", sessionID, info.ID)
	}
	if info.Title != "get-test" {
		t.Errorf("expected title %q, got %q", "get-test", info.Title)
	}
	if info.Status != "running" {
		t.Errorf("expected status %q, got %q", "running", info.Status)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/sessions/nonexistent")
	if err != nil {
		t.Fatalf("GET /api/sessions/nonexistent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Session delete
// ---------------------------------------------------------------------------

func TestDeleteSession_Success(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := createTestSession(t, ts, "delete-test")

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatalf("failed to create DELETE request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/sessions/%s failed: %v", sessionID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", resp.StatusCode)
	}

	// Subsequent GET should return 404.
	getResp, err := http.Get(ts.URL + "/api/sessions/" + sessionID)
	if err != nil {
		t.Fatalf("GET after delete failed: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/nonexistent", nil)
	if err != nil {
		t.Fatalf("failed to create DELETE request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/sessions/nonexistent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

// TestDeleteSession_FixedProtected verifies that DELETE on a fixed session
// returns HTTP 403 with the exact error body and leaves the session intact.
func TestDeleteSession_FixedProtected(t *testing.T) {
	srv, ts := newTestServer(t)

	sess, err := srv.LaunchZpit()
	if err != nil {
		t.Fatalf("LaunchZpit failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+sess.ID, nil)
	if err != nil {
		t.Fatalf("failed to create DELETE request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/sessions/%s failed: %v", sess.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", resp.StatusCode)
	}

	var errResp errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "cannot delete fixed session" {
		t.Errorf("expected error %q, got %q", "cannot delete fixed session", errResp.Error)
	}

	// Verify the fixed session is still listed with status "running".
	getResp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions failed: %v", err)
	}
	defer getResp.Body.Close()

	var infos []session.SessionInfo
	if err := json.NewDecoder(getResp.Body).Decode(&infos); err != nil {
		t.Fatalf("failed to decode sessions list: %v", err)
	}

	found := false
	for _, info := range infos {
		if info.ID == sess.ID {
			found = true
			if info.Status != "running" {
				t.Errorf("fixed session status: expected %q, got %q", "running", info.Status)
			}
			break
		}
	}
	if !found {
		t.Errorf("fixed session %q not found in session list after attempted delete", sess.ID)
	}
}

// ---------------------------------------------------------------------------
// zpit restart endpoint
// ---------------------------------------------------------------------------

// TestRestartZpit_Disabled verifies that POST /api/zpit/restart returns
// HTTP 409 with the appropriate error body when zpit is disabled.
func TestRestartZpit_Disabled(t *testing.T) {
	_, ts := newTestServerWithZpit(t, config.ZpitConfig{Enabled: false})

	resp, err := http.Post(ts.URL+"/api/zpit/restart", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /api/zpit/restart failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected status 409, got %d", resp.StatusCode)
	}

	var errResp errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "zpit is disabled" {
		t.Errorf("expected error %q, got %q", "zpit is disabled", errResp.Error)
	}
}

// TestRestartZpit_Success verifies that POST /api/zpit/restart returns
// HTTP 201 with a new session ID that differs from the previously launched one.
func TestRestartZpit_Success(t *testing.T) {
	srv, ts := newTestServer(t)

	oldSess, err := srv.LaunchZpit()
	if err != nil {
		t.Fatalf("initial LaunchZpit failed: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/zpit/restart", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /api/zpit/restart failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}

	var result createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode restart response: %v", err)
	}

	if result.ID == "" {
		t.Error("expected non-empty session ID in restart response")
	}
	if result.ID == oldSess.ID {
		t.Errorf("expected new session ID to differ from old %q, but got same ID", oldSess.ID)
	}
	if !strings.HasPrefix(result.WsURL, "/ws/") {
		t.Errorf("expected ws_url to start with /ws/, got %q", result.WsURL)
	}
}

// ---------------------------------------------------------------------------
// Session patch
// ---------------------------------------------------------------------------

func TestPatchSession_Success(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := createTestSession(t, ts, "patch-test")

	patchBody := `{"title":"patched title"}`
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+sessionID, strings.NewReader(patchBody))
	if err != nil {
		t.Fatalf("failed to create PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /api/sessions/%s failed: %v", sessionID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var info session.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode patched session: %v", err)
	}

	if info.Title != "patched title" {
		t.Errorf("expected title %q, got %q", "patched title", info.Title)
	}
}

func TestPatchSession_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	patchBody := `{"title":"will not work"}`
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/nonexistent", strings.NewReader(patchBody))
	if err != nil {
		t.Fatalf("failed to create PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /api/sessions/nonexistent failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// WebSocket — connect and I/O relay
// ---------------------------------------------------------------------------

func TestWebSocket_ConnectAndIO(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := createTestSession(t, ts, "ws-io-test")

	// Allow the shell to start up before connecting.
	time.Sleep(2 * time.Second)

	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL(ts, "/ws/"+sessionID), nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send a command that produces recognizable output. We do this
	// immediately — any replay data or prompt output is harmless and
	// will be read alongside the echo output in the loop below.
	inputMsg := wsMessage{Type: "input", Data: "echo hello-ws-test\r\n"}
	if err := conn.WriteJSON(inputMsg); err != nil {
		t.Fatalf("failed to send input message: %v", err)
	}

	// Read output messages until we see the echo string or timeout.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	found := false
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}
		if strings.Contains(string(raw), "hello-ws-test") {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected to see 'hello-ws-test' in WebSocket output")
	}
}

// ---------------------------------------------------------------------------
// WebSocket — resize
// ---------------------------------------------------------------------------

func TestWebSocket_Resize(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := createTestSession(t, ts, "ws-resize-test")

	time.Sleep(1 * time.Second)

	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL(ts, "/ws/"+sessionID), nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// Send a resize message.
	resizeMsg := wsMessage{Type: "resize", Cols: 120, Rows: 40}
	if err := conn.WriteJSON(resizeMsg); err != nil {
		t.Fatalf("failed to send resize message: %v", err)
	}

	// Resize is fire-and-forget; verify the connection is still alive by
	// reading at least one message (prompt output) or timing out gracefully.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, readErr := conn.ReadMessage()
	// A timeout is acceptable — the resize succeeded without error.
	if readErr != nil && !isTimeoutError(readErr) {
		t.Errorf("unexpected error after resize: %v", readErr)
	}
}

// isTimeoutError checks whether an error is a network timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	// net.Error has a Timeout() method.
	type timeouter interface {
		Timeout() bool
	}
	if te, ok := err.(timeouter); ok {
		return te.Timeout()
	}
	return false
}

// ---------------------------------------------------------------------------
// WebSocket — not found
// ---------------------------------------------------------------------------

func TestWebSocket_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	dialer := websocket.Dialer{}
	_, resp, err := dialer.Dial(wsURL(ts, "/ws/nonexistent-id"), nil)
	if err == nil {
		t.Fatal("expected WebSocket dial to fail for nonexistent session")
	}

	if resp == nil {
		t.Fatal("expected non-nil HTTP response on dial failure")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Preferences endpoints
// ---------------------------------------------------------------------------

func TestPreferences_DefaultEmpty(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/preferences")
	if err != nil {
		t.Fatalf("GET /api/preferences failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body prefs.Preferences
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode preferences: %v", err)
	}
	if body.PanelClose != "" || body.WorkspaceClose != "" {
		t.Errorf("expected empty default preferences, got %+v", body)
	}
}

func TestPreferences_PutThenGet(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/preferences",
		strings.NewReader(`{"panel_close":"kill","workspace_close":"detach"}`))
	req.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/preferences failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 on PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(ts.URL + "/api/preferences")
	if err != nil {
		t.Fatalf("GET /api/preferences failed: %v", err)
	}
	defer getResp.Body.Close()

	var body prefs.Preferences
	if err := json.NewDecoder(getResp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode preferences: %v", err)
	}
	want := prefs.Preferences{PanelClose: "kill", WorkspaceClose: "detach"}
	if body != want {
		t.Errorf("GET after PUT: got %+v, want %+v", body, want)
	}
}

func TestPreferences_PutInvalidValue(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/preferences",
		strings.NewReader(`{"panel_close":"explode"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/preferences failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid value, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Session metadata round-trip (AC-2)
// ---------------------------------------------------------------------------

// TestCreateSession_MetadataRoundTrip verifies that all five identity metadata
// fields (source, project_id, issue_id, role, agent_state) survive a
// POST /api/sessions → GET /api/sessions/{id} round-trip.
func TestCreateSession_MetadataRoundTrip(t *testing.T) {
	_, ts := newTestServer(t)

	body := `{"shell":"pwsh","title":"meta","source":"zpit","project_id":"p","issue_id":"42","role":"coder","agent_state":"active"}`
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}

	var created createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty session ID")
	}

	getResp, err := http.Get(ts.URL + "/api/sessions/" + created.ID)
	if err != nil {
		t.Fatalf("GET /api/sessions/%s failed: %v", created.ID, err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", getResp.StatusCode)
	}

	var info session.SessionInfo
	if err := json.NewDecoder(getResp.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode session info: %v", err)
	}

	if info.Source != "zpit" {
		t.Errorf("Source: expected %q, got %q", "zpit", info.Source)
	}
	if info.ProjectID != "p" {
		t.Errorf("ProjectID: expected %q, got %q", "p", info.ProjectID)
	}
	if info.IssueID != "42" {
		t.Errorf("IssueID: expected %q, got %q", "42", info.IssueID)
	}
	if info.Role != "coder" {
		t.Errorf("Role: expected %q, got %q", "coder", info.Role)
	}
	if info.AgentState != "active" {
		t.Errorf("AgentState: expected %q, got %q", "active", info.AgentState)
	}
}

// ---------------------------------------------------------------------------
// agent_state validation on POST (AC-3)
// ---------------------------------------------------------------------------

// TestCreateSession_InvalidAgentState verifies that POST /api/sessions with an
// invalid agent_state value returns HTTP 400 with the exact error message.
func TestCreateSession_InvalidAgentState(t *testing.T) {
	_, ts := newTestServer(t)

	body := `{"shell":"pwsh","title":"bad","agent_state":"bogus"}`
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", resp.StatusCode)
	}

	var errResp errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "invalid agent_state" {
		t.Errorf("expected error %q, got %q", "invalid agent_state", errResp.Error)
	}
}

// ---------------------------------------------------------------------------
// agent_state on PATCH — valid path (AC-3)
// ---------------------------------------------------------------------------

// TestPatchSession_AgentState verifies that PATCH /api/sessions/{id} with a
// valid agent_state value updates the field and returns 200 with the new info.
func TestPatchSession_AgentState(t *testing.T) {
	_, ts := newTestServer(t)

	id := createTestSession(t, ts, "patch-agent")

	patchBody := `{"agent_state":"waiting"}`
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+id, strings.NewReader(patchBody))
	if err != nil {
		t.Fatalf("failed to create PATCH request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /api/sessions/%s failed: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var info session.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode session info: %v", err)
	}
	if info.AgentState != "waiting" {
		t.Errorf("AgentState: expected %q, got %q", "waiting", info.AgentState)
	}
}

// ---------------------------------------------------------------------------
// agent_state on PATCH — invalid path, session unchanged (AC-3)
// ---------------------------------------------------------------------------

// TestPatchSession_InvalidAgentState verifies that an invalid agent_state in
// PATCH returns HTTP 400 and leaves the session's agent_state unchanged.
func TestPatchSession_InvalidAgentState(t *testing.T) {
	_, ts := newTestServer(t)

	id := createTestSession(t, ts, "patch-agent-invalid")

	// First, set a known state via a valid PATCH.
	validPatch := `{"agent_state":"active"}`
	req1, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+id, strings.NewReader(validPatch))
	if err != nil {
		t.Fatalf("failed to create PATCH request: %v", err)
	}
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("PATCH (valid) failed: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on valid PATCH, got %d", resp1.StatusCode)
	}

	// Now try an invalid agent_state.
	invalidPatch := `{"agent_state":"explode"}`
	req2, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+id, strings.NewReader(invalidPatch))
	if err != nil {
		t.Fatalf("failed to create PATCH request: %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("PATCH (invalid) failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid agent_state, got %d", resp2.StatusCode)
	}

	var errResp errorResponse
	if err := json.NewDecoder(resp2.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "invalid agent_state" {
		t.Errorf("expected error %q, got %q", "invalid agent_state", errResp.Error)
	}

	// Verify the session state is still "active" (unchanged by the rejected patch).
	getResp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("GET /api/sessions/%s failed: %v", id, err)
	}
	defer getResp.Body.Close()

	var info session.SessionInfo
	if err := json.NewDecoder(getResp.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode session info: %v", err)
	}
	if info.AgentState != "active" {
		t.Errorf("AgentState after invalid PATCH: expected %q, got %q", "active", info.AgentState)
	}
}

// ---------------------------------------------------------------------------
// SSE headers and event delivery (AC-4 + AC-13b)
// ---------------------------------------------------------------------------

// TestEvents_HeadersAndDelivery verifies that GET /api/events returns the
// correct SSE headers and delivers a session.created event when a session is
// created while the client is subscribed.
func TestEvents_HeadersAndDelivery(t *testing.T) {
	_, ts := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/events failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %q", cc)
	}
	if conn := resp.Header.Get("Connection"); conn != "keep-alive" {
		t.Errorf("expected Connection keep-alive, got %q", conn)
	}

	// Give the subscriber goroutine a moment to register, then create a session.
	time.Sleep(200 * time.Millisecond)
	go func() {
		body := `{"shell":"pwsh","title":"sse-test"}`
		http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(body)) //nolint:errcheck
	}()

	// Read from the stream until we see a session.created event or time out.
	resultCh := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: ") {
				resultCh <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				return
			}
		}
	}()

	select {
	case evType := <-resultCh:
		if evType != "session.created" {
			t.Errorf("expected first event type session.created, got %q", evType)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session.created SSE event")
	}
}

// ---------------------------------------------------------------------------
// responseWriter implements http.Flusher (AC-6)
// ---------------------------------------------------------------------------

// TestResponseWriterImplementsFlusher asserts that the responseWriter type
// satisfies both http.Flusher and http.Hijacker at compile and runtime.
func TestResponseWriterImplementsFlusher(t *testing.T) {
	var rw interface{} = &responseWriter{}
	if _, ok := rw.(http.Flusher); !ok {
		t.Error("responseWriter does not implement http.Flusher")
	}
	if _, ok := rw.(http.Hijacker); !ok {
		t.Error("responseWriter does not implement http.Hijacker")
	}
}

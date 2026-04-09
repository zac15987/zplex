package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zac15987/zplex/daemon/session"
)

// newTestServer creates a Server backed by a real SessionManager for
// integration testing. The test server and manager are torn down
// automatically when the test finishes.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	mgr := session.NewSessionManager("powershell", 102400)
	srv := NewServer(mgr)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		mgr.Shutdown()
	})
	return srv, ts
}

// wsURL converts an httptest.Server URL to a WebSocket URL for the given path.
func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

// createTestSession creates a session via the REST API and returns its ID.
// It fails the test immediately if the request or response is malformed.
func createTestSession(t *testing.T, ts *httptest.Server, title string) string {
	t.Helper()

	body := `{"shell":"powershell","title":"` + title + `"}`
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

	body := `{"shell":"powershell","title":"create-test"}`
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
		{name: "missing title", body: `{"shell":"powershell"}`},
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

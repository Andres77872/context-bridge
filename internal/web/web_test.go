package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/config"
	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("secure db directory: %v", err)
	}
	dbPath := filepath.Join(dir, "context-bridge.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedSession(t *testing.T, st *store.Store, sessionID string, captures []struct {
	seq     int
	agent   string
	desc    string
	content string
}) {
	t.Helper()
	for _, c := range captures {
		record, err := st.AddCapture(store.CaptureInput{
			ParentSessionID: sessionID,
			ChildSessionID:  "child-" + sessionID,
			CallID:          fmt.Sprintf("call-%s-%d", sessionID, c.seq),
			Agent:           c.agent,
			Description:     c.desc,
			Content:         c.content,
			CapturedAt:      time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("seed capture %d: %v", c.seq, err)
		}
		if record.Seq != c.seq {
			t.Fatalf("expected seq %d, got %d", c.seq, record.Seq)
		}
	}
}

// searchResults decodes the search envelope and returns its result list.
func searchResults(t *testing.T, body []byte) []any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("parse search response: %v", err)
	}
	results, ok := response["results"].([]any)
	if !ok {
		if response["results"] == nil {
			return nil
		}
		t.Fatalf("expected a results array, got %T", response["results"])
	}
	return results
}

// captureItems decodes the capture page envelope and returns its item list.
func captureItems(t *testing.T, body []byte) []any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("parse capture response: %v", err)
	}
	items, ok := response["items"].([]any)
	if !ok {
		if response["items"] == nil {
			return nil
		}
		t.Fatalf("expected an items array, got %T", response["items"])
	}
	return items
}

func TestHandleSearchReturnsResults(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_test", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_test", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "search files", content: "authentication handler code here"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest("GET", "/api/sessions/ses_test/search?q=auth", nil)
	req.SetPathValue("id", "ses_test")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(searchResults(t, rec.Body.Bytes())) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestHandleSearchEmptyQueryReturns400(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_test", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	req := httptest.NewRequest("GET", "/api/sessions/ses_test/search?q=", nil)
	req.SetPathValue("id", "ses_test")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleSearchReflectsModeRegex(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_regex", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_regex", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "auth module", content: "regex pattern content here"},
		{seq: 2, agent: "explore", desc: "other", content: "other content without match"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest("GET", "/api/sessions/ses_regex/search?q=regex%20pattern", nil)
	req.SetPathValue("id", "ses_regex")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if results := searchResults(t, rec.Body.Bytes()); len(results) != 1 {
		t.Fatalf("regex mode should match only 'regex pattern', got %d results", len(results))
	}
}

func TestHandleSearchReflectsModeFTS5(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_fts5", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_fts5", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "auth module", content: "authentication module code"},
		{seq: 2, agent: "explore", desc: "other", content: "other content"},
	})

	srv := New(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest("GET", "/api/sessions/ses_fts5/search?q=authentication", nil)
	req.SetPathValue("id", "ses_fts5")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if results := searchResults(t, rec.Body.Bytes()); len(results) != 1 {
		t.Fatalf("fts5 mode should match 'authentication' word, got %d results", len(results))
	}
}

func TestHandleDeleteSessionSoftDeletesAndReturnsActionResponse(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_delete", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_delete", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "capture", content: "session content"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/ses_delete", nil)
	req.SetPathValue("id", "ses_delete")

	srv.handleDeleteSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp actionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.OK || resp.Action != "delete_session" || resp.SessionID != "ses_delete" {
		t.Fatalf("unexpected action response: %+v", resp)
	}

	sessions, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected deleted session to be hidden, got %d", len(sessions))
	}

	captures, err := st.ListCaptures("ses_delete", "")
	if err != nil {
		t.Fatalf("list captures: %v", err)
	}
	if len(captures) != 0 {
		t.Fatalf("expected deleted session captures to be hidden, got %d", len(captures))
	}
}

func TestHandleDeleteSessionReturns404ForMissingSession(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/ses_missing", nil)
	req.SetPathValue("id", "ses_missing")

	srv.handleDeleteSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected session not found error, got %s", rec.Body.String())
	}
}

func TestHandleDeleteCaptureHardDeletesAndReturnsActionResponse(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_capture_delete", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_capture_delete", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "first", content: "one"},
		{seq: 2, agent: "explore", desc: "second", content: "two"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/ses_capture_delete/captures/1", nil)
	req.SetPathValue("id", "ses_capture_delete")
	req.SetPathValue("seq", "1")

	srv.handleDeleteCapture(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp actionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.OK || resp.Action != "delete_capture" || resp.SessionID != "ses_capture_delete" || resp.Seq != 1 {
		t.Fatalf("unexpected action response: %+v", resp)
	}

	captures, err := st.ListCaptures("ses_capture_delete", "")
	if err != nil {
		t.Fatalf("list captures: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("expected 1 capture after delete, got %d", len(captures))
	}
	if captures[0].Seq != 2 {
		t.Fatalf("expected seq 2 to remain, got %d", captures[0].Seq)
	}
}

func TestHandleDeleteCaptureReturns400ForInvalidSeq(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/ses_bad/captures/nope", nil)
	req.SetPathValue("id", "ses_bad")
	req.SetPathValue("seq", "nope")

	srv.handleDeleteCapture(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid seq") {
		t.Fatalf("expected invalid seq error, got %s", rec.Body.String())
	}
}

func TestHandleDeleteCaptureReturns404ForMissingCapture(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_missing_capture", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_missing_capture", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "first", content: "one"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/ses_missing_capture/captures/9", nil)
	req.SetPathValue("id", "ses_missing_capture")
	req.SetPathValue("seq", "9")

	srv.handleDeleteCapture(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("expected not found error, got %s", rec.Body.String())
	}
}

func TestHandleCapturesFiltersByAgentQuery(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_filter", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_filter", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "search files", content: "one"},
		{seq: 2, agent: "explore", desc: "audit ui", content: "two"},
	})

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_filter/captures?agent=grep", nil)
	req.SetPathValue("id", "ses_filter")

	srv.handleCaptures(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	captures := captureItems(t, rec.Body.Bytes())
	if len(captures) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(captures))
	}
	first, ok := captures[0].(map[string]any)
	if !ok {
		t.Fatalf("expected a capture object, got %T", captures[0])
	}
	if first["agent"] != "grep" {
		t.Fatalf("expected grep capture, got %#v", first["agent"])
	}
}

func TestRoutesServeHTMLWithParityControls(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440/", nil)
	req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{
		"Cache-Control":                "no-store",
		"Content-Security-Policy":      dashboardCSP,
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("expected %s=%q, got %q", name, want, got)
		}
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`id="view-overview"`,
		`id="view-analytics"`,
		`id="view-sessions"`,
		`id="view-search"`,
		`id="confirm-modal"`,
		`id="settings-modal"`,
		`id="shortcuts-modal"`,
		`id="feedback-banner"`,
		`id="capture-agent-filter"`,
		`id="capture-query-filter"`,
		`id="capture-delete-button"`,
		`href="/static/app.css"`,
		`src="/static/app.js"`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected html to contain %s", needle)
		}
	}
}

// TestDashboardAssetsAreSelfContained pins the offline guarantee: the shell and
// its assets must not reference any external origin, because the strict CSP
// blocks them and the dashboard has to render on a disconnected machine.
func TestDashboardAssetsAreSelfContained(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	assets := map[string]string{
		"/":               "text/html",
		"/static/app.css": "text/css",
		"/static/app.js":  "text/javascript",
	}

	for path, wantType := range assets {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440"+path, nil)
		req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})
		srv.Routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, wantType) {
			t.Fatalf("%s: expected content type %s, got %s", path, wantType, got)
		}
		body := rec.Body.String()
		for _, forbidden := range []string{"https://", "http://cdn", "fonts.googleapis", "cdn.tailwindcss"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s must not reference the network, found %q", path, forbidden)
			}
		}
	}
}

// TestDashboardScriptUsesDelegatedActions guards the XSS posture: the dashboard
// dispatches through data attributes instead of building inline handlers out of
// interpolated session ids.
func TestDashboardScriptUsesDelegatedActions(t *testing.T) {
	script, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	shell, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	body := string(script)
	sources := body + string(shell)

	// Every destructive or navigational control is dispatched from a data
	// attribute, so no user-controlled id is ever concatenated into markup that
	// executes.
	for _, needle := range []string{
		`data-action="open-session"`,
		`data-action="open-capture"`,
		`data-action="delete-session"`,
		`data-action="delete-capture"`,
	} {
		if !strings.Contains(sources, needle) {
			t.Fatalf("expected the dashboard sources to contain %s", needle)
		}
	}
	if !strings.Contains(body, `function esc(value)`) {
		t.Fatal("expected app.js to define the HTML escaper")
	}
	for _, forbidden := range []string{
		`onclick="`,
		"selectSession('${",
		"viewCapture('${",
		"innerHTML = capture.content",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("app.js must not contain unsafe pattern %q", forbidden)
		}
	}
}

func TestCSPForbidsInlineScriptAndExternalOrigins(t *testing.T) {
	if strings.Contains(dashboardCSP, "script-src 'self' 'unsafe-inline'") {
		t.Fatal("script-src must not allow inline script")
	}
	if !strings.Contains(dashboardCSP, "default-src 'none'") {
		t.Fatalf("expected a default-deny policy, got %q", dashboardCSP)
	}
	for _, forbidden := range []string{"cdn.tailwindcss.com", "fonts.googleapis.com", "fonts.gstatic.com"} {
		if strings.Contains(dashboardCSP, forbidden) {
			t.Fatalf("CSP must not allow external origin %s", forbidden)
		}
	}
}

func TestRoutesRejectCrossOriginRequestsWithoutCORS(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440/api/stats", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected cross-origin request to be rejected with 403, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("dashboard must not emit a permissive CORS header, got %q", got)
	}
}

func TestRoutesAllowSameOriginRequests(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440/api/stats", nil)
	req.Header.Set("Origin", "http://127.0.0.1:7440")
	req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected same-origin request to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoutesRejectDNSRebindingHostEvenWithValidToken(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest(http.MethodGet, "http://evil.example:7440/api/stats", nil)
	req.Header.Set("Origin", "http://evil.example:7440")
	req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})
	rec := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected DNS-rebinding Host to be rejected, got %d", rec.Code)
	}
}

func TestRoutesRequireTokenAndBootstrapSessionCookie(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	unauthorized := httptest.NewRecorder()
	srv.Routes().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440/api/stats", nil))
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("expected missing token to be rejected, got %d", unauthorized.Code)
	}

	bootstrap := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7440/?token="+srv.accessToken, nil)
	srv.Routes().ServeHTTP(bootstrap, req)
	if bootstrap.Code != http.StatusSeeOther {
		t.Fatalf("expected token bootstrap redirect, got %d", bootstrap.Code)
	}
	cookies := bootstrap.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "context_bridge_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected bootstrap cookie: %+v", cookies)
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7440", "127.8.9.10:7440", "[::1]:7440", "localhost:7440"} {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("expected %q to be accepted: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:7440", ":7440", "192.0.2.1:7440", "example.com:7440", "127.0.0.1"} {
		if err := validateLoopbackAddr(addr); err == nil {
			t.Errorf("expected %q to be rejected", addr)
		}
	}
}

func TestHandleSearchFTS5SanitizerHandlesUnmatchedQuote(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_fts5_sanitize", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_fts5_sanitize", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "test", content: "some content"},
	})

	srv := New(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"))
	// The FTS5 sanitizer properly handles unmatched quotes by escaping them
	// Input: "unmatched -> becomes valid FTS5 query: """unmatched"
	req := httptest.NewRequest("GET", "/api/sessions/ses_fts5_sanitize/search?q=\"unmatched", nil)
	req.SetPathValue("id", "ses_fts5_sanitize")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	// The sanitizer makes this a valid query, so it should return 200 with no results
	// (the content doesn't contain "unmatched")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for sanitized FTS5 query, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNewStoresSearchMode(t *testing.T) {
	st := openTestStore(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	srv := New(st, store.SearchModeFTS5, configPath)

	if srv.searchMode != store.SearchModeFTS5 {
		t.Fatalf("expected FTS5 mode, got %q", srv.searchMode)
	}
	if srv.configPath != configPath {
		t.Fatalf("expected config path %q, got %q", configPath, srv.configPath)
	}
}

func TestHandleGetConfigReturnsCurrentMode(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"))

	req := httptest.NewRequest("GET", "/api/config", nil)
	rec := httptest.NewRecorder()
	srv.handleGetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["search_mode"] != "fts5" {
		t.Fatalf("expected search_mode fts5, got %#v", resp["search_mode"])
	}
	if resp["scope"] != "global" {
		t.Fatalf("expected scope global, got %#v", resp["scope"])
	}
}

func TestHandleUpdateConfigWritesAndUpdatesMemory(t *testing.T) {
	st := openTestStore(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	srv := New(st, store.SearchModeRegex, configPath)

	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"search_mode":"fts5"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := srv.currentSearchMode(); got != store.SearchModeFTS5 {
		t.Fatalf("expected in-memory mode %q, got %q", store.SearchModeFTS5, got)
	}

	cfg, err := config.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.SearchMode != config.SearchModeFTS5 {
		t.Fatalf("expected saved mode %q, got %q", config.SearchModeFTS5, cfg.SearchMode)
	}
}

func TestHandleUpdateConfigRejectsInvalidMode(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"search_mode":"ripgrep"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateConfigRejectsUnknownFields(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"search_mode":"regex","engine":"fts5"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateConfigEnforcesJSONTransportContract(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "missing content type", body: `{"search_mode":"regex"}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "wrong content type", contentType: "text/plain", body: `{"search_mode":"regex"}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "multiple JSON values", contentType: "application/json", body: `{"search_mode":"regex"} {}`, wantStatus: http.StatusBadRequest},
		{name: "trailing garbage", contentType: "application/json", body: `{"search_mode":"regex"} trailing`, wantStatus: http.StatusBadRequest},
		{name: "oversized body", contentType: "application/json", body: strings.Repeat(" ", maxConfigBodyBytes+1), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "JSON media type parameters", contentType: "application/json; charset=utf-8", body: `{"search_mode":"fts5"}`, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openTestStore(t)
			srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
			req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()

			srv.handleUpdateConfig(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleSearchRejectsOversizedUTF8Query(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	query := strings.Repeat("é", maxWebQueryBytes/2+1)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_test/search?q="+query, nil)
	req.SetPathValue("id", "ses_test")
	rec := httptest.NewRecorder()

	srv.handleSearch(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "exceeds") {
		t.Fatalf("expected oversized UTF-8 query to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCapturesPropagatesCanceledRequestContext(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_test/captures", nil)
	req.SetPathValue("id", "ses_test")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.handleCaptures(rec, req)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "context canceled") {
		t.Fatalf("expected canceled request context to reach the store, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWebCaptureAndSearchResultsAreBounded(t *testing.T) {
	st := openTestStore(t)
	const sessionID = "ses_web_limits"
	for i := 1; i <= maxWebCaptures+1; i++ {
		if _, err := st.AddCapture(store.CaptureInput{
			ParentSessionID: sessionID,
			CallID:          fmt.Sprintf("call-%03d", i),
			Agent:           "grep",
			Description:     "bounded output",
			Content:         "needle",
			CapturedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed capture %d: %v", i, err)
		}
	}

	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	capturesRequest := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/captures", nil)
	capturesRequest.SetPathValue("id", sessionID)
	capturesRecorder := httptest.NewRecorder()
	srv.handleCaptures(capturesRecorder, capturesRequest)
	if capturesRecorder.Code != http.StatusOK {
		t.Fatalf("captures: expected 200, got %d: %s", capturesRecorder.Code, capturesRecorder.Body.String())
	}
	if captures := captureItems(t, capturesRecorder.Body.Bytes()); len(captures) != maxWebCaptures {
		t.Fatalf("expected at most %d captures, got %d", maxWebCaptures, len(captures))
	}

	searchRequest := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/search?q=needle", nil)
	searchRequest.SetPathValue("id", sessionID)
	searchRecorder := httptest.NewRecorder()
	srv.handleSearch(searchRecorder, searchRequest)
	if searchRecorder.Code != http.StatusOK {
		t.Fatalf("search: expected 200, got %d: %s", searchRecorder.Code, searchRecorder.Body.String())
	}
	if results := searchResults(t, searchRecorder.Body.Bytes()); len(results) != maxWebSearchResults {
		t.Fatalf("expected at most %d search results, got %d", maxWebSearchResults, len(results))
	}
}

// TestHandleSearchModePersistsAcrossRestart proves that saved mode persists
// when the config is loaded and a new Server instance is created (simulating restart).
// This mirrors the flow in main.go: LoadConfig -> New(st, mode, configPath)
func TestHandleSearchModePersistsAcrossRestart(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_persist", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_persist", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "auth module", content: "authentication module code"},
		{seq: 2, agent: "explore", desc: "other", content: "other content"},
	})

	configPath := filepath.Join(t.TempDir(), "config.json")

	// First server instance starts with regex mode
	firstSrv := New(st, store.SearchModeRegex, configPath)

	// Update to FTS5 via API
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"search_mode":"fts5"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	firstSrv.handleUpdateConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update config: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// "Restart" simulation: load saved config and create new server
	// This mirrors what main.go does on restart
	cfg, err := config.LoadConfig(configPath, true)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	secondSrv := New(st, store.SearchMode(cfg.SearchMode), configPath)

	// Verify the new server uses FTS5 mode from saved config
	if secondSrv.currentSearchMode() != store.SearchModeFTS5 {
		t.Fatalf("expected new server to use FTS5 from saved config, got %q", secondSrv.currentSearchMode())
	}

	// Prove it behaves as FTS5: search for word that would match differently in regex
	searchReq := httptest.NewRequest("GET", "/api/sessions/ses_persist/search?q=authentication", nil)
	searchReq.SetPathValue("id", "ses_persist")
	searchRec := httptest.NewRecorder()
	secondSrv.handleSearch(searchRec, searchReq)

	if searchRec.Code != http.StatusOK {
		t.Fatalf("search: expected 200, got %d: %s", searchRec.Code, searchRec.Body.String())
	}

	if results := searchResults(t, searchRec.Body.Bytes()); len(results) != 1 {
		t.Fatalf("FTS5 mode should match 'authentication' word, got %d results", len(results))
	}
}

// TestHandleSearchModeRoundTripBothModes proves both modes can be saved and loaded
// through the full config -> runtime -> restart cycle.
func TestHandleSearchModeRoundTripBothModes(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_roundtrip", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_roundtrip", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "test", content: "uniqueword test content"},
	})

	configPath := filepath.Join(t.TempDir(), "config.json")

	tests := []struct {
		name       string
		mode       string
		query      string
		wantResult bool
	}{
		{
			name:       "regex mode persists",
			mode:       "regex",
			query:      "unique.*word", // regex pattern
			wantResult: true,
		},
		{
			name:       "fts5 mode persists",
			mode:       "fts5",
			query:      "uniqueword", // simple word for FTS5
			wantResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create server, save mode
			srv := New(st, store.SearchModeRegex, configPath)
			req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"search_mode":"`+tt.mode+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.handleUpdateConfig(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("save config: %d - %s", rec.Code, rec.Body.String())
			}

			// Restart simulation: load config and create new instance (mirrors main.go)
			cfg, err := config.LoadConfig(configPath, true)
			if err != nil {
				t.Fatalf("load config after save: %v", err)
			}
			newSrv := New(st, store.SearchMode(cfg.SearchMode), configPath)

			if newSrv.currentSearchMode() != store.SearchMode(tt.mode) {
				t.Fatalf("expected mode %q after restart, got %q", tt.mode, newSrv.currentSearchMode())
			}

			// Verify search uses the persisted mode
			searchReq := httptest.NewRequest("GET", "/api/sessions/ses_roundtrip/search?q="+tt.query, nil)
			searchReq.SetPathValue("id", "ses_roundtrip")
			searchRec := httptest.NewRecorder()
			newSrv.handleSearch(searchRec, searchReq)

			if searchRec.Code != http.StatusOK {
				t.Fatalf("search: %d - %s", searchRec.Code, searchRec.Body.String())
			}

			if results := searchResults(t, searchRec.Body.Bytes()); tt.wantResult && len(results) == 0 {
				t.Fatalf("expected results for mode %q with query %q", tt.mode, tt.query)
			}
		})
	}
}

// ── new API surface ─────────────────────────────────────────────────────────

func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("parse response: %v (body: %s)", err, string(body))
	}
	return value
}

func serveAPI(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "http://127.0.0.1:7440"+path, nil)
	req.AddCookie(&http.Cookie{Name: "context_bridge_session", Value: srv.accessToken})
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func seedDashboard(t *testing.T, st *store.Store) {
	t.Helper()
	for _, session := range []string{"ses_one", "ses_two"} {
		if err := st.EnsureSession(session, ""); err != nil {
			t.Fatalf("ensure session %s: %v", session, err)
		}
	}
	captures := []struct {
		session string
		agent   string
		desc    string
		content string
	}{
		{"ses_one", "grep", "find auth", "authentication handler body"},
		{"ses_one", "explore", "map routes", "routing table body"},
		{"ses_two", "grep", "find login", "authentication login body"},
	}
	for i, capture := range captures {
		if _, err := st.AddCapture(store.CaptureInput{
			ParentSessionID: capture.session,
			CallID:          fmt.Sprintf("call-%d", i),
			Agent:           capture.agent,
			Description:     capture.desc,
			Content:         capture.content,
			CapturedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed capture %d: %v", i, err)
		}
	}
}

func TestHandleMetaReportsVersionAndLimits(t *testing.T) {
	st := openTestStore(t)
	srv := NewWithVersion(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"), "v9.9.9")

	rec := serveAPI(t, srv, http.MethodGet, "/api/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	meta := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if meta["version"] != "v9.9.9" {
		t.Fatalf("expected the build version, got %v", meta["version"])
	}
	if meta["search_mode"] != "fts5" {
		t.Fatalf("expected the running search mode, got %v", meta["search_mode"])
	}
	if meta["retention_days"] != float64(store.RetentionWindowDays) {
		t.Fatalf("expected retention %d, got %v", store.RetentionWindowDays, meta["retention_days"])
	}
	if path, _ := meta["database_path"].(string); path == "" {
		t.Fatal("expected the database path to be reported")
	}
	limits, ok := meta["limits"].(map[string]any)
	if !ok || limits["max_search_results"] != float64(maxWebSearchResults) {
		t.Fatalf("expected limits to be reported, got %v", meta["limits"])
	}
}

func TestNewDefaultsVersionToDev(t *testing.T) {
	st := openTestStore(t)
	if srv := New(st, store.SearchModeRegex, ""); srv.version != "dev" {
		t.Fatalf("expected the default version dev, got %q", srv.version)
	}
	if srv := NewWithVersion(st, store.SearchModeRegex, "", "  "); srv.version != "dev" {
		t.Fatalf("expected blank versions to fall back to dev, got %q", srv.version)
	}
}

func TestHandleAnalyticsReturnsDenseSeries(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/analytics?days=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	analytics := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if analytics["window_days"] != float64(7) {
		t.Fatalf("expected a 7 day window, got %v", analytics["window_days"])
	}
	if analytics["captures"] != float64(3) {
		t.Fatalf("expected 3 captures, got %v", analytics["captures"])
	}
	if daily, ok := analytics["daily"].([]any); !ok || len(daily) != 7 {
		t.Fatalf("expected 7 dense daily buckets, got %v", analytics["daily"])
	}
	if heatmap, ok := analytics["heatmap"].([]any); !ok || len(heatmap) != 7*24 {
		t.Fatalf("expected a dense heatmap, got %d cells", len(heatmap))
	}
	if agents, ok := analytics["agents"].([]any); !ok || len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %v", analytics["agents"])
	}
	storage, ok := analytics["storage"].(map[string]any)
	if !ok || storage["database_bytes"] == float64(0) {
		t.Fatalf("expected storage totals, got %v", analytics["storage"])
	}
}

func TestHandleAnalyticsClampsWindowToRetention(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/analytics?days=9999")
	analytics := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if analytics["window_days"] != float64(store.RetentionWindowDays) {
		t.Fatalf("expected the window clamped to retention, got %v", analytics["window_days"])
	}
}

func TestHandleAgentsListsDistinctAgents(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/agents")
	agents := decodeJSON[[]string](t, rec.Body.Bytes())
	if len(agents) != 2 || agents[0] != "explore" || agents[1] != "grep" {
		t.Fatalf("expected sorted distinct agents, got %v", agents)
	}
}

func TestHandleSessionsSupportsFilteringAndSorting(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	all := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions?limit=10").Body.Bytes())
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(all))
	}
	if all[0]["bytes"] == nil || all[0]["agent_count"] == nil {
		t.Fatalf("expected usage columns on session items, got %v", all[0])
	}

	filtered := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions?q=ses_two").Body.Bytes())
	if len(filtered) != 1 || filtered[0]["id"] != "ses_two" {
		t.Fatalf("expected only ses_two, got %v", filtered)
	}

	byAgent := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions?agent=explore").Body.Bytes())
	if len(byAgent) != 1 || byAgent[0]["id"] != "ses_one" {
		t.Fatalf("expected only ses_one for agent explore, got %v", byAgent)
	}

	sorted := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions?sort=captures").Body.Bytes())
	if sorted[0]["id"] != "ses_one" {
		t.Fatalf("expected the busiest session first, got %v", sorted[0]["id"])
	}

	if rec := serveAPI(t, srv, http.MethodGet, "/api/sessions?sort=bogus"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported sort, got %d", rec.Code)
	}
}

func TestHandleSessionsHidesDeletedUnlessRequested(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	if err := st.MarkSessionDeleted("ses_two"); err != nil {
		t.Fatalf("MarkSessionDeleted: %v", err)
	}
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	live := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions").Body.Bytes())
	if len(live) != 1 {
		t.Fatalf("expected deleted sessions hidden, got %d", len(live))
	}

	withDeleted := decodeJSON[[]map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions?include_deleted=true").Body.Bytes())
	if len(withDeleted) != 2 {
		t.Fatalf("expected 2 sessions when including deleted, got %d", len(withDeleted))
	}
}

func TestHandleSessionDetailReportsUsage(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	detail := decodeJSON[map[string]any](t, rec.Body.Bytes())
	session, _ := detail["session"].(map[string]any)
	if session == nil || session["id"] != "ses_one" {
		t.Fatalf("expected the session envelope, got %v", detail)
	}
	if session["capture_count"] != float64(2) {
		t.Fatalf("expected 2 captures, got %v", session["capture_count"])
	}
	if agents, ok := detail["agents"].([]any); !ok || len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %v", detail["agents"])
	}

	if rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing session, got %d", rec.Code)
	}
}

func TestHandleCapturesPagesFiltersAndReportsTotals(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	page := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?limit=1").Body.Bytes())
	if page["total"] != float64(2) || page["filtered"] != float64(2) {
		t.Fatalf("expected totals 2/2, got %v/%v", page["total"], page["filtered"])
	}
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one item per page, got %d", len(items))
	}
	if agents, ok := page["agents"].([]any); !ok || len(agents) != 2 {
		t.Fatalf("expected the session agent list, got %v", page["agents"])
	}

	second := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?limit=1&offset=1").Body.Bytes())
	secondItems, _ := second["items"].([]any)
	first := items[0].(map[string]any)
	if len(secondItems) != 1 || secondItems[0].(map[string]any)["seq"] == first["seq"] {
		t.Fatalf("expected the offset to advance the page, got %v", secondItems)
	}

	desc := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?order=desc").Body.Bytes())
	descItems, _ := desc["items"].([]any)
	if desc["order"] != "desc" || descItems[0].(map[string]any)["seq"] != float64(2) {
		t.Fatalf("expected newest-first ordering, got %v", desc["order"])
	}

	byText := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?q=routes").Body.Bytes())
	if byText["filtered"] != float64(1) || byText["total"] != float64(2) {
		t.Fatalf("expected 1 of 2 after filtering, got %v of %v", byText["filtered"], byText["total"])
	}
}

func TestHandleCapturesRejectsInvalidAgentFilter(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	if rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?agent=not%20valid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid agent filter, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCaptureIncludesLineCount(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	capture := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures/1").Body.Bytes())
	if capture["content"] == nil || capture["lines"] == float64(0) {
		t.Fatalf("expected content and a line count, got %v", capture)
	}
}

func TestHandleCaptureRawServesPlainTextDownload(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures/1/raw")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected text/plain, got %s", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "ses_one-1.txt") {
		t.Fatalf("expected an attachment filename, got %s", got)
	}
	if !strings.Contains(rec.Body.String(), "authentication handler body") {
		t.Fatalf("expected the raw capture body, got %s", rec.Body.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("raw downloads must keep the nosniff header")
	}

	if rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures/99/raw"); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing capture, got %d", rec.Code)
	}
}

func TestHandleExportSessionBundlesCaptures(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/export")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "ses_one-export.json") {
		t.Fatalf("expected an export filename, got %s", got)
	}
	export := decodeJSON[map[string]any](t, rec.Body.Bytes())
	captures, _ := export["captures"].([]any)
	if len(captures) != 2 {
		t.Fatalf("expected 2 exported captures, got %d", len(captures))
	}
	if first, _ := captures[0].(map[string]any); first["content"] == nil {
		t.Fatalf("expected full content in the export, got %v", captures[0])
	}
}

func TestHandleGlobalSearchSpansSessions(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/search?q=authentication")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	response := decodeJSON[map[string]any](t, rec.Body.Bytes())
	if response["scope"] != "all" {
		t.Fatalf("expected an all-session scope, got %v", response["scope"])
	}
	results, _ := response["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("expected a hit in each session, got %d", len(results))
	}
	if response["mode"] != "regex" || response["total_matches"] == float64(0) {
		t.Fatalf("expected mode and match totals, got %v", response)
	}

	scoped := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/search?q=authentication&session=ses_two").Body.Bytes())
	if scoped["scope"] != "session" {
		t.Fatalf("expected a session scope, got %v", scoped["scope"])
	}
	scopedResults, _ := scoped["results"].([]any)
	if len(scopedResults) != 1 {
		t.Fatalf("expected a single scoped result, got %d", len(scopedResults))
	}

	byAgent := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/search?q=body&agent=explore").Body.Bytes())
	agentResults, _ := byAgent["results"].([]any)
	if len(agentResults) != 1 {
		t.Fatalf("expected the agent filter to narrow results, got %d", len(agentResults))
	}
}

func TestHandleGlobalSearchReportsInvalidRegexAsClientError(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/search?q=%5B")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid pattern, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid regex pattern") {
		t.Fatalf("expected the regex error to reach the client, got %s", rec.Body.String())
	}
}

func TestHandleGlobalSearchRequiresQuery(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	if rec := serveAPI(t, srv, http.MethodGet, "/api/search"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without a query, got %d", rec.Code)
	}
}

func TestSanitizeFilenameKeepsDownloadsSafe(t *testing.T) {
	cases := map[string]string{
		`ses_ok-1.2`:             "ses_ok-1.2",
		`../../etc/passwd`:       "etc-passwd",
		`ses "quoted"`:           "ses--quoted",
		``:                       "session",
		strings.Repeat("a", 200): strings.Repeat("a", 64),
	}
	for input, want := range cases {
		if got := sanitizeFilename(input); got != want {
			t.Fatalf("sanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHandleCapturesTreatsZeroLimitAsTheCap(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	page := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures?limit=0").Body.Bytes())
	if page["limit"] != float64(maxWebCaptures) {
		t.Fatalf("expected a zero limit to snap to the cap, got %v", page["limit"])
	}
}

func TestHandleSearchTreatsZeroLimitAsTheCap(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	rec := serveAPI(t, srv, http.MethodGet, "/api/search?q=body&limit=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if results := searchResults(t, rec.Body.Bytes()); len(results) != 3 {
		t.Fatalf("expected every match, got %d", len(results))
	}
}

// TestAgentViewMatchesTheMCPToolPayload is the contract behind the dashboard's
// agent view: what an operator reads must be byte-for-byte what the model got.
func TestAgentViewMatchesTheMCPToolPayload(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))
	ctx := context.Background()

	read := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/mcp/read/1").Body.Bytes())
	wantRead, err := bridgeMCP.RenderRead(ctx, st, "ses_one", 1)
	if err != nil {
		t.Fatalf("RenderRead: %v", err)
	}
	if read["text"] != wantRead {
		t.Fatalf("read agent view differs from the MCP payload:\n%v", read["text"])
	}
	if read["tool"] != "read" || read["bytes"] == float64(0) || read["max_bytes"] == float64(0) {
		t.Fatalf("expected payload metadata, got %v", read)
	}
	text, _ := read["text"].(string)
	for _, want := range []string{
		"<untrusted-context-bridge-data>",
		"## Output #1: [grep]",
		"# Context Bridge: grep subagent output",
		"authentication handler body",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent view must contain %q, got:\n%s", want, text)
		}
	}

	list := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/mcp/list").Body.Bytes())
	wantList, err := bridgeMCP.RenderList(ctx, st, "ses_one", "")
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	if list["text"] != wantList {
		t.Fatalf("list agent view differs from the MCP payload")
	}

	search := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/mcp/search?q=authentication").Body.Bytes())
	wantSearch, err := bridgeMCP.RenderSearch(ctx, st, "ses_one", "authentication", bridgeMCP.DefaultSearchContextLines, store.SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderSearch: %v", err)
	}
	if search["text"] != wantSearch {
		t.Fatalf("search agent view differs from the MCP payload")
	}
}

func TestAgentViewUsesTheSavedSearchMode(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"))

	got := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/mcp/search?q=authentication").Body.Bytes())
	want, err := bridgeMCP.RenderSearch(context.Background(), st, "ses_one", "authentication", bridgeMCP.DefaultSearchContextLines, store.SearchModeFTS5)
	if err != nil {
		t.Fatalf("RenderSearch: %v", err)
	}
	if got["text"] != want {
		t.Fatalf("the agent view must use the mode the tools would use")
	}
}

func TestAgentViewRejectsBadInput(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	cases := map[string]int{
		"/api/sessions/ses_one/mcp/read/abc":  http.StatusBadRequest,
		"/api/sessions/ses_one/mcp/read/999":  http.StatusNotFound,
		"/api/sessions/ses_one/mcp/search":    http.StatusBadRequest,
		"/api/sessions/ses_one/mcp/search?q=": http.StatusBadRequest,
	}
	for path, want := range cases {
		if rec := serveAPI(t, srv, http.MethodGet, path); rec.Code != want {
			t.Fatalf("%s: expected %d, got %d (%s)", path, want, rec.Code, rec.Body.String())
		}
	}
}

// The stored document keeps its generated front matter; the dashboard shows the
// agent rendering instead of inventing its own.
func TestHandleCaptureReturnsTheStoredDocumentVerbatim(t *testing.T) {
	st := openTestStore(t)
	seedDashboard(t, st)
	srv := New(st, store.SearchModeRegex, filepath.Join(t.TempDir(), "config.json"))

	capture := decodeJSON[map[string]any](t, serveAPI(t, srv, http.MethodGet, "/api/sessions/ses_one/captures/1").Body.Bytes())
	content, _ := capture["content"].(string)

	record, err := st.GetCaptureBySeq("ses_one", 1)
	if err != nil {
		t.Fatalf("GetCaptureBySeq: %v", err)
	}
	if content != record.Content {
		t.Fatal("the capture endpoint must return the stored document unchanged")
	}
	if !strings.Contains(content, "# Context Bridge:") {
		t.Fatalf("expected the stored front matter to survive, got %q", content)
	}
}

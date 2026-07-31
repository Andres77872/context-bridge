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

	var results []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(results) == 0 {
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

	var results []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(results) != 1 {
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

	var results []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(results) != 1 {
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

	var captures []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &captures); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(captures))
	}
	if captures[0]["agent"] != "grep" {
		t.Fatalf("expected grep capture, got %#v", captures[0]["agent"])
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
		`id="confirm-modal"`,
		`id="feedback-banner"`,
		`id="capture-agent-filter"`,
		`id="capture-query-filter"`,
		`id="session-delete-button"`,
		`id="capture-delete-button"`,
		`function inlineString(value)`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected html to contain %s", needle)
		}
	}
	for _, forbidden := range []string{
		`selectSession('${escHtml(`,
		`viewCapture('${escHtml(`,
		`jumpToCapture('${escHtml(`,
		`id="sitem-${CSS.escape(`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("expected html not to contain unsafe inline interpolation %s", forbidden)
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
	var captures []map[string]any
	if err := json.Unmarshal(capturesRecorder.Body.Bytes(), &captures); err != nil {
		t.Fatalf("parse captures response: %v", err)
	}
	if len(captures) != maxWebCaptures {
		t.Fatalf("expected at most %d captures, got %d", maxWebCaptures, len(captures))
	}

	searchRequest := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/search?q=needle", nil)
	searchRequest.SetPathValue("id", sessionID)
	searchRecorder := httptest.NewRecorder()
	srv.handleSearch(searchRecorder, searchRequest)
	if searchRecorder.Code != http.StatusOK {
		t.Fatalf("search: expected 200, got %d: %s", searchRecorder.Code, searchRecorder.Body.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(searchRecorder.Body.Bytes(), &results); err != nil {
		t.Fatalf("parse search response: %v", err)
	}
	if len(results) != maxWebSearchResults {
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

	var results []map[string]any
	if err := json.Unmarshal(searchRec.Body.Bytes(), &results); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(results) != 1 {
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

			var results []map[string]any
			json.Unmarshal(searchRec.Body.Bytes(), &results)
			if tt.wantResult && len(results) == 0 {
				t.Fatalf("expected results for mode %q with query %q", tt.mode, tt.query)
			}
		})
	}
}

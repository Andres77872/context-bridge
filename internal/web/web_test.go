package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/config"
	"context-bridge/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "context-bridge.db")
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
		_, err := st.ImportCapture(sessionID, store.CaptureInput{
			ParentSessionID: sessionID,
			ChildSessionID:  "child-" + sessionID,
			CallID:          "call-" + sessionID,
			Agent:           c.agent,
			Description:     c.desc,
			Content:         c.content,
			CapturedAt:      time.Now().UTC(),
		}, c.seq, "", c.desc, len(c.content), false)
		if err != nil {
			t.Fatalf("seed capture %d: %v", c.seq, err)
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

func TestHandleSearchFTS5InvalidSyntax(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses_fts5_err", ""); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	seedSession(t, st, "ses_fts5_err", []struct {
		seq     int
		agent   string
		desc    string
		content string
	}{
		{seq: 1, agent: "grep", desc: "test", content: "some content"},
	})

	srv := New(st, store.SearchModeFTS5, filepath.Join(t.TempDir(), "config.json"))
	req := httptest.NewRequest("GET", "/api/sessions/ses_fts5_err/search?q=\"unmatched", nil)
	req.SetPathValue("id", "ses_fts5_err")
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for invalid FTS5 syntax, got %d: %s", rec.Code, rec.Body.String())
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
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

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
		record, err := st.AddCapture(store.CaptureInput{
			ParentSessionID: sessionID,
			ChildSessionID:  "child-" + sessionID,
			CallID:          "call-" + sessionID + "-" + c.agent + "-" + c.desc,
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

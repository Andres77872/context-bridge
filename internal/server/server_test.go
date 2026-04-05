package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/store"
)

func TestHealthEndpoint(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	decodeJSON(t, rec.Body.Bytes(), &body)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected ok=true, got %v", body)
	}
}

func TestSessionCreatedEventLinksChildToRoot(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex)

	rec := postJSON(t, srv.Routes(), "/events", map[string]any{
		"type": "session.created",
		"properties": map[string]any{
			"info": map[string]any{
				"id":       "ses-child",
				"parentID": "ses-root",
			},
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	root, err := st.ResolveRoot("ses-child")
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if root != "ses-root" {
		t.Fatalf("expected root ses-root, got %s", root)
	}
}

func TestSessionDeletedEventMarksSessionEnded(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex)

	if err := st.EnsureSession("ses-root", ""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	rec := postJSON(t, srv.Routes(), "/events", map[string]any{
		"type": "session.deleted",
		"properties": map[string]any{
			"info": map[string]any{"id": "ses-root"},
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	sessions, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt == nil {
		t.Fatal("expected ended_at to be set")
	}
	if sessions[0].DeletedAt != nil {
		t.Fatal("expected deleted_at to remain nil")
	}
}

func TestCaptureEndpointIngestsAndDedupes(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex)
	capturedAt := time.Date(2026, 3, 22, 14, 0, 0, 0, time.UTC)

	payload := map[string]any{
		"parent_session_id": "ses-root",
		"child_session_id":  "ses-child",
		"call_id":           "call-1",
		"agent":             "grep",
		"description":       "Inspect auth flow",
		"content":           "auth capture body",
		"captured_at":       capturedAt.Format(time.RFC3339Nano),
	}

	first := postJSON(t, srv.Routes(), "/capture", payload)
	if first.Code != http.StatusOK {
		t.Fatalf("first capture expected 200, got %d: %s", first.Code, first.Body.String())
	}
	second := postJSON(t, srv.Routes(), "/capture", payload)
	if second.Code != http.StatusOK {
		t.Fatalf("second capture expected 200, got %d: %s", second.Code, second.Body.String())
	}

	var firstBody struct {
		OK  bool `json:"ok"`
		Seq int  `json:"seq"`
	}
	decodeJSON(t, first.Body.Bytes(), &firstBody)
	if !firstBody.OK || firstBody.Seq != 1 {
		t.Fatalf("unexpected first capture response: %+v", firstBody)
	}

	captures, err := st.ListCaptures("ses-child", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("expected 1 deduped capture, got %d", len(captures))
	}
	if !captures[0].CapturedAt.Equal(capturedAt) {
		t.Fatalf("expected captured_at %s, got %s", capturedAt, captures[0].CapturedAt)
	}
}

func TestHintEndpointReturnsRenderedHint(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, store.SearchModeRegex)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Map codebase",
		content:        "capture content",
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hint?session_id=ses-child", nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Text string `json:"text"`
	}
	decodeJSON(t, rec.Body.Bytes(), &body)
	if !strings.Contains(body.Text, "[#1] [grep] Map codebase") {
		t.Fatalf("expected numbered hint entry, got %q", body.Text)
	}
	if !strings.Contains(body.Text, "read") {
		t.Fatalf("expected MCP guidance in hint, got %q", body.Text)
	}
}

type seededCapture struct {
	childSessionID string
	callID         string
	agent          string
	description    string
	content        string
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "server.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedCapture(t *testing.T, st *store.Store, sessionID string, seq int, capturedAt time.Time, capture seededCapture) {
	t.Helper()
	record, err := st.AddCapture(store.CaptureInput{
		ParentSessionID: sessionID,
		ChildSessionID:  capture.childSessionID,
		CallID:          capture.callID,
		Agent:           capture.agent,
		Description:     capture.description,
		Content:         capture.content,
		CapturedAt:      capturedAt,
	})
	if err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if record.Seq != seq {
		t.Fatalf("expected seq %d, got %d", seq, record.Seq)
	}
}

func postJSON(t *testing.T, handler http.Handler, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, payload []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode json: %v\npayload: %s", err, string(payload))
	}
}

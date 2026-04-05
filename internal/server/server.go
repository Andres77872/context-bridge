package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"context-bridge/internal/store"
)

type Server struct {
	store      *store.Store
	mux        *http.ServeMux
	searchMode store.SearchMode
}

func New(st *store.Store, searchMode store.SearchMode) *Server {
	s := &Server{store: st, mux: http.NewServeMux(), searchMode: searchMode}
	s.routes()
	return s
}

func (s *Server) Routes() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /events", s.handleEvent)
	s.mux.HandleFunc("POST /capture", s.handleCapture)
	s.mux.HandleFunc("GET /hint", s.handleHint)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type eventPayload struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type sessionInfo struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	var payload eventPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	info := decodeSessionInfo(payload.Properties)
	if strings.TrimSpace(info.ID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}

	var err error
	switch payload.Type {
	case "session.created":
		err = s.store.EnsureSession(info.ID, info.ParentID)
	case "session.deleted":
		err = s.store.MarkSessionDeleted(info.ID)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true})
		return
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type capturePayload struct {
	ParentSessionID string `json:"parent_session_id"`
	ChildSessionID  string `json:"child_session_id"`
	CallID          string `json:"call_id"`
	Agent           string `json:"agent"`
	Description     string `json:"description"`
	Content         string `json:"content"`
	CapturedAt      string `json:"captured_at"`
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var payload capturePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	capturedAt := time.Now().UTC()
	if strings.TrimSpace(payload.CapturedAt) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, payload.CapturedAt); err == nil {
			capturedAt = parsed.UTC()
		}
	}

	record, err := s.store.AddCapture(store.CaptureInput{
		ParentSessionID: payload.ParentSessionID,
		ChildSessionID:  payload.ChildSessionID,
		CallID:          payload.CallID,
		Agent:           payload.Agent,
		Description:     payload.Description,
		Content:         payload.Content,
		CapturedAt:      capturedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": record.ID, "seq": record.Seq})
}

func (s *Server) handleHint(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if strings.TrimSpace(sessionID) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"text": ""})
		return
	}

	hint, err := s.store.RenderHint(sessionID, s.searchMode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"text": hint})
}

func decodeSessionInfo(raw json.RawMessage) sessionInfo {
	var wrapped struct {
		Info sessionInfo `json:"info"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && strings.TrimSpace(wrapped.Info.ID) != "" {
		return wrapped.Info
	}

	var info sessionInfo
	_ = json.Unmarshal(raw, &info)
	return info
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
}

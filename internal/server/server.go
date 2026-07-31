package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"context-bridge/internal/store"
)

const (
	maxEventRequestBytes   = 1024 * 1024
	maxCaptureRequestBytes = 8 * 1024 * 1024
)

type Server struct {
	store      *store.Store
	mux        *http.ServeMux
	searchMode store.SearchMode
	version    string
	shutdown   func()
}

func New(st *store.Store, searchMode store.SearchMode) *Server {
	return NewWithVersion(st, searchMode, "dev")
}

func NewWithVersion(st *store.Store, searchMode store.SearchMode, version string) *Server {
	return NewWithVersionAndShutdown(st, searchMode, version, nil)
}

func NewWithVersionAndShutdown(st *store.Store, searchMode store.SearchMode, version string, shutdown func()) *Server {
	s := &Server{store: st, mux: http.NewServeMux(), searchMode: searchMode, version: version, shutdown: shutdown}
	s.routes()
	return s
}

func (s *Server) Routes() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	if s.shutdown != nil {
		s.mux.HandleFunc("POST /shutdown", s.handleShutdown)
	}
	s.mux.HandleFunc("POST /events", s.handleEvent)
	s.mux.HandleFunc("POST /capture", s.handleCapture)
	s.mux.HandleFunc("GET /hint", s.handleHint)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	var payload struct{}
	if !decodeLocalJSON(w, r, 1024, &payload) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shutdown": true})
	s.shutdown()
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"service":  "context-bridge",
		"protocol": 1,
		"version":  s.version,
	})
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
	if !decodeLocalJSON(w, r, maxEventRequestBytes, &payload) {
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
		err = s.store.MarkSessionEnded(info.ID)
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
	if !decodeLocalJSON(w, r, maxCaptureRequestBytes, &payload) {
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

func decodeLocalJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	if strings.TrimSpace(r.Header.Get("Origin")) != "" {
		writeError(w, http.StatusForbidden, errors.New("browser-origin requests are not accepted"))
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
			return false
		}
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("request body must contain exactly one JSON value")
		}
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
}

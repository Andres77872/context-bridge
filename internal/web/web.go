package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"context-bridge/internal/config"
	"context-bridge/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	store      *store.Store
	searchMode store.SearchMode
	configPath string
	mu         sync.RWMutex
	mux        *http.ServeMux
}

func New(st *store.Store, searchMode store.SearchMode, configPath string) *Server {
	s := &Server{store: st, searchMode: searchMode, configPath: configPath, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Routes() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)
	s.mux.HandleFunc("GET /api/sessions", s.handleSessions)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	s.mux.HandleFunc("GET /api/sessions/{id}/captures", s.handleCaptures)
	s.mux.HandleFunc("DELETE /api/sessions/{id}/captures/{seq}", s.handleDeleteCapture)
	s.mux.HandleFunc("GET /api/sessions/{id}/captures/{seq}", s.handleCapture)
	s.mux.HandleFunc("GET /api/sessions/{id}/search", s.handleSearch)

	sub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		content, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	})
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

type configResponse struct {
	SearchMode    string   `json:"search_mode"`
	Scope         string   `json:"scope"`
	Immediate     []string `json:"immediate"`
	RestartNeeded []string `json:"restart_required"`
}

type actionResponse struct {
	OK        bool   `json:"ok"`
	Action    string `json:"action"`
	SessionID string `json:"session_id,omitempty"`
	Seq       int    `json:"seq,omitempty"`
}

func (s *Server) currentSearchMode() store.SearchMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.searchMode
}

func (s *Server) setSearchMode(mode store.SearchMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchMode = mode
}

func (s *Server) currentConfigResponse() configResponse {
	return configResponse{
		SearchMode:    string(s.currentSearchMode()),
		Scope:         "global",
		Immediate:     []string{"web"},
		RestartNeeded: []string{"tui", "mcp"},
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cors(w)
	writeJSON(w, http.StatusOK, s.currentConfigResponse())
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if s.configPath == "" {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("config path is unavailable"))
		return
	}

	var req config.Config
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid config payload: %w", err))
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := config.SaveConfig(s.configPath, req); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.setSearchMode(store.SearchMode(req.SearchMode))
	writeJSON(w, http.StatusOK, s.currentConfigResponse())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	cors(w)
	stats, err := s.store.Stats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":    stats.Sessions,
		"captures":    stats.Captures,
		"total_bytes": stats.TotalBytes,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	cors(w)
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	sessions, err := s.store.ListRootSessions(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if sessions == nil {
		sessions = []store.SessionSummary{}
	}

	type sessionItem struct {
		ID             string     `json:"id"`
		CreatedAt      time.Time  `json:"created_at"`
		LastCapturedAt time.Time  `json:"last_captured_at"`
		CaptureCount   int        `json:"capture_count"`
		DeletedAt      *time.Time `json:"deleted_at,omitempty"`
		EndedAt        *time.Time `json:"ended_at,omitempty"`
	}
	items := make([]sessionItem, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{
			ID:             s.ID,
			CreatedAt:      s.CreatedAt,
			LastCapturedAt: s.LastCapturedAt,
			CaptureCount:   s.CaptureCount,
			DeletedAt:      s.DeletedAt,
			EndedAt:        s.EndedAt,
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("session id is required"))
		return
	}

	exists, err := s.rootSessionExists(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, fmt.Errorf("session not found: %s", id))
		return
	}

	if err := s.store.MarkSessionDeleted(id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, actionResponse{OK: true, Action: "delete_session", SessionID: id})
}

func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := r.PathValue("id")
	agent := r.URL.Query().Get("agent")

	captures, err := s.store.ListCaptures(id, agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	type captureListItem struct {
		ID             int64      `json:"id"`
		SessionID      string     `json:"session_id"`
		Seq            int        `json:"seq"`
		ChildSessionID string     `json:"child_session_id,omitempty"`
		Agent          string     `json:"agent"`
		Description    string     `json:"description"`
		Preview        string     `json:"preview"`
		Bytes          int        `json:"bytes"`
		CapturedAt     time.Time  `json:"captured_at"`
		DeletedAt      *time.Time `json:"deleted_at,omitempty"`
		EndedAt        *time.Time `json:"ended_at,omitempty"`
	}

	items := make([]captureListItem, len(captures))
	for i, c := range captures {
		items[i] = captureListItem{
			ID:             c.ID,
			SessionID:      c.SessionID,
			Seq:            c.Seq,
			ChildSessionID: c.ChildSessionID,
			Agent:          c.Agent,
			Description:    c.Description,
			Preview:        c.Preview,
			Bytes:          c.Bytes,
			CapturedAt:     c.CapturedAt,
			DeletedAt:      c.DeletedAt,
			EndedAt:        c.EndedAt,
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleDeleteCapture(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := strings.TrimSpace(r.PathValue("id"))
	seqStr := r.PathValue("seq")

	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %s", seqStr))
		return
	}

	if _, err := s.store.GetCaptureBySeq(id, seq); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.DeleteCapture(id, seq); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, actionResponse{OK: true, Action: "delete_capture", SessionID: id, Seq: seq})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := r.PathValue("id")
	seqStr := r.PathValue("seq")

	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %s", seqStr))
		return
	}

	capture, err := s.store.GetCaptureBySeq(id, seq)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	type captureDetail struct {
		ID             int64      `json:"id"`
		SessionID      string     `json:"session_id"`
		Seq            int        `json:"seq"`
		ChildSessionID string     `json:"child_session_id,omitempty"`
		Agent          string     `json:"agent"`
		Description    string     `json:"description"`
		Preview        string     `json:"preview"`
		Content        string     `json:"content"`
		Bytes          int        `json:"bytes"`
		CapturedAt     time.Time  `json:"captured_at"`
		DeletedAt      *time.Time `json:"deleted_at,omitempty"`
		EndedAt        *time.Time `json:"ended_at,omitempty"`
	}
	writeJSON(w, http.StatusOK, captureDetail{
		ID:             capture.ID,
		SessionID:      capture.SessionID,
		Seq:            capture.Seq,
		ChildSessionID: capture.ChildSessionID,
		Agent:          capture.Agent,
		Description:    capture.Description,
		Preview:        capture.Preview,
		Content:        capture.Content,
		Bytes:          capture.Bytes,
		CapturedAt:     capture.CapturedAt,
		DeletedAt:      capture.DeletedAt,
		EndedAt:        capture.EndedAt,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := r.PathValue("id")
	q := r.URL.Query().Get("q")
	contextLines := 3
	if v := r.URL.Query().Get("context"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			contextLines = n
		}
	}

	if strings.TrimSpace(q) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q parameter is required"))
		return
	}

	results, err := s.store.SearchWithMode(id, q, contextLines, s.currentSearchMode())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	type captureListItem struct {
		ID             int64      `json:"id"`
		SessionID      string     `json:"session_id"`
		Seq            int        `json:"seq"`
		ChildSessionID string     `json:"child_session_id,omitempty"`
		Agent          string     `json:"agent"`
		Description    string     `json:"description"`
		Preview        string     `json:"preview"`
		Bytes          int        `json:"bytes"`
		CapturedAt     time.Time  `json:"captured_at"`
		DeletedAt      *time.Time `json:"deleted_at,omitempty"`
		EndedAt        *time.Time `json:"ended_at,omitempty"`
	}
	type searchItem struct {
		Capture    captureListItem `json:"capture"`
		Snippet    string          `json:"snippet"`
		MatchCount int             `json:"match_count"`
	}

	items := make([]searchItem, len(results))
	for i, r := range results {
		items[i] = searchItem{
			Capture: captureListItem{
				ID:             r.Capture.ID,
				SessionID:      r.Capture.SessionID,
				Seq:            r.Capture.Seq,
				ChildSessionID: r.Capture.ChildSessionID,
				Agent:          r.Capture.Agent,
				Description:    r.Capture.Description,
				Preview:        r.Capture.Preview,
				Bytes:          r.Capture.Bytes,
				CapturedAt:     r.Capture.CapturedAt,
				DeletedAt:      r.Capture.DeletedAt,
				EndedAt:        r.Capture.EndedAt,
			},
			Snippet:    r.Snippet,
			MatchCount: r.MatchCount,
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) rootSessionExists(id string) (bool, error) {
	sessions, err := s.store.ListRootSessions(int(^uint(0) >> 1))
	if err != nil {
		return false, err
	}
	for _, session := range sessions {
		if session.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func Run(st *store.Store, addr, version string, searchMode store.SearchMode, configPath string, openBrowser bool) error {
	srv := New(st, searchMode, configPath)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("Context Bridge dashboard → http://%s\n", addr)

	if openBrowser {
		if err := openURL("http://" + addr); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to open browser: %v\n", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
}

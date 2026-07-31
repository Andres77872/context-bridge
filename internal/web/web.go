package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"context-bridge/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

const (
	maxConfigBodyBytes     = 8 << 10
	maxWebSessions         = 500
	maxWebCaptures         = 500
	maxWebSearchResults    = 100
	maxWebSearchMatches    = 2000
	maxWebSearchCandidates = 500
	maxWebContextLines     = 20
	maxWebQueryBytes       = 1024
	maxWebHeaderBytes      = 16 << 10

	// dashboardCSP is origin-locked: the dashboard ships every stylesheet,
	// script, and glyph it needs, so no external origin is allowed and the page
	// keeps working with no network access at all. script-src stays strict
	// ('self' only, no inline) because that is the boundary that matters for
	// XSS; style-src allows inline attributes because charts colour their marks
	// through style attributes, which cannot execute anything.
	dashboardCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
)

var errJSONContentType = errors.New("Content-Type must be application/json")

type Server struct {
	store       *store.Store
	searchMode  store.SearchMode
	configPath  string
	version     string
	accessToken string
	startedAt   time.Time
	mu          sync.RWMutex
	mux         *http.ServeMux
}

// New builds a dashboard server with an unknown build version.
func New(st *store.Store, searchMode store.SearchMode, configPath string) *Server {
	return NewWithVersion(st, searchMode, configPath, "dev")
}

// NewWithVersion builds a dashboard server that reports the running build.
func NewWithVersion(st *store.Store, searchMode store.SearchMode, configPath, version string) *Server {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		panic(fmt.Sprintf("generate dashboard access token: %v", err))
	}
	s := &Server{
		store:       st,
		searchMode:  searchMode,
		configPath:  configPath,
		version:     strings.TrimSpace(version),
		accessToken: hex.EncodeToString(tokenBytes),
		startedAt:   time.Now(),
		mux:         http.NewServeMux(),
	}
	if s.version == "" {
		s.version = "dev"
	}
	s.routes()
	return s
}

func (s *Server) Routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if !loopbackRequestHost(r.Host) {
			writeError(w, http.StatusForbidden, fmt.Errorf("non-loopback Host rejected"))
			return
		}
		if !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, fmt.Errorf("cross-origin dashboard request rejected"))
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Has("token") {
			if !secureTokenEqual(r.URL.Query().Get("token"), s.accessToken) {
				writeError(w, http.StatusForbidden, fmt.Errorf("invalid dashboard access token"))
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: "context_bridge_session", Value: s.accessToken, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie("context_bridge_session")
		if err != nil || !secureTokenEqual(cookie.Value, s.accessToken) {
			writeError(w, http.StatusForbidden, fmt.Errorf("dashboard access token required"))
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/meta", s.handleMeta)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/analytics", s.handleAnalytics)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)
	s.mux.HandleFunc("GET /api/search", s.handleGlobalSearch)
	s.mux.HandleFunc("GET /api/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleSessionDetail)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	s.mux.HandleFunc("GET /api/sessions/{id}/export", s.handleExportSession)
	s.mux.HandleFunc("GET /api/sessions/{id}/captures", s.handleCaptures)
	s.mux.HandleFunc("GET /api/sessions/{id}/captures/{seq}", s.handleCapture)
	s.mux.HandleFunc("GET /api/sessions/{id}/captures/{seq}/raw", s.handleCaptureRaw)
	s.mux.HandleFunc("DELETE /api/sessions/{id}/captures/{seq}", s.handleDeleteCapture)
	s.mux.HandleFunc("GET /api/sessions/{id}/search", s.handleSearch)
	// Agent view: the exact payloads the MCP tools return.
	s.mux.HandleFunc("GET /api/sessions/{id}/mcp/list", s.handleAgentList)
	s.mux.HandleFunc("GET /api/sessions/{id}/mcp/read/{seq}", s.handleAgentRead)
	s.mux.HandleFunc("GET /api/sessions/{id}/mcp/search", s.handleAgentSearch)

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

func sameOriginRequest(r *http.Request) bool {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return true
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme != "http" || origin.Host == "" || origin.User != nil {
		return false
	}
	if origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	return loopbackRequestHost(origin.Host) && strings.EqualFold(origin.Host, r.Host)
}

func loopbackRequestHost(hostPort string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(hostPort))
	if err != nil || strings.TrimSpace(port) == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secureTokenEqual(got, want string) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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

func Run(st *store.Store, addr, version string, searchMode store.SearchMode, configPath string, openBrowser bool) error {
	if err := validateLoopbackAddr(addr); err != nil {
		return err
	}
	srv := NewWithVersion(st, searchMode, configPath, version)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxWebHeaderBytes,
	}

	accessURL := fmt.Sprintf("http://%s/?token=%s", addr, srv.accessToken)
	fmt.Printf("Context Bridge dashboard → %s\n", accessURL)

	if openBrowser {
		if err := openURL(accessURL); err != nil {
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

func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("dashboard address must be a loopback host:port: %q", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback dashboard address %q", addr)
	}
	return nil
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
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": fmt.Sprintf("%v", err)})
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("Content-Security-Policy", dashboardCSP)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return errJSONContentType
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errJSONContentType
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

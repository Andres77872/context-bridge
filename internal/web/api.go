package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"context-bridge/internal/config"
	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/store"
)

// ── response shapes ─────────────────────────────────────────────────────────
//
// Every handler below returns one of these types. They are declared once so
// the capture shape stays identical across listing, search, and detail.

type sessionItem struct {
	ID              string     `json:"id"`
	CreatedAt       time.Time  `json:"created_at"`
	FirstCapturedAt *time.Time `json:"first_captured_at,omitempty"`
	LastCapturedAt  time.Time  `json:"last_captured_at"`
	CaptureCount    int        `json:"capture_count"`
	Bytes           int64      `json:"bytes"`
	AgentCount      int        `json:"agent_count"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
}

type captureItem struct {
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

// captureDetailItem carries the stored document exactly as it sits in SQLite.
// The agent-facing rendering lives behind /api/sessions/{id}/mcp/read/{seq}.
type captureDetailItem struct {
	captureItem
	Content string `json:"content"`
	Lines   int    `json:"lines"`
}

func toCaptureDetailItem(record store.CaptureRecord) captureDetailItem {
	return captureDetailItem{
		captureItem: toCaptureItem(record),
		Content:     record.Content,
		Lines:       countLines(record.Content),
	}
}

// ── agent view ──────────────────────────────────────────────────────────────
//
// These endpoints return the exact text the MCP tools hand to the agent, by
// calling the same render functions the tool handlers call. The dashboard's
// whole purpose is inspecting what the model actually received, so it must not
// re-render captured output its own way.

type agentViewResponse struct {
	Tool      string `json:"tool"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Bytes     int    `json:"bytes"`
	Lines     int    `json:"lines"`
	MaxBytes  int    `json:"max_bytes"`
	Truncated bool   `json:"truncated"`
}

func writeAgentView(w http.ResponseWriter, tool, sessionID, text string) {
	maxBytes, _, _, _ := bridgeMCP.Limits()
	writeJSON(w, http.StatusOK, agentViewResponse{
		Tool:      tool,
		SessionID: sessionID,
		Text:      text,
		Bytes:     len([]byte(text)),
		Lines:     countLines(text),
		MaxBytes:  maxBytes,
		Truncated: strings.Contains(text, bridgeMCP.TruncationNotice),
	})
}

func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	text, err := bridgeMCP.RenderList(r.Context(), s.store, id, strings.TrimSpace(r.URL.Query().Get("agent")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeAgentView(w, "list", id, text)
}

func (s *Server) handleAgentRead(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %s", r.PathValue("seq")))
		return
	}
	text, err := bridgeMCP.RenderRead(r.Context(), s.store, id, seq)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeAgentView(w, "read", id, text)
}

func (s *Server) handleAgentSearch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	query := r.URL.Query().Get("q")
	if strings.TrimSpace(query) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q parameter is required"))
		return
	}
	if len([]byte(query)) > maxWebQueryBytes {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q parameter exceeds %d bytes", maxWebQueryBytes))
		return
	}

	contextLines := intParam(r, "context", bridgeMCP.DefaultSearchContextLines, maxWebContextLines)
	text, err := bridgeMCP.RenderSearch(r.Context(), s.store, id, query, contextLines, s.currentSearchMode())
	if err != nil {
		writeSearchError(w, err)
		return
	}
	writeAgentView(w, "search", id, text)
}

type capturePageResponse struct {
	Items    []captureItem `json:"items"`
	Total    int           `json:"total"`
	Filtered int           `json:"filtered"`
	Limit    int           `json:"limit"`
	Offset   int           `json:"offset"`
	Agents   []string      `json:"agents"`
	Order    string        `json:"order"`
}

type searchItem struct {
	Capture    captureItem `json:"capture"`
	Snippet    string      `json:"snippet"`
	MatchCount int         `json:"match_count"`
}

type searchResponse struct {
	Query        string       `json:"query"`
	Mode         string       `json:"mode"`
	Scope        string       `json:"scope"`
	SessionID    string       `json:"session_id,omitempty"`
	Agent        string       `json:"agent,omitempty"`
	ContextLines int          `json:"context_lines"`
	Results      []searchItem `json:"results"`
	TotalMatches int          `json:"total_matches"`
	Truncated    bool         `json:"truncated"`
	ElapsedMS    int64        `json:"elapsed_ms"`
}

type agentUsageItem struct {
	Agent          string     `json:"agent"`
	Captures       int        `json:"captures"`
	Bytes          int64      `json:"bytes"`
	Sessions       int        `json:"sessions"`
	LastCapturedAt *time.Time `json:"last_captured_at,omitempty"`
}

type sessionDetailResponse struct {
	Session         sessionItem      `json:"session"`
	Bytes           int64            `json:"bytes"`
	Agents          []agentUsageItem `json:"agents"`
	FirstCapturedAt *time.Time       `json:"first_captured_at,omitempty"`
	LastCapturedAt  *time.Time       `json:"last_captured_at,omitempty"`
	LargestBytes    int              `json:"largest_bytes"`
	ChildSessions   int              `json:"child_sessions"`
}

func toSessionItem(summary store.SessionSummary) sessionItem {
	item := sessionItem{
		ID:             summary.ID,
		CreatedAt:      summary.CreatedAt,
		LastCapturedAt: summary.LastCapturedAt,
		CaptureCount:   summary.CaptureCount,
		Bytes:          summary.Bytes,
		AgentCount:     summary.AgentCount,
		DeletedAt:      summary.DeletedAt,
		EndedAt:        summary.EndedAt,
	}
	if !summary.FirstCapturedAt.IsZero() {
		first := summary.FirstCapturedAt
		item.FirstCapturedAt = &first
	}
	return item
}

func toCaptureItem(record store.CaptureRecord) captureItem {
	return captureItem{
		ID:             record.ID,
		SessionID:      record.SessionID,
		Seq:            record.Seq,
		ChildSessionID: record.ChildSessionID,
		Agent:          record.Agent,
		Description:    record.Description,
		Preview:        record.Preview,
		Bytes:          record.Bytes,
		CapturedAt:     record.CapturedAt,
		DeletedAt:      record.DeletedAt,
		EndedAt:        record.EndedAt,
	}
}

func toAgentUsageItems(usage []store.AgentUsage) []agentUsageItem {
	items := make([]agentUsageItem, 0, len(usage))
	for _, item := range usage {
		entry := agentUsageItem{
			Agent:    item.Agent,
			Captures: item.Captures,
			Bytes:    item.Bytes,
			Sessions: item.Sessions,
		}
		if !item.LastCapturedAt.IsZero() {
			last := item.LastCapturedAt
			entry.LastCapturedAt = &last
		}
		items = append(items, entry)
	}
	return items
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t
	return &value
}

// ── query parsing helpers ───────────────────────────────────────────────────

func intParam(r *http.Request, name string, fallback, maxValue int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	if maxValue > 0 && value > maxValue {
		return maxValue
	}
	return value
}

func boolParam(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func trimmedParam(r *http.Request, name string, maxBytes int) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return "", fmt.Errorf("%s parameter exceeds %d bytes", name, maxBytes)
	}
	return value, nil
}

// ── meta and configuration ──────────────────────────────────────────────────

type limitsResponse struct {
	MaxSessions      int `json:"max_sessions"`
	MaxCaptures      int `json:"max_captures"`
	MaxSearchResults int `json:"max_search_results"`
	MaxQueryBytes    int `json:"max_query_bytes"`
	MaxContextLines  int `json:"max_context_lines"`
	MaxCaptureBytes  int `json:"max_capture_bytes"`
}

type metaResponse struct {
	Version       string         `json:"version"`
	SearchMode    string         `json:"search_mode"`
	RetentionDays int            `json:"retention_days"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	DatabasePath  string         `json:"database_path"`
	DatabaseBytes int64          `json:"database_bytes"`
	Limits        limitsResponse `json:"limits"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	meta := metaResponse{
		Version:       s.version,
		SearchMode:    string(s.currentSearchMode()),
		RetentionDays: store.RetentionWindowDays,
		StartedAt:     s.startedAt,
		UptimeSeconds: int64(now.Sub(s.startedAt).Seconds()),
		DatabasePath:  s.store.Path(),
		Limits: limitsResponse{
			MaxSessions:      maxWebSessions,
			MaxCaptures:      maxWebCaptures,
			MaxSearchResults: maxWebSearchResults,
			MaxQueryBytes:    maxWebQueryBytes,
			MaxContextLines:  maxWebContextLines,
			MaxCaptureBytes:  store.MaxCaptureContentBytes,
		},
	}
	if bytes, err := s.store.DiskUsage(); err == nil {
		meta.DatabaseBytes = bytes
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.currentConfigResponse())
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("config path is unavailable"))
		return
	}

	var req config.Config
	if err := decodeJSONBody(w, r, maxConfigBodyBytes, &req); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		case errors.Is(err, errJSONContentType):
			writeError(w, http.StatusUnsupportedMediaType, err)
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid config payload: %w", err))
		}
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

// ── stats and analytics ─────────────────────────────────────────────────────

// handleStats stays deliberately cheap: it is the endpoint the dashboard polls
// on a timer, so it must not run the full analytics sweep.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
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

type analyticsResponse struct {
	GeneratedAt   time.Time `json:"generated_at"`
	WindowDays    int       `json:"window_days"`
	RetentionDays int       `json:"retention_days"`

	Sessions        int   `json:"sessions"`
	ActiveSessions  int   `json:"active_sessions"`
	EndedSessions   int   `json:"ended_sessions"`
	DeletedSessions int   `json:"deleted_sessions"`
	Captures        int   `json:"captures"`
	Bytes           int64 `json:"bytes"`

	Captures24h int   `json:"captures_24h"`
	Bytes24h    int64 `json:"bytes_24h"`
	Captures7d  int   `json:"captures_7d"`
	Bytes7d     int64 `json:"bytes_7d"`

	AvgCaptureBytes    int64 `json:"avg_capture_bytes"`
	MedianCaptureBytes int64 `json:"median_capture_bytes"`
	P95CaptureBytes    int64 `json:"p95_capture_bytes"`

	FirstCapturedAt *time.Time `json:"first_captured_at,omitempty"`
	LastCapturedAt  *time.Time `json:"last_captured_at,omitempty"`

	LargestCapture *largestCaptureItem `json:"largest_capture,omitempty"`

	Agents      []agentUsageItem     `json:"agents"`
	Daily       []dailyBucketItem    `json:"daily"`
	Hourly      []hourlyBucketItem   `json:"hourly"`
	Heatmap     []heatCellItem       `json:"heatmap"`
	TopSessions []sessionUsageItem   `json:"top_sessions"`
	SizeBuckets []sizeBucketItem     `json:"size_buckets"`
	Storage     storageUsageResponse `json:"storage"`
}

type largestCaptureItem struct {
	SessionID   string    `json:"session_id"`
	Seq         int       `json:"seq"`
	Agent       string    `json:"agent"`
	Description string    `json:"description"`
	Bytes       int       `json:"bytes"`
	CapturedAt  time.Time `json:"captured_at"`
}

type dailyBucketItem struct {
	Day      string `json:"day"`
	Captures int    `json:"captures"`
	Bytes    int64  `json:"bytes"`
}

type hourlyBucketItem struct {
	Hour     int `json:"hour"`
	Captures int `json:"captures"`
}

type heatCellItem struct {
	Weekday  int `json:"weekday"`
	Hour     int `json:"hour"`
	Captures int `json:"captures"`
}

type sessionUsageItem struct {
	ID              string     `json:"id"`
	Captures        int        `json:"captures"`
	Bytes           int64      `json:"bytes"`
	Agents          int        `json:"agents"`
	FirstCapturedAt *time.Time `json:"first_captured_at,omitempty"`
	LastCapturedAt  *time.Time `json:"last_captured_at,omitempty"`
}

type sizeBucketItem struct {
	Label    string `json:"label"`
	MinBytes int    `json:"min_bytes"`
	MaxBytes int    `json:"max_bytes"`
	Captures int    `json:"captures"`
}

type storageUsageResponse struct {
	CaptureBytes  int64 `json:"capture_bytes"`
	DatabaseBytes int64 `json:"database_bytes"`
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	days := intParam(r, "days", store.RetentionWindowDays, store.RetentionWindowDays)
	analytics, err := s.store.AnalyticsContext(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	response := analyticsResponse{
		GeneratedAt:        analytics.GeneratedAt,
		WindowDays:         analytics.WindowDays,
		RetentionDays:      analytics.RetentionDays,
		Sessions:           analytics.Sessions,
		ActiveSessions:     analytics.ActiveSessions,
		EndedSessions:      analytics.EndedSessions,
		DeletedSessions:    analytics.DeletedSessions,
		Captures:           analytics.Captures,
		Bytes:              analytics.Bytes,
		Captures24h:        analytics.Captures24h,
		Bytes24h:           analytics.Bytes24h,
		Captures7d:         analytics.Captures7d,
		Bytes7d:            analytics.Bytes7d,
		AvgCaptureBytes:    analytics.AvgCaptureBytes,
		MedianCaptureBytes: analytics.MedianCaptureBytes,
		P95CaptureBytes:    analytics.P95CaptureBytes,
		FirstCapturedAt:    optionalTime(analytics.FirstCapturedAt),
		LastCapturedAt:     optionalTime(analytics.LastCapturedAt),
		Agents:             toAgentUsageItems(analytics.Agents),
		Storage: storageUsageResponse{
			CaptureBytes:  analytics.Bytes,
			DatabaseBytes: analytics.DiskBytes,
		},
	}

	response.Daily = make([]dailyBucketItem, 0, len(analytics.Daily))
	for _, bucket := range analytics.Daily {
		response.Daily = append(response.Daily, dailyBucketItem{
			Day:      bucket.Day.Format("2006-01-02"),
			Captures: bucket.Captures,
			Bytes:    bucket.Bytes,
		})
	}

	response.Hourly = make([]hourlyBucketItem, 0, len(analytics.Hourly))
	for _, bucket := range analytics.Hourly {
		response.Hourly = append(response.Hourly, hourlyBucketItem{Hour: bucket.Hour, Captures: bucket.Captures})
	}

	response.Heatmap = make([]heatCellItem, 0, len(analytics.Heatmap))
	for _, cell := range analytics.Heatmap {
		response.Heatmap = append(response.Heatmap, heatCellItem{Weekday: cell.Weekday, Hour: cell.Hour, Captures: cell.Captures})
	}

	response.TopSessions = make([]sessionUsageItem, 0, len(analytics.TopSessions))
	for _, item := range analytics.TopSessions {
		response.TopSessions = append(response.TopSessions, sessionUsageItem{
			ID:              item.ID,
			Captures:        item.Captures,
			Bytes:           item.Bytes,
			Agents:          item.Agents,
			FirstCapturedAt: optionalTime(item.FirstCapturedAt),
			LastCapturedAt:  optionalTime(item.LastCapturedAt),
		})
	}

	response.SizeBuckets = make([]sizeBucketItem, 0, len(analytics.SizeBuckets))
	for _, bucket := range analytics.SizeBuckets {
		response.SizeBuckets = append(response.SizeBuckets, sizeBucketItem{
			Label:    bucket.Label,
			MinBytes: bucket.MinBytes,
			MaxBytes: bucket.MaxBytes,
			Captures: bucket.Captures,
		})
	}

	if analytics.LargestCapture != nil {
		response.LargestCapture = &largestCaptureItem{
			SessionID:   analytics.LargestCapture.SessionID,
			Seq:         analytics.LargestCapture.Seq,
			Agent:       analytics.LargestCapture.Agent,
			Description: analytics.LargestCapture.Description,
			Bytes:       analytics.LargestCapture.Bytes,
			CapturedAt:  analytics.LargestCapture.CapturedAt,
		}
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgentsContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if agents == nil {
		agents = []string{}
	}
	writeJSON(w, http.StatusOK, agents)
}

// ── sessions ────────────────────────────────────────────────────────────────

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	query, err := trimmedParam(r, "q", maxWebQueryBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent, err := trimmedParam(r, "agent", maxWebQueryBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	sessions, err := s.store.ListRootSessionsContext(r.Context(), store.SessionListOptions{
		Limit:          intParam(r, "limit", 50, maxWebSessions),
		Query:          query,
		Agent:          agent,
		Sort:           strings.TrimSpace(r.URL.Query().Get("sort")),
		IncludeDeleted: boolParam(r, "include_deleted"),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	items := make([]sessionItem, 0, len(sessions))
	for _, summary := range sessions {
		items = append(items, toSessionItem(summary))
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("session id is required"))
		return
	}

	detail, err := s.store.SessionDetailContext(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionDetailResponse{
		Session:         toSessionItem(detail.Summary),
		Bytes:           detail.Bytes,
		Agents:          toAgentUsageItems(detail.Agents),
		FirstCapturedAt: optionalTime(detail.FirstCapturedAt),
		LastCapturedAt:  optionalTime(detail.LastCapturedAt),
		LargestBytes:    detail.LargestBytes,
		ChildSessions:   detail.ChildSessions,
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("session id is required"))
		return
	}

	exists, err := s.store.RootSessionExistsContext(r.Context(), id)
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

// ── captures ────────────────────────────────────────────────────────────────

func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	filter, err := trimmedParam(r, "q", maxWebQueryBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// A zero limit would disable the store-side bound, so it snaps to the cap.
	limit := intParam(r, "limit", maxWebCaptures, maxWebCaptures)
	if limit == 0 {
		limit = maxWebCaptures
	}
	offset := intParam(r, "offset", 0, 0)
	newest := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("order")), "desc")

	page, err := s.store.ListCapturesPageContext(r.Context(), store.CaptureListOptions{
		SessionID: id,
		Agent:     strings.TrimSpace(r.URL.Query().Get("agent")),
		Query:     filter,
		Newest:    newest,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	items := make([]captureItem, 0, len(page.Captures))
	for _, record := range page.Captures {
		items = append(items, toCaptureItem(record))
	}
	order := "asc"
	if newest {
		order = "desc"
	}
	writeJSON(w, http.StatusOK, capturePageResponse{
		Items:    items,
		Total:    page.Total,
		Filtered: page.Filtered,
		Limit:    limit,
		Offset:   offset,
		Agents:   page.Agents,
		Order:    order,
	})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	capture, ok := s.lookupCapture(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toCaptureDetailItem(*capture))
}

// handleCaptureRaw serves the capture body as a download so operators can pipe
// a captured output straight into another tool.
func (s *Server) handleCaptureRaw(w http.ResponseWriter, r *http.Request) {
	capture, ok := s.lookupCapture(w, r)
	if !ok {
		return
	}

	filename := fmt.Sprintf("%s-%d.txt", sanitizeFilename(capture.SessionID), capture.Seq)
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(capture.Content))
}

func (s *Server) handleDeleteCapture(w http.ResponseWriter, r *http.Request) {
	capture, ok := s.lookupCapture(w, r)
	if !ok {
		return
	}

	if err := s.store.DeleteCapture(capture.SessionID, capture.Seq); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, actionResponse{
		OK:        true,
		Action:    "delete_capture",
		SessionID: strings.TrimSpace(r.PathValue("id")),
		Seq:       capture.Seq,
	})
}

// lookupCapture resolves the {id}/{seq} path pair, writing the appropriate
// error response and reporting false when it cannot.
func (s *Server) lookupCapture(w http.ResponseWriter, r *http.Request) (*store.CaptureRecord, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	seqStr := r.PathValue("seq")

	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %s", seqStr))
		return nil, false
	}

	capture, err := s.store.GetCaptureBySeqContext(r.Context(), id, seq)
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	return capture, true
}

// ── search ──────────────────────────────────────────────────────────────────

// handleSearch serves the session-scoped route; the session comes from the
// path. handleGlobalSearch serves the same logic with the session as an
// optional query parameter so one implementation backs both routes.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.runSearch(w, r, strings.TrimSpace(r.PathValue("id")))
}

func (s *Server) handleGlobalSearch(w http.ResponseWriter, r *http.Request) {
	s.runSearch(w, r, strings.TrimSpace(r.URL.Query().Get("session")))
}

func (s *Server) runSearch(w http.ResponseWriter, r *http.Request, sessionID string) {
	query := r.URL.Query().Get("q")
	if strings.TrimSpace(query) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q parameter is required"))
		return
	}
	if len([]byte(query)) > maxWebQueryBytes {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q parameter exceeds %d bytes", maxWebQueryBytes))
		return
	}

	contextLines := intParam(r, "context", 3, maxWebContextLines)
	limit := intParam(r, "limit", maxWebSearchResults, maxWebSearchResults)
	if limit == 0 {
		limit = maxWebSearchResults
	}
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	mode := s.currentSearchMode()

	started := time.Now()
	results, err := s.store.SearchContext(r.Context(), query, store.SearchOptions{
		SessionID:     sessionID,
		Agent:         agent,
		Mode:          mode,
		ContextLines:  contextLines,
		MaxResults:    limit,
		MaxMatches:    maxWebSearchMatches,
		MaxCandidates: maxWebSearchCandidates,
	})
	if err != nil {
		writeSearchError(w, err)
		return
	}

	items := make([]searchItem, 0, len(results))
	totalMatches := 0
	for _, result := range results {
		totalMatches += result.MatchCount
		items = append(items, searchItem{
			Capture:    toCaptureItem(result.Capture),
			Snippet:    result.Snippet,
			MatchCount: result.MatchCount,
		})
	}

	scope := "all"
	if sessionID != "" {
		scope = "session"
	}
	writeJSON(w, http.StatusOK, searchResponse{
		Query:        query,
		Mode:         string(mode),
		Scope:        scope,
		SessionID:    sessionID,
		Agent:        agent,
		ContextLines: contextLines,
		Results:      items,
		TotalMatches: totalMatches,
		Truncated:    len(items) >= limit || totalMatches >= maxWebSearchMatches,
		ElapsedMS:    time.Since(started).Milliseconds(),
	})
}

// ── export ──────────────────────────────────────────────────────────────────

type sessionExport struct {
	ExportedAt time.Time           `json:"exported_at"`
	Version    string              `json:"version"`
	Session    sessionItem         `json:"session"`
	Captures   []captureDetailItem `json:"captures"`
}

// handleExportSession bundles a session and its captures into a single
// downloadable JSON document.
func (s *Server) handleExportSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("session id is required"))
		return
	}

	detail, err := s.store.SessionDetailContext(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	captures, err := s.store.ListCapturesContext(r.Context(), id, "", maxWebCaptures)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	export := sessionExport{
		ExportedAt: time.Now().UTC(),
		Version:    s.version,
		Session:    toSessionItem(detail.Summary),
		Captures:   make([]captureDetailItem, 0, len(captures)),
	}
	for _, record := range captures {
		full, err := s.store.GetCaptureBySeqContext(r.Context(), detail.Summary.ID, record.Seq)
		if err != nil {
			continue
		}
		export.Captures = append(export.Captures, toCaptureDetailItem(*full))
	}

	filename := fmt.Sprintf("%s-export.json", sanitizeFilename(detail.Summary.ID))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	writeJSON(w, http.StatusOK, export)
}

// ── shared error mapping ────────────────────────────────────────────────────

// writeStoreError maps store failures onto HTTP status codes. Missing rows are
// 404, malformed identifiers and filters are 400, everything else is 500.
func writeStoreError(w http.ResponseWriter, err error) {
	message := err.Error()
	switch {
	case strings.Contains(message, "not found"):
		writeError(w, http.StatusNotFound, err)
	case strings.Contains(message, "unsupported characters"),
		strings.Contains(message, "must not be negative"),
		strings.Contains(message, "is too long"),
		strings.Contains(message, "must not be empty"),
		strings.Contains(message, "exceeds"):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// writeSearchError keeps user-authored query mistakes out of the 500 bucket so
// the dashboard can show them inline instead of as an outage.
func writeSearchError(w http.ResponseWriter, err error) {
	message := err.Error()
	if strings.Contains(message, "invalid regex pattern") ||
		strings.Contains(message, "fts5") ||
		strings.Contains(message, "query is required") {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeStoreError(w, err)
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// sanitizeFilename keeps only characters that are safe inside a quoted
// Content-Disposition filename.
func sanitizeFilename(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	name := strings.Trim(builder.String(), "-.")
	if name == "" {
		return "session"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

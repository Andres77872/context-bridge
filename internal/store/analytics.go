package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// RetentionWindowDays mirrors captureRetention for callers that need to explain
// the read window to a user. Every aggregate below is scoped to it because
// pruning removes anything older.
const RetentionWindowDays = int(captureRetention / (24 * time.Hour))

const (
	maxAnalyticsWindowDays = RetentionWindowDays
	maxTopSessions         = 10
	maxTopAgents           = 24
)

// AgentUsage aggregates one agent's footprint inside an analytics window.
type AgentUsage struct {
	Agent          string
	Captures       int
	Bytes          int64
	Sessions       int
	LastCapturedAt time.Time
}

// ActivityBucket is a single day of capture volume. Buckets are dense: days
// with no captures are present with zero counts so charts keep a real x axis.
type ActivityBucket struct {
	Day      time.Time
	Captures int
	Bytes    int64
}

// HourUsage counts captures for one hour of the day (UTC).
type HourUsage struct {
	Hour     int
	Captures int
}

// HeatCell counts captures for one weekday/hour pair (UTC). Sunday is 0.
type HeatCell struct {
	Weekday  int
	Hour     int
	Captures int
}

// SessionUsage summarises one session's contribution to the window.
type SessionUsage struct {
	ID              string
	Captures        int
	Bytes           int64
	Agents          int
	FirstCapturedAt time.Time
	LastCapturedAt  time.Time
}

// SizeBucket counts captures whose byte size falls in [MinBytes, MaxBytes).
// A zero MaxBytes means unbounded.
type SizeBucket struct {
	Label    string
	MinBytes int
	MaxBytes int
	Captures int
}

// CaptureRef points at a capture without carrying its content.
type CaptureRef struct {
	SessionID   string
	Seq         int
	Agent       string
	Description string
	Bytes       int
	CapturedAt  time.Time
}

// Analytics is the full aggregate powering the dashboard usage views.
type Analytics struct {
	GeneratedAt   time.Time
	WindowDays    int
	RetentionDays int

	Sessions        int
	ActiveSessions  int
	EndedSessions   int
	DeletedSessions int
	Captures        int
	Bytes           int64

	Captures24h int
	Bytes24h    int64
	Captures7d  int
	Bytes7d     int64

	AvgCaptureBytes    int64
	MedianCaptureBytes int64
	P95CaptureBytes    int64
	LargestCapture     *CaptureRef

	FirstCapturedAt time.Time
	LastCapturedAt  time.Time

	Agents      []AgentUsage
	Daily       []ActivityBucket
	Hourly      []HourUsage
	Heatmap     []HeatCell
	TopSessions []SessionUsage
	SizeBuckets []SizeBucket

	DiskBytes int64
}

// captureScopeSQL restricts a query to live captures on live root sessions
// inside the retention window. The single bound parameter is the SQLite
// modifier for the window, e.g. "-30 days".
const captureScopeSQL = `
	FROM captures c
	JOIN sessions s ON s.id = c.session_id
	WHERE s.parent_id IS NULL AND s.deleted_at IS NULL
	  AND julianday(c.created_at) >= julianday('now', ?)
`

func windowModifier(days int) string {
	return fmt.Sprintf("-%d days", days)
}

func clampWindowDays(days int) int {
	if days <= 0 {
		return maxAnalyticsWindowDays
	}
	if days > maxAnalyticsWindowDays {
		return maxAnalyticsWindowDays
	}
	return days
}

// AnalyticsContext computes dashboard usage aggregates over the last
// windowDays days. windowDays is clamped to the retention window because older
// captures no longer exist.
func (s *Store) AnalyticsContext(ctx context.Context, windowDays int) (Analytics, error) {
	windowDays = clampWindowDays(windowDays)
	now := time.Now().UTC()
	window := windowModifier(windowDays)

	result := Analytics{
		GeneratedAt:   now,
		WindowDays:    windowDays,
		RetentionDays: RetentionWindowDays,
	}

	if err := s.readSessionCounts(ctx, window, &result); err != nil {
		return Analytics{}, err
	}
	if err := s.readCaptureTotals(ctx, window, &result); err != nil {
		return Analytics{}, err
	}
	if err := s.readByteDistribution(ctx, window, &result); err != nil {
		return Analytics{}, err
	}
	agents, err := s.readAgentUsage(ctx, window)
	if err != nil {
		return Analytics{}, err
	}
	result.Agents = agents

	daily, err := s.readDailyActivity(ctx, window, windowDays, now)
	if err != nil {
		return Analytics{}, err
	}
	result.Daily = daily

	hourly, heatmap, err := s.readActivityRhythm(ctx, window)
	if err != nil {
		return Analytics{}, err
	}
	result.Hourly = hourly
	result.Heatmap = heatmap

	top, err := s.readTopSessions(ctx, window)
	if err != nil {
		return Analytics{}, err
	}
	result.TopSessions = top

	buckets, err := s.readSizeBuckets(ctx, window)
	if err != nil {
		return Analytics{}, err
	}
	result.SizeBuckets = buckets

	largest, err := s.readLargestCapture(ctx, window)
	if err != nil {
		return Analytics{}, err
	}
	result.LargestCapture = largest

	if disk, err := s.DiskUsage(); err == nil {
		result.DiskBytes = disk
	}

	return result, nil
}

func (s *Store) readSessionCounts(ctx context.Context, window string, out *Analytics) error {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN s.ended_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM sessions s
		WHERE s.parent_id IS NULL AND s.deleted_at IS NULL
	`)
	if err := row.Scan(&out.Sessions, &out.EndedSessions); err != nil {
		return err
	}

	row = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions WHERE parent_id IS NULL AND deleted_at IS NOT NULL
	`)
	if err := row.Scan(&out.DeletedSessions); err != nil {
		return err
	}

	row = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT c.session_id)`+captureScopeSQL, window)
	return row.Scan(&out.ActiveSessions)
}

func (s *Store) readCaptureTotals(ctx context.Context, window string, out *Analytics) error {
	var firstAt, lastAt sql.NullString
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(c.id),
			COALESCE(SUM(c.bytes), 0),
			COALESCE(SUM(CASE WHEN julianday(c.captured_at) >= julianday('now', '-1 day') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN julianday(c.captured_at) >= julianday('now', '-1 day') THEN c.bytes ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN julianday(c.captured_at) >= julianday('now', '-7 days') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN julianday(c.captured_at) >= julianday('now', '-7 days') THEN c.bytes ELSE 0 END), 0),
			MIN(c.captured_at),
			MAX(c.captured_at)
		`+captureScopeSQL, window)
	if err := row.Scan(
		&out.Captures, &out.Bytes,
		&out.Captures24h, &out.Bytes24h,
		&out.Captures7d, &out.Bytes7d,
		&firstAt, &lastAt,
	); err != nil {
		return err
	}
	if firstAt.Valid {
		out.FirstCapturedAt = parseDBTime(firstAt.String)
	}
	if lastAt.Valid {
		out.LastCapturedAt = parseDBTime(lastAt.String)
	}
	if out.Captures > 0 {
		out.AvgCaptureBytes = out.Bytes / int64(out.Captures)
	}
	return nil
}

// readByteDistribution fills median and p95 using offset selection, which is
// exact and cheap for the bounded capture counts this store keeps.
func (s *Store) readByteDistribution(ctx context.Context, window string, out *Analytics) error {
	if out.Captures == 0 {
		return nil
	}
	percentile := func(fraction float64) (int64, error) {
		offset := int(float64(out.Captures-1) * fraction)
		var value int64
		row := s.db.QueryRowContext(ctx, `SELECT c.bytes`+captureScopeSQL+` ORDER BY c.bytes ASC LIMIT 1 OFFSET ?`, window, offset)
		if err := row.Scan(&value); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		return value, nil
	}

	median, err := percentile(0.5)
	if err != nil {
		return err
	}
	p95, err := percentile(0.95)
	if err != nil {
		return err
	}
	out.MedianCaptureBytes = median
	out.P95CaptureBytes = p95
	return nil
}

func (s *Store) readAgentUsage(ctx context.Context, window string) ([]AgentUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.agent, COUNT(c.id), COALESCE(SUM(c.bytes), 0), COUNT(DISTINCT c.session_id), MAX(c.captured_at)
		`+captureScopeSQL+`
		GROUP BY c.agent
		ORDER BY COUNT(c.id) DESC, c.agent ASC
		LIMIT ?
	`, window, maxTopAgents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	usage := []AgentUsage{}
	for rows.Next() {
		var item AgentUsage
		var lastAt sql.NullString
		if err := rows.Scan(&item.Agent, &item.Captures, &item.Bytes, &item.Sessions, &lastAt); err != nil {
			return nil, err
		}
		if lastAt.Valid {
			item.LastCapturedAt = parseDBTime(lastAt.String)
		}
		usage = append(usage, item)
	}
	return usage, rows.Err()
}

func (s *Store) readDailyActivity(ctx context.Context, window string, windowDays int, now time.Time) ([]ActivityBucket, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(c.captured_at), COUNT(c.id), COALESCE(SUM(c.bytes), 0)
		`+captureScopeSQL+`
		GROUP BY 1
	`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dayTotals struct {
		captures int
		bytes    int64
	}
	byDay := map[string]dayTotals{}
	for rows.Next() {
		var day sql.NullString
		var totals dayTotals
		if err := rows.Scan(&day, &totals.captures, &totals.bytes); err != nil {
			return nil, err
		}
		if day.Valid {
			byDay[day.String] = totals
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	buckets := make([]ActivityBucket, 0, windowDays)
	for offset := windowDays - 1; offset >= 0; offset-- {
		day := today.AddDate(0, 0, -offset)
		totals := byDay[day.Format("2006-01-02")]
		buckets = append(buckets, ActivityBucket{Day: day, Captures: totals.captures, Bytes: totals.bytes})
	}
	return buckets, nil
}

func (s *Store) readActivityRhythm(ctx context.Context, window string) ([]HourUsage, []HeatCell, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(strftime('%w', c.captured_at) AS INTEGER), CAST(strftime('%H', c.captured_at) AS INTEGER), COUNT(c.id)
		`+captureScopeSQL+`
		GROUP BY 1, 2
	`, window)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	hourly := make([]HourUsage, 24)
	for hour := range hourly {
		hourly[hour] = HourUsage{Hour: hour}
	}
	cells := make([]HeatCell, 0, 7*24)
	counts := map[int]int{}

	for rows.Next() {
		var weekday, hour sql.NullInt64
		var count int
		if err := rows.Scan(&weekday, &hour, &count); err != nil {
			return nil, nil, err
		}
		if !weekday.Valid || !hour.Valid {
			continue
		}
		w := int(weekday.Int64)
		h := int(hour.Int64)
		if w < 0 || w > 6 || h < 0 || h > 23 {
			continue
		}
		counts[w*24+h] += count
		hourly[h].Captures += count
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	for weekday := 0; weekday < 7; weekday++ {
		for hour := 0; hour < 24; hour++ {
			cells = append(cells, HeatCell{Weekday: weekday, Hour: hour, Captures: counts[weekday*24+hour]})
		}
	}
	return hourly, cells, nil
}

func (s *Store) readTopSessions(ctx context.Context, window string) ([]SessionUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.session_id, COUNT(c.id), COALESCE(SUM(c.bytes), 0), COUNT(DISTINCT c.agent), MIN(c.captured_at), MAX(c.captured_at)
		`+captureScopeSQL+`
		GROUP BY c.session_id
		ORDER BY COUNT(c.id) DESC, COALESCE(SUM(c.bytes), 0) DESC, c.session_id ASC
		LIMIT ?
	`, window, maxTopSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []SessionUsage{}
	for rows.Next() {
		var item SessionUsage
		var firstAt, lastAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Captures, &item.Bytes, &item.Agents, &firstAt, &lastAt); err != nil {
			return nil, err
		}
		if firstAt.Valid {
			item.FirstCapturedAt = parseDBTime(firstAt.String)
		}
		if lastAt.Valid {
			item.LastCapturedAt = parseDBTime(lastAt.String)
		}
		sessions = append(sessions, item)
	}
	return sessions, rows.Err()
}

// sizeBucketBounds defines the capture-size histogram shown in usage views.
var sizeBucketBounds = []SizeBucket{
	{Label: "< 1 KB", MinBytes: 0, MaxBytes: 1 << 10},
	{Label: "1–10 KB", MinBytes: 1 << 10, MaxBytes: 10 << 10},
	{Label: "10–50 KB", MinBytes: 10 << 10, MaxBytes: 50 << 10},
	{Label: "50–100 KB", MinBytes: 50 << 10, MaxBytes: 100 << 10},
	{Label: "≥ 100 KB", MinBytes: 100 << 10, MaxBytes: 0},
}

func (s *Store) readSizeBuckets(ctx context.Context, window string) ([]SizeBucket, error) {
	buckets := make([]SizeBucket, len(sizeBucketBounds))
	copy(buckets, sizeBucketBounds)

	rows, err := s.db.QueryContext(ctx, `SELECT c.bytes, COUNT(c.id)`+captureScopeSQL+` GROUP BY c.bytes`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var size, count int
		if err := rows.Scan(&size, &count); err != nil {
			return nil, err
		}
		for i := range buckets {
			if size >= buckets[i].MinBytes && (buckets[i].MaxBytes == 0 || size < buckets[i].MaxBytes) {
				buckets[i].Captures += count
				break
			}
		}
	}
	return buckets, rows.Err()
}

func (s *Store) readLargestCapture(ctx context.Context, window string) (*CaptureRef, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT c.session_id, c.seq, c.agent, c.description, c.bytes, c.captured_at
		`+captureScopeSQL+`
		ORDER BY c.bytes DESC, c.id ASC
		LIMIT 1
	`, window)

	var ref CaptureRef
	var capturedAt string
	if err := row.Scan(&ref.SessionID, &ref.Seq, &ref.Agent, &ref.Description, &ref.Bytes, &capturedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ref.CapturedAt = parseDBTime(capturedAt)
	return &ref, nil
}

// SessionDetail describes one root session in enough depth to render a header
// without pulling every capture body.
type SessionDetail struct {
	Summary         SessionSummary
	Bytes           int64
	Agents          []AgentUsage
	FirstCapturedAt time.Time
	LastCapturedAt  time.Time
	LargestBytes    int
	ChildSessions   int
}

// SessionDetailContext loads aggregates for a single root session.
func (s *Store) SessionDetailContext(ctx context.Context, sessionID string) (*SessionDetail, error) {
	rootID, err := s.ResolveRootContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	detail := &SessionDetail{Summary: SessionSummary{ID: rootID}}

	var createdAt string
	var deletedAt, endedAt sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT created_at, deleted_at, ended_at FROM sessions WHERE id = ?`, rootID)
	if err := row.Scan(&createdAt, &deletedAt, &endedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		return nil, err
	}
	detail.Summary.CreatedAt = parseDBTime(createdAt)
	if deletedAt.Valid {
		t := parseDBTime(deletedAt.String)
		detail.Summary.DeletedAt = &t
	}
	if endedAt.Valid {
		t := parseDBTime(endedAt.String)
		detail.Summary.EndedAt = &t
	}

	var firstAt, lastAt sql.NullString
	row = s.db.QueryRowContext(ctx, `
		SELECT COUNT(c.id), COALESCE(SUM(c.bytes), 0), COALESCE(MAX(c.bytes), 0), MIN(c.captured_at), MAX(c.captured_at)
		FROM captures c
		WHERE c.session_id = ?
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
	`, rootID)
	if err := row.Scan(&detail.Summary.CaptureCount, &detail.Bytes, &detail.LargestBytes, &firstAt, &lastAt); err != nil {
		return nil, err
	}
	if firstAt.Valid {
		detail.FirstCapturedAt = parseDBTime(firstAt.String)
	}
	if lastAt.Valid {
		detail.LastCapturedAt = parseDBTime(lastAt.String)
		detail.Summary.LastCapturedAt = detail.LastCapturedAt
	}
	detail.Summary.Bytes = detail.Bytes

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.agent, COUNT(c.id), COALESCE(SUM(c.bytes), 0), MAX(c.captured_at)
		FROM captures c
		WHERE c.session_id = ?
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
		GROUP BY c.agent
		ORDER BY COUNT(c.id) DESC, c.agent ASC
	`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	detail.Agents = []AgentUsage{}
	for rows.Next() {
		var item AgentUsage
		var lastCapturedAt sql.NullString
		if err := rows.Scan(&item.Agent, &item.Captures, &item.Bytes, &lastCapturedAt); err != nil {
			return nil, err
		}
		if lastCapturedAt.Valid {
			item.LastCapturedAt = parseDBTime(lastCapturedAt.String)
		}
		item.Sessions = 1
		detail.Agents = append(detail.Agents, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detail.Summary.AgentCount = len(detail.Agents)

	row = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE parent_id = ?`, rootID)
	if err := row.Scan(&detail.ChildSessions); err != nil {
		return nil, err
	}

	return detail, nil
}

// SessionListOptions controls which root sessions a listing returns.
type SessionListOptions struct {
	Limit          int
	Query          string
	Agent          string
	Sort           string
	IncludeDeleted bool
}

// Session sort keys accepted by ListRootSessionsContext.
const (
	SessionSortRecent   = "recent"
	SessionSortCaptures = "captures"
	SessionSortBytes    = "bytes"
	SessionSortCreated  = "created"
	SessionSortID       = "id"
)

func normalizeSessionSort(sortKey string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(sortKey)) {
	case "", SessionSortRecent:
		return `COALESCE(MAX(c.captured_at), s.created_at) DESC, s.created_at DESC`, nil
	case SessionSortCaptures:
		return `COUNT(c.id) DESC, COALESCE(MAX(c.captured_at), s.created_at) DESC`, nil
	case SessionSortBytes:
		return `COALESCE(SUM(c.bytes), 0) DESC, COUNT(c.id) DESC`, nil
	case SessionSortCreated:
		return `s.created_at DESC`, nil
	case SessionSortID:
		return `s.id ASC`, nil
	default:
		return "", fmt.Errorf("unsupported session sort %q", sortKey)
	}
}

// ListRootSessionsContext lists root sessions with per-session usage totals.
// Filters are applied in SQL so limits bound real work rather than trimming an
// already-materialised list.
func (s *Store) ListRootSessionsContext(ctx context.Context, opts SessionListOptions) ([]SessionSummary, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	orderBy, err := normalizeSessionSort(opts.Sort)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT
			s.id,
			s.created_at,
			s.deleted_at,
			s.ended_at,
			COUNT(c.id) AS capture_count,
			COALESCE(SUM(c.bytes), 0) AS total_bytes,
			COUNT(DISTINCT c.agent) AS agent_count,
			MIN(c.captured_at) AS first_captured_at,
			MAX(c.captured_at) AS last_captured_at
		FROM sessions s
		LEFT JOIN captures c ON c.session_id = s.id
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
		WHERE s.parent_id IS NULL
	`
	args := []any{}
	if !opts.IncludeDeleted {
		query += ` AND s.deleted_at IS NULL`
	}
	if filter := strings.TrimSpace(opts.Query); filter != "" {
		if len([]byte(filter)) > maxIdentifierBytes {
			return nil, errors.New("session filter is too long")
		}
		query += ` AND s.id LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLikePattern(filter)+"%")
	}
	if agent := strings.TrimSpace(opts.Agent); agent != "" {
		if len([]byte(agent)) > maxAgentBytes || !identifierPattern.MatchString(agent) {
			return nil, errors.New("agent filter contains unsupported characters or is too long")
		}
		query += ` AND EXISTS (SELECT 1 FROM captures ac WHERE ac.session_id = s.id AND ac.agent = ?)`
		args = append(args, agent)
	}
	query += `
		GROUP BY s.id, s.created_at, s.deleted_at, s.ended_at
		ORDER BY ` + orderBy + `
		LIMIT ?`
	args = append(args, opts.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var summary SessionSummary
		var createdAt string
		var deletedAt, endedAt, firstCapturedAt, lastCapturedAt sql.NullString

		if err := rows.Scan(
			&summary.ID, &createdAt, &deletedAt, &endedAt,
			&summary.CaptureCount, &summary.Bytes, &summary.AgentCount,
			&firstCapturedAt, &lastCapturedAt,
		); err != nil {
			return nil, err
		}

		summary.CreatedAt = parseDBTime(createdAt)
		if firstCapturedAt.Valid {
			summary.FirstCapturedAt = parseDBTime(firstCapturedAt.String)
		}
		if lastCapturedAt.Valid {
			summary.LastCapturedAt = parseDBTime(lastCapturedAt.String)
		}
		if deletedAt.Valid {
			t := parseDBTime(deletedAt.String)
			summary.DeletedAt = &t
		}
		if endedAt.Valid {
			t := parseDBTime(endedAt.String)
			summary.EndedAt = &t
		}

		sessions = append(sessions, summary)
	}

	return sessions, rows.Err()
}

// ListAgentsContext returns the distinct agents seen in the retention window,
// most active first. It powers agent filter menus without a full scan client
// side.
func (s *Store) ListAgentsContext(ctx context.Context) ([]string, error) {
	usage, err := s.readAgentUsage(ctx, windowModifier(RetentionWindowDays))
	if err != nil {
		return nil, err
	}
	agents := make([]string, 0, len(usage))
	for _, item := range usage {
		agents = append(agents, item.Agent)
	}
	sort.Strings(agents)
	return agents, nil
}

// DiskUsage reports the size of the SQLite database plus its sidecar files.
func (s *Store) DiskUsage() (int64, error) {
	if strings.TrimSpace(s.path) == "" {
		return 0, errors.New("database path is unknown")
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// Path returns the resolved SQLite database path.
func (s *Store) Path() string { return s.path }

// escapeLikePattern escapes the LIKE wildcards so user input matches
// literally. Callers must pair it with ESCAPE '\'.
func escapeLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

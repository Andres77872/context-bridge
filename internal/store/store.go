package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"context-bridge/internal/search"

	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 2

type Store struct {
	db *sql.DB
}

type CaptureInput struct {
	ParentSessionID string
	ChildSessionID  string
	CallID          string
	Agent           string
	Description     string
	Content         string
	CapturedAt      time.Time
}

type CaptureRecord struct {
	ID             int64
	SessionID      string
	Seq            int
	ChildSessionID string
	CallID         string
	Agent          string
	Description    string
	Preview        string
	Content        string
	Bytes          int
	SourcePath     string
	CapturedAt     time.Time
	DeletedAt      *time.Time
	EndedAt        *time.Time
}

type SearchResult struct {
	Capture    CaptureRecord
	Snippet    string
	MatchCount int
}

type SessionSummary struct {
	ID             string
	CreatedAt      time.Time
	LastCapturedAt time.Time
	CaptureCount   int
	DeletedAt      *time.Time
	EndedAt        *time.Time
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	dsn := dbPath + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) EnsureSession(id, parentID string) error {
	id = strings.TrimSpace(id)
	parentID = strings.TrimSpace(parentID)
	if id == "" {
		return errors.New("session id is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if parentID != "" && parentID != id {
		if _, err := tx.Exec(`INSERT INTO sessions (id) VALUES (?) ON CONFLICT(id) DO NOTHING`, parentID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO sessions (id, parent_id)
		VALUES (?, NULLIF(?, ''))
		ON CONFLICT(id) DO NOTHING
	`, id, parentID); err != nil {
		return err
	}

	if parentID != "" && parentID != id {
		if _, err := tx.Exec(`
			UPDATE sessions
			SET parent_id = COALESCE(parent_id, NULLIF(?, ''))
			WHERE id = ?
		`, parentID, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) ResolveRoot(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is required")
	}

	current := sessionID
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if seen[current] {
			return "", fmt.Errorf("cycle detected resolving session %s", sessionID)
		}
		seen[current] = true

		var parent sql.NullString
		err := s.db.QueryRow(`SELECT parent_id FROM sessions WHERE id = ?`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if !parent.Valid || strings.TrimSpace(parent.String) == "" {
			return current, nil
		}
		current = parent.String
	}

	return "", fmt.Errorf("session chain too deep for %s", sessionID)
}

func (s *Store) MarkSessionDeleted(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("session id is required")
	}
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) MarkSessionEnded(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("session id is required")
	}
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) AddCapture(input CaptureInput) (*CaptureRecord, error) {
	rootID, content, preview, bytes, capturedAt, err := s.normalizeCaptureInput(input)
	if err != nil {
		return nil, err
	}

	if err := s.EnsureSession(rootID, ""); err != nil {
		return nil, err
	}
	if input.ChildSessionID != "" {
		if err := s.EnsureSession(input.ChildSessionID, rootID); err != nil {
			return nil, err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if existing, err := getCaptureByCallID(tx, rootID, input.CallID); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}

	seq, err := nextSeq(tx, rootID)
	if err != nil {
		return nil, err
	}

	res, err := tx.Exec(`
		INSERT INTO captures (
			session_id, seq, child_session_id, call_id, agent, description, content, preview, bytes, captured_at
		) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)
	`, rootID, seq, strings.TrimSpace(input.ChildSessionID), strings.TrimSpace(input.CallID), normalizeAgent(input.Agent), strings.TrimSpace(input.Description), content, preview, bytes, formatDBTime(capturedAt))
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	record, err := getCaptureByID(tx, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *Store) DeleteCapture(sessionID string, seq int) error {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`DELETE FROM captures WHERE session_id = ? AND seq = ?`, rootID, seq)
	return err
}

func (s *Store) ListCaptures(sessionID, agent string) ([]CaptureRecord, error) {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND s.deleted_at IS NULL
	`
	args := []any{rootID}
	if strings.TrimSpace(agent) != "" {
		query += ` AND c.agent = ?`
		args = append(args, strings.TrimSpace(agent))
	}
	query += ` ORDER BY c.seq ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var captures []CaptureRecord
	for rows.Next() {
		record, err := scanCapture(rows, false)
		if err != nil {
			return nil, err
		}
		captures = append(captures, *record)
	}

	return captures, rows.Err()
}

func (s *Store) GetCaptureBySeq(sessionID string, seq int) (*CaptureRecord, error) {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return nil, err
	}

	row := s.db.QueryRow(`
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND c.seq = ? AND s.deleted_at IS NULL
	`, rootID, seq)

	record, err := scanCapture(row, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("output #%d not found", seq)
		}
		return nil, err
	}
	return record, nil
}

func (s *Store) ListRootSessions(limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT
			s.id,
			s.created_at,
			s.deleted_at,
			s.ended_at,
			COUNT(c.id) AS capture_count,
			MAX(c.captured_at) AS last_captured_at
		FROM sessions s
		LEFT JOIN captures c ON c.session_id = s.id
		WHERE s.parent_id IS NULL AND s.deleted_at IS NULL
		GROUP BY s.id, s.created_at, s.deleted_at, s.ended_at
		ORDER BY COALESCE(MAX(c.captured_at), s.created_at) DESC, s.created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionSummary
	for rows.Next() {
		var summary SessionSummary
		var createdAt string
		var deletedAt sql.NullString
		var endedAt sql.NullString
		var lastCapturedAt sql.NullString

		if err := rows.Scan(&summary.ID, &createdAt, &deletedAt, &endedAt, &summary.CaptureCount, &lastCapturedAt); err != nil {
			return nil, err
		}

		summary.CreatedAt = parseDBTime(createdAt)
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

// StoreStats holds aggregate statistics for all root sessions.
type StoreStats struct {
	Sessions   int
	Captures   int
	TotalBytes int64
}

// Stats returns aggregate statistics across all root sessions.
func (s *Store) Stats() (StoreStats, error) {
	var st StoreStats
	row := s.db.QueryRow(`
		SELECT
			COUNT(DISTINCT s.id),
			COUNT(c.id),
			COALESCE(SUM(c.bytes), 0)
		FROM sessions s
		LEFT JOIN captures c ON c.session_id = s.id
		WHERE s.parent_id IS NULL
		  AND s.deleted_at IS NULL
	`)
	err := row.Scan(&st.Sessions, &st.Captures, &st.TotalBytes)
	return st, err
}

func (s *Store) Search(sessionID, query string, contextLines int) ([]SearchResult, error) {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if contextLines <= 0 {
		contextLines = 3
	}

	// Try compiling as regex. If it fails, return explicit error.
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	rows, err := s.db.Query(`
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, ses.deleted_at, ses.ended_at
		FROM captures c
		JOIN sessions ses ON ses.id = c.session_id
		WHERE c.session_id = ? AND ses.deleted_at IS NULL
		ORDER BY c.seq ASC
	`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		record, err := scanCapture(rows, true)
		if err != nil {
			return nil, err
		}
		snippet, matches := buildSnippet(record.Content, re, contextLines)
		if matches == 0 {
			continue
		}
		results = append(results, SearchResult{Capture: *record, Snippet: snippet, MatchCount: matches})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type SearchMode string

const (
	SearchModeRegex SearchMode = "regex"
	SearchModeFTS5  SearchMode = "fts5"
)

func (s *Store) SearchWithMode(sessionID, query string, contextLines int, mode SearchMode) ([]SearchResult, error) {
	switch mode {
	case SearchModeRegex, "":
		return s.Search(sessionID, query, contextLines)
	case SearchModeFTS5:
		return s.searchFTS5(sessionID, query, contextLines)
	default:
		return nil, fmt.Errorf("unsupported search mode: %q", mode)
	}
}

func (s *Store) searchFTS5(sessionID, query string, contextLines int) ([]SearchResult, error) {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if contextLines <= 0 {
		contextLines = 3
	}

	// Sanitize user input for safe FTS5 MATCH expression
	matchQuery, err := search.BuildLiteralFTS5Match(query)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.bytes, c.source_path, c.captured_at, ses.deleted_at, ses.ended_at,
		       highlight(captures_fts, 1, '<<CBHL>>', '<</CBHL>>') AS content_hl
		FROM captures c
		JOIN sessions ses ON ses.id = c.session_id
		JOIN captures_fts ON captures_fts.rowid = c.id
		WHERE c.session_id = ? AND ses.deleted_at IS NULL AND captures_fts MATCH ?
		ORDER BY rank, c.seq ASC
	`, rootID, matchQuery)
	if err != nil {
		return nil, fmt.Errorf("fts5 query failed: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		record, contentHL, err := scanCaptureWithHighlight(rows)
		if err != nil {
			return nil, err
		}
		snippet, matches := buildFTS5Snippet(contentHL, contextLines)
		if matches == 0 {
			continue
		}
		results = append(results, SearchResult{Capture: *record, Snippet: snippet, MatchCount: matches})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) RenderHint(sessionID string, mode SearchMode) (string, error) {
	rootID, err := s.ResolveRoot(sessionID)
	if err != nil {
		return "", err
	}

	captures, err := s.ListCaptures(rootID, "")
	if err != nil {
		return "", err
	}
	if len(captures) == 0 {
		return "", nil
	}

	lines := []string{
		"## Prior Research Available — READ BEFORE WORKING",
		"",
		fmt.Sprintf("There are %d prior subagent outputs from this session:", len(captures)),
	}
	for _, capture := range captures {
		lines = append(lines, fmt.Sprintf("- [#%d] [%s] %s", capture.Seq, capture.Agent, capture.Description))
	}

	lines = append(lines,
		"",
		"**REQUIRED**: Before starting your task, check prior research that is relevant.",
		fmt.Sprintf("- Use `read` with `session_id=%q` and the output # to read full content", rootID),
		fmt.Sprintf("- Use `search` with `session_id=%q` and keywords to find specific info", rootID),
		fmt.Sprintf("- Use `list` with `session_id=%q` to see the full list with previews", rootID),
		"Do NOT redo research that already exists.",
		"",
		"### Tools",
		"",
		"OpenCode auto-prefixes ALL MCP tool names as `{server-name}_{tool-name}`. The context-bridge MCP server registers three tools:",
		"",
		"| Tool | Purpose | When to Use |",
		"| --- | --- | --- |",
		"| `context-bridge_search` | Search across all outputs by keyword. Returns matching snippets with context lines. | You need specific info but don't know which output has it. **START HERE for most tasks.** |",
		"| `context-bridge_read` | Read the full content of a specific output by its number. | You identified a relevant output (from the hint list or from search results) and need its full content. |",
		"| `context-bridge_list` | Summary table of all outputs with timestamps, sizes, and previews. | You need an overview of what exists. Rarely needed -- the hint already lists outputs. |",
		"",
		"### Decision Flow",
		"",
		"1. Your system prompt lists prior outputs with numbers, agent types, and descriptions",
		"2. If the task clearly relates to a listed output -- use `context-bridge_read` with that output number",
		"3. If you need to find specific information -- use `context-bridge_search` with a keyword",
		"4. Only read full outputs that are relevant to your task -- do not read everything",
		"",
		"### Relationship to Engram",
		"",
		"| Mechanism | Scope | Persistence | When to Use |",
		"| --- | --- | --- | --- |",
		"| Context Bridge tools | Current session only | Ephemeral (dies with session) | Same-session subagent outputs -- fresh research, recent exploration |",
		"| `mem_search` / `mem_get_observation` | Cross-session | Persistent (survives forever) | Historical context, previous session findings, architectural decisions |",
		"",
		"**Check BOTH** when recovering context -- context bridge for fresh same-session work, engram for historical knowledge.",
	)

	// Add mode-specific search guidance
	lines = appendModeSpecificGuidance(lines, mode)

	return strings.Join(lines, "\n"), nil
}

// appendModeSpecificGuidance adds search-engine-specific tips to the hint output.
// The guidance reflects the active search mode's query semantics.
func appendModeSpecificGuidance(lines []string, mode SearchMode) []string {
	switch mode {
	case SearchModeFTS5:
		return append(lines,
			"",
			"### FTS5 Search Tips",
			"",
			"The `search` tool uses SQLite FTS5 full-text search:",
			"- Enter keywords separated by spaces",
			"- Each keyword must appear in the content (implicit AND)",
			"- Punctuation and special characters are preserved as-is",
			"- Results ranked by BM25 relevance score",
			"",
			"**Tokenizer behavior**: Terms like `user@email.com` tokenize as separate words (`user`, `email`, `com`). Search each word separately or use a unique portion.",
		)
	default: // SearchModeRegex or empty
		return append(lines,
			"",
			"### Regex Search Tips",
			"",
			"The `search` tool uses case-insensitive Go regex:",
			"- Patterns: `auth.*`, `error.*Handler`, `(?i)jwt`",
			"- Invalid patterns return explicit errors (no fallback)",
			"- Use valid regex syntax or simple literal terms",
		)
	}
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return err
		}
		version = 0
	} else if err != nil {
		return err
	}

	if version >= currentSchemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if version < 1 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS sessions (
				id TEXT PRIMARY KEY,
				parent_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				deleted_at TEXT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS captures (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES sessions(id),
				seq INTEGER NOT NULL,
				child_session_id TEXT NULL,
				call_id TEXT NOT NULL,
				agent TEXT NOT NULL DEFAULT 'unknown',
				description TEXT NOT NULL DEFAULT '',
				content TEXT NOT NULL,
				preview TEXT NOT NULL DEFAULT '',
				bytes INTEGER NOT NULL DEFAULT 0,
				source_path TEXT NULL,
				captured_at TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(session_id, seq),
				UNIQUE(session_id, call_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_captures_session ON captures(session_id, seq)`,
			`CREATE INDEX IF NOT EXISTS idx_captures_agent ON captures(session_id, agent)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_id)`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS captures_fts USING fts5(description, content, content='captures', content_rowid='id')`,
			`CREATE TRIGGER IF NOT EXISTS captures_ai AFTER INSERT ON captures BEGIN INSERT INTO captures_fts(rowid, description, content) VALUES (new.id, new.description, new.content); END`,
			`CREATE TRIGGER IF NOT EXISTS captures_ad AFTER DELETE ON captures BEGIN INSERT INTO captures_fts(captures_fts, rowid, description, content) VALUES ('delete', old.id, old.description, old.content); END`,
			`CREATE TRIGGER IF NOT EXISTS captures_au AFTER UPDATE ON captures BEGIN INSERT INTO captures_fts(captures_fts, rowid, description, content) VALUES ('delete', old.id, old.description, old.content); INSERT INTO captures_fts(rowid, description, content) VALUES (new.id, new.description, new.content); END`,
		}

		for _, stmt := range statements {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(`UPDATE schema_version SET version = 1`); err != nil {
			return err
		}
		version = 1
	}

	if version < 2 {
		hasEndedAt, err := hasColumn(tx, "sessions", "ended_at")
		if err != nil {
			return err
		}
		if !hasEndedAt {
			if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN ended_at TEXT NULL`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = 2`); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) normalizeCaptureInput(input CaptureInput) (rootID, content, preview string, bytes int, capturedAt time.Time, err error) {
	parentID := strings.TrimSpace(input.ParentSessionID)
	if parentID == "" {
		return "", "", "", 0, time.Time{}, errors.New("parent session id is required")
	}
	if strings.TrimSpace(input.CallID) == "" {
		return "", "", "", 0, time.Time{}, errors.New("call id is required")
	}
	if strings.TrimSpace(input.Content) == "" {
		return "", "", "", 0, time.Time{}, errors.New("content is required")
	}

	rootID, err = s.ResolveRoot(parentID)
	if err != nil {
		return "", "", "", 0, time.Time{}, err
	}
	capturedAt = normalizeCapturedAt(input.CapturedAt)
	preview = extractPreview(input.Content)
	content = formatCaptureDocument(rootID, input, capturedAt)
	bytes = len([]byte(content))
	return rootID, content, preview, bytes, capturedAt, nil
}

func nextSeq(tx *sql.Tx, rootID string) (int, error) {
	var seq int
	err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM captures WHERE session_id = ?`, rootID).Scan(&seq)
	return seq, err
}

func getCaptureByCallID(tx *sql.Tx, rootID, callID string) (*CaptureRecord, error) {
	row := tx.QueryRow(`
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND c.call_id = ?
	`, rootID, callID)
	record, err := scanCapture(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func getCaptureByID(q interface{ QueryRow(string, ...any) *sql.Row }, id int64) (*CaptureRecord, error) {
	row := q.QueryRow(`
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.id = ?
	`, id)
	return scanCapture(row, true)
}

type scanner interface{ Scan(dest ...any) error }

func scanCapture(s scanner, withContent bool) (*CaptureRecord, error) {
	var record CaptureRecord
	var child sql.NullString
	var source sql.NullString
	var capturedAt string
	var deletedAt sql.NullString
	var endedAt sql.NullString

	if withContent {
		if err := s.Scan(&record.ID, &record.SessionID, &record.Seq, &child, &record.CallID, &record.Agent, &record.Description, &record.Preview, &record.Content, &record.Bytes, &source, &capturedAt, &deletedAt, &endedAt); err != nil {
			return nil, err
		}
	} else {
		if err := s.Scan(&record.ID, &record.SessionID, &record.Seq, &child, &record.CallID, &record.Agent, &record.Description, &record.Preview, &record.Bytes, &source, &capturedAt, &deletedAt, &endedAt); err != nil {
			return nil, err
		}
	}

	record.ChildSessionID = child.String
	record.SourcePath = source.String
	record.CapturedAt = parseDBTime(capturedAt)
	if deletedAt.Valid {
		t := parseDBTime(deletedAt.String)
		record.DeletedAt = &t
	}
	if endedAt.Valid {
		t := parseDBTime(endedAt.String)
		record.EndedAt = &t
	}
	return &record, nil
}

// scanCaptureWithHighlight scans a capture row including the FTS5-highlighted content column.
// It returns the capture record and the highlighted content string separately.
func scanCaptureWithHighlight(s scanner) (*CaptureRecord, string, error) {
	var record CaptureRecord
	var child sql.NullString
	var source sql.NullString
	var capturedAt string
	var deletedAt sql.NullString
	var endedAt sql.NullString
	var contentHL string

	if err := s.Scan(&record.ID, &record.SessionID, &record.Seq, &child, &record.CallID, &record.Agent, &record.Description, &record.Preview, &record.Bytes, &source, &capturedAt, &deletedAt, &endedAt, &contentHL); err != nil {
		return nil, "", err
	}

	record.ChildSessionID = child.String
	record.SourcePath = source.String
	record.CapturedAt = parseDBTime(capturedAt)
	if deletedAt.Valid {
		t := parseDBTime(deletedAt.String)
		record.DeletedAt = &t
	}
	if endedAt.Valid {
		t := parseDBTime(endedAt.String)
		record.EndedAt = &t
	}
	return &record, contentHL, nil
}

func hasColumn(tx *sql.Tx, tableName, columnName string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}

	return false, rows.Err()
}

const (
	fts5HighlightStart = "<<CBHL>>"
	fts5HighlightEnd   = "<</CBHL>>"
)

// buildFTS5Snippet builds a snippet from FTS5-highlighted content.
// It extracts lines containing highlight markers and builds context windows.
// The markers are stripped from the final output and matched lines are marked with ">>>".
func buildFTS5Snippet(contentHL string, contextLines int) (string, int) {
	lines := strings.Split(contentHL, "\n")
	var matches []int

	// Find lines containing highlight markers
	for idx, line := range lines {
		if strings.Contains(line, fts5HighlightStart) {
			matches = append(matches, idx)
		}
	}

	if len(matches) == 0 {
		return "", 0
	}

	type span struct{ start, end int }
	var spans []span
	for _, match := range matches {
		start := match - contextLines
		if start < 0 {
			start = 0
		}
		end := match + contextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		if len(spans) > 0 && start <= spans[len(spans)-1].end+1 {
			spans[len(spans)-1].end = end
			continue
		}
		spans = append(spans, span{start: start, end: end})
	}
	if len(spans) > 5 {
		spans = spans[:5]
	}

	var blocks []string
	for _, sp := range spans {
		var snippetLines []string
		for i := sp.start; i <= sp.end; i++ {
			prefix := "   "
			for _, match := range matches {
				if match == i {
					prefix = ">>>"
					break
				}
			}
			// Strip highlight markers from the line content
			cleanLine := strings.ReplaceAll(lines[i], fts5HighlightStart, "")
			cleanLine = strings.ReplaceAll(cleanLine, fts5HighlightEnd, "")
			snippetLines = append(snippetLines, fmt.Sprintf("%s %d: %s", prefix, i+1, cleanLine))
		}
		blocks = append(blocks, "```\n"+strings.Join(snippetLines, "\n")+"\n```")
	}

	return strings.Join(blocks, "\n\n"), len(matches)
}

func normalizeAgent(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return "unknown"
	}
	return agent
}

func normalizeCapturedAt(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func formatCaptureDocument(rootID string, input CaptureInput, capturedAt time.Time) string {
	return strings.Join([]string{
		fmt.Sprintf("# Context Bridge: %s subagent output", normalizeAgent(input.Agent)),
		"",
		fmt.Sprintf("> **Agent**: %s", normalizeAgent(input.Agent)),
		fmt.Sprintf("> **Task**: %s", strings.TrimSpace(input.Description)),
		fmt.Sprintf("> **Parent Session**: %s", rootID),
		fmt.Sprintf("> **Child Session**: %s", strings.TrimSpace(input.ChildSessionID)),
		fmt.Sprintf("> **Call ID**: %s", strings.TrimSpace(input.CallID)),
		fmt.Sprintf("> **Time**: %s", capturedAt.Format(time.RFC3339Nano)),
		"",
		"---",
		"",
		strings.TrimSpace(input.Content),
	}, "\n")
}

func extractPreview(text string) string {
	lines := strings.Split(text, "\n")
	preview := make([]string, 0, 5)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(preview) == 0 && strings.HasPrefix(line, "task_id:") {
			continue
		}
		if len(line) > 120 {
			line = line[:120]
		}
		preview = append(preview, line)
		if len(preview) == 5 {
			break
		}
	}
	if len(preview) == 0 {
		return "[empty output]"
	}
	return strings.Join(preview, "\n")
}

func formatDBTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func buildSnippet(content string, re *regexp.Regexp, contextLines int) (string, int) {
	lines := strings.Split(content, "\n")
	var matches []int

	for idx, line := range lines {
		if re.MatchString(line) {
			matches = append(matches, idx)
		}
	}

	if len(matches) == 0 {
		return "", 0
	}

	type span struct{ start, end int }
	var spans []span
	for _, match := range matches {
		start := match - contextLines
		if start < 0 {
			start = 0
		}
		end := match + contextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		if len(spans) > 0 && start <= spans[len(spans)-1].end+1 {
			spans[len(spans)-1].end = end
			continue
		}
		spans = append(spans, span{start: start, end: end})
	}
	if len(spans) > 5 {
		spans = spans[:5]
	}

	var blocks []string
	for _, sp := range spans {
		var snippetLines []string
		for i := sp.start; i <= sp.end; i++ {
			prefix := "   "
			for _, match := range matches {
				if match == i {
					prefix = ">>>"
					break
				}
			}
			snippetLines = append(snippetLines, fmt.Sprintf("%s %d: %s", prefix, i+1, lines[i]))
		}
		blocks = append(blocks, "```\n"+strings.Join(snippetLines, "\n")+"\n```")
	}

	return strings.Join(blocks, "\n\n"), len(matches)
}

func FormatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func FormatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	delta := time.Since(t)
	if delta < time.Minute {
		return "just now"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
}

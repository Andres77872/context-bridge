package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"context-bridge/internal/search"

	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 4

const (
	// MaxCaptureContentBytes bounds persisted tool output independently of the
	// adapter. The HTTP body has a small additional allowance for metadata.
	MaxCaptureContentBytes = 256 << 10
	maxDescriptionBytes    = 4 << 10
	maxPreviewBytes        = 5*120 + 4
	maxIdentifierBytes     = 256
	maxAgentBytes          = 128
	maxCapturesPerSession  = 1000
	maxCapturesGlobal      = 10000
	maxCaptureBytesGlobal  = 256 << 20
	maxSearchQueryBytes    = 1024
	maxSnippetLineBytes    = 2 << 10
	maxSnippetBytes        = 16 << 10
	maxHintCaptures        = 20
	captureRetention       = 30 * 24 * time.Hour
	secretKeyPrefixPattern = `(["']?\b(?:api[_-]?key|token|secret|password|authorization)\b["']?[ \t]*[:=][ \t]*)`
)

var (
	privateBlockPattern         = regexp.MustCompile(`(?is)<[[:space:]]*private(?:[[:space:]][^>]*)?>.*?<[[:space:]]*/[[:space:]]*private[[:space:]]*>`)
	trustBoundaryPattern        = regexp.MustCompile(`(?i)<[[:space:]]*/?[[:space:]]*untrusted-context-bridge-(?:data|hint)(?:[[:space:]/][^>]*)?>`)
	envSecretPattern            = regexp.MustCompile(`(?im)^([ \t]*[A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)[A-Z0-9_]*[ \t]*=[ \t]*)(.*)$`)
	bearerPattern               = regexp.MustCompile(`(?i)\b(Bearer[ \t]+)[A-Za-z0-9._~+/=-]{12,}`)
	doubleQuotedKeyValuePattern = regexp.MustCompile(`(?i)` + secretKeyPrefixPattern + `("(?:\\.|[^"\\\r\n]){8,}")`)
	singleQuotedKeyValuePattern = regexp.MustCompile(`(?i)` + secretKeyPrefixPattern + `('(?:\\.|[^'\\\r\n]){8,}')`)
	bareKeyValuePattern         = regexp.MustCompile(`(?i)` + secretKeyPrefixPattern + `(\[[^\]\r\n]*\]+|[^\s"',;}]{8,})`)
	identifierPattern           = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	safeHintAgentPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Store struct {
	db   *sql.DB
	path string
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
	ID              string
	CreatedAt       time.Time
	FirstCapturedAt time.Time
	LastCapturedAt  time.Time
	CaptureCount    int
	Bytes           int64
	AgentCount      int
	DeletedAt       *time.Time
	EndedAt         *time.Time
}

func Open(dbPath string) (*Store, error) {
	return open(dbPath, false)
}

// OpenManaged opens a database whose parent directory is owned by Context
// Bridge. Unlike Open, it may create and harden that dedicated directory.
func OpenManaged(dbPath string) (*Store, error) {
	return open(dbPath, true)
}

func open(dbPath string, managedDirectory bool) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, errors.New("database path is required")
	}
	dbPath = filepath.Clean(dbPath)
	if !filepath.IsAbs(dbPath) {
		return nil, fmt.Errorf("database path must be absolute: %s", dbPath)
	}
	if strings.ContainsRune(dbPath, '?') || strings.ContainsRune(dbPath, '\x00') {
		return nil, errors.New("database path must not contain '?' or NUL because it is used in a SQLite DSN")
	}
	if err := prepareDBPath(dbPath, managedDirectory); err != nil {
		return nil, err
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

	s := &Store{db: db, path: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.pruneRetention(time.Now().UTC()); err != nil {
		db.Close()
		return nil, fmt.Errorf("prune expired captures: %w", err)
	}
	if err := secureSQLiteFiles(dbPath, managedDirectory); err != nil {
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
	if err := validateIdentifier("session id", id, false); err != nil {
		return err
	}
	if err := validateIdentifier("parent session id", parentID, true); err != nil {
		return err
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
	if err := validateParentAssignment(tx, id, parentID); err != nil {
		return err
	}

	var currentParent sql.NullString
	err = tx.QueryRow(`SELECT parent_id FROM sessions WHERE id = ?`, id).Scan(&currentParent)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`INSERT INTO sessions (id, parent_id) VALUES (?, NULLIF(?, ''))`, id, parentID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	if parentID == "" {
		return tx.Commit()
	}
	if currentParent.Valid && strings.TrimSpace(currentParent.String) != "" {
		currentRoot, err := resolveRootTx(tx, currentParent.String)
		if err != nil {
			return err
		}
		requestedRoot, err := resolveRootTx(tx, parentID)
		if err != nil {
			return err
		}
		if currentRoot != requestedRoot {
			return fmt.Errorf("session %s is already attached to root %s; refusing conflicting root %s", id, currentRoot, requestedRoot)
		}
		if strings.TrimSpace(currentParent.String) != parentID {
			if _, err := tx.Exec(`UPDATE sessions SET parent_id = ? WHERE id = ?`, parentID, id); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	targetRoot, err := resolveRootTx(tx, parentID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET parent_id = ? WHERE id = ?`, parentID, id); err != nil {
		return err
	}
	if err := migrateRootCaptures(tx, id, targetRoot); err != nil {
		return err
	}

	return tx.Commit()
}

func resolveRootTx(tx *sql.Tx, sessionID string) (string, error) {
	current := strings.TrimSpace(sessionID)
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if seen[current] {
			return "", fmt.Errorf("cycle detected resolving session %s", sessionID)
		}
		seen[current] = true
		var parent sql.NullString
		err := tx.QueryRow(`SELECT parent_id FROM sessions WHERE id = ?`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if !parent.Valid || strings.TrimSpace(parent.String) == "" {
			return current, nil
		}
		current = strings.TrimSpace(parent.String)
	}
	return "", fmt.Errorf("session chain too deep for %s", sessionID)
}

func migrateRootCaptures(tx *sql.Tx, fromRoot, toRoot string) error {
	if fromRoot == toRoot {
		return nil
	}
	type pendingCapture struct {
		id      int64
		callID  string
		content string
	}
	rows, err := tx.Query(`SELECT id, call_id, content FROM captures WHERE session_id = ? ORDER BY seq ASC`, fromRoot)
	if err != nil {
		return err
	}
	var pending []pendingCapture
	for rows.Next() {
		var capture pendingCapture
		if err := rows.Scan(&capture.id, &capture.callID, &capture.content); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, capture)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, capture := range pending {
		var duplicateID int64
		err := tx.QueryRow(`SELECT id FROM captures WHERE session_id = ? AND call_id = ?`, toRoot, capture.callID).Scan(&duplicateID)
		if err == nil {
			if _, err := tx.Exec(`DELETE FROM captures WHERE id = ?`, capture.id); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		seq, err := nextSeq(tx, toRoot)
		if err != nil {
			return err
		}
		oldHeader := fmt.Sprintf("> **Parent Session**: %s", fromRoot)
		newHeader := fmt.Sprintf("> **Parent Session**: %s", toRoot)
		content := strings.Replace(capture.content, oldHeader, newHeader, 1)
		content = truncateUTF8Bytes(content, MaxCaptureContentBytes)
		if _, err := tx.Exec(`UPDATE captures SET session_id = ?, seq = ?, content = ?, bytes = ? WHERE id = ?`, toRoot, seq, content, len([]byte(content)), capture.id); err != nil {
			return err
		}
	}

	return nil
}

func validateParentAssignment(tx *sql.Tx, sessionID, parentID string) error {
	if parentID == "" {
		return nil
	}
	if parentID == sessionID {
		return fmt.Errorf("session %s cannot be its own parent", sessionID)
	}

	current := parentID
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if current == sessionID {
			return fmt.Errorf("parent assignment would create a cycle for session %s", sessionID)
		}
		if seen[current] {
			return fmt.Errorf("existing cycle detected while linking session %s", sessionID)
		}
		seen[current] = true

		var parent sql.NullString
		err := tx.QueryRow(`SELECT parent_id FROM sessions WHERE id = ?`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !parent.Valid || strings.TrimSpace(parent.String) == "" {
			return nil
		}
		current = strings.TrimSpace(parent.String)
	}
	return fmt.Errorf("session chain too deep while linking %s", sessionID)
}

func (s *Store) ResolveRoot(sessionID string) (string, error) {
	return s.ResolveRootContext(context.Background(), sessionID)
}

// ResolveRootContext resolves a child session to its root while honoring
// caller cancellation. A bounded traversal prevents malformed session graphs
// from consuming unbounded work.
func (s *Store) ResolveRootContext(ctx context.Context, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := validateIdentifier("session id", sessionID, false); err != nil {
		return "", err
	}

	current := sessionID
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if seen[current] {
			return "", fmt.Errorf("cycle detected resolving session %s", sessionID)
		}
		seen[current] = true

		var parent sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT parent_id FROM sessions WHERE id = ?`, current).Scan(&parent)
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
	if err := validateIdentifier("session id", id, false); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET deleted_at = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) MarkSessionEnded(id string) error {
	id = strings.TrimSpace(id)
	if err := validateIdentifier("session id", id, false); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = datetime('now') WHERE id = ?`, id)
	return err
}

func (s *Store) AddCapture(input CaptureInput) (*CaptureRecord, error) {
	var err error
	input, err = sanitizeCaptureInput(input)
	if err != nil {
		return nil, err
	}
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
	if err := pruneRetentionTx(tx, rootID, time.Now().UTC()); err != nil {
		return nil, err
	}
	var deletedAt sql.NullString
	if err := tx.QueryRow(`SELECT deleted_at FROM sessions WHERE id = ?`, rootID).Scan(&deletedAt); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		return nil, fmt.Errorf("root session %s was deleted and cannot accept captures", rootID)
	}

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
	if err := enforceCaptureLimits(tx, rootID); err != nil {
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
	return s.ListCapturesContext(context.Background(), sessionID, agent, 0)
}

// ListCapturesContext lists captures in sequence order. A positive limit keeps
// only the newest records and then restores ascending sequence order.
func (s *Store) ListCapturesContext(ctx context.Context, sessionID, agent string, limit int) ([]CaptureRecord, error) {
	if limit < 0 {
		return nil, errors.New("capture limit must not be negative")
	}
	rootID, err := s.ResolveRootContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	agent = strings.TrimSpace(agent)
	if agent != "" {
		if len([]byte(agent)) > maxAgentBytes || !identifierPattern.MatchString(agent) {
			return nil, errors.New("agent filter contains unsupported characters or is too long")
		}
	}

	query := `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND s.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
	`
	args := []any{rootID}
	if agent != "" {
		query += ` AND c.agent = ?`
		args = append(args, agent)
	}
	if limit > 0 {
		query += ` ORDER BY c.seq DESC LIMIT ?`
		args = append(args, limit)
	} else {
		query += ` ORDER BY c.seq ASC`
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var captures []CaptureRecord
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := scanCapture(rows, false)
		if err != nil {
			return nil, err
		}
		captures = append(captures, *record)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 {
		reverseCaptures(captures)
	}
	return captures, nil
}

// CaptureListOptions describes a filtered, paged capture listing.
type CaptureListOptions struct {
	SessionID string
	Agent     string
	Query     string
	Newest    bool
	Limit     int
	Offset    int
}

// CapturePage is one page of captures plus the totals needed to render
// "showing X of Y" without a second round trip.
type CapturePage struct {
	Captures []CaptureRecord
	Total    int
	Filtered int
	Agents   []string
}

// ListCapturesPageContext lists captures for a session with server-side agent
// and text filtering. Filtering in SQL keeps large sessions cheap: the caller
// never materialises rows it will not show.
func (s *Store) ListCapturesPageContext(ctx context.Context, opts CaptureListOptions) (*CapturePage, error) {
	if opts.Limit < 0 || opts.Offset < 0 {
		return nil, errors.New("capture limit and offset must not be negative")
	}
	rootID, err := s.ResolveRootContext(ctx, opts.SessionID)
	if err != nil {
		return nil, err
	}

	agent := strings.TrimSpace(opts.Agent)
	if agent != "" {
		if len([]byte(agent)) > maxAgentBytes || !identifierPattern.MatchString(agent) {
			return nil, errors.New("agent filter contains unsupported characters or is too long")
		}
	}
	textQuery := strings.TrimSpace(opts.Query)
	if len([]byte(textQuery)) > maxSearchQueryBytes {
		return nil, fmt.Errorf("capture filter exceeds %d bytes", maxSearchQueryBytes)
	}

	const scope = `
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND s.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
	`
	where := ""
	args := []any{rootID}
	if agent != "" {
		where += ` AND c.agent = ?`
		args = append(args, agent)
	}
	if textQuery != "" {
		pattern := "%" + escapeLikePattern(textQuery) + "%"
		where += ` AND (c.description LIKE ? ESCAPE '\' OR c.agent LIKE ? ESCAPE '\' OR c.preview LIKE ? ESCAPE '\' OR CAST(c.seq AS TEXT) LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern, pattern)
	}

	page := &CapturePage{Captures: []CaptureRecord{}, Agents: []string{}}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(c.id)`+scope, rootID).Scan(&page.Total); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(c.id)`+scope+where, args...).Scan(&page.Filtered); err != nil {
		return nil, err
	}

	agentRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT c.agent`+scope+` ORDER BY c.agent ASC`, rootID)
	if err != nil {
		return nil, err
	}
	defer agentRows.Close()
	for agentRows.Next() {
		var name string
		if err := agentRows.Scan(&name); err != nil {
			return nil, err
		}
		page.Agents = append(page.Agents, name)
	}
	if err := agentRows.Err(); err != nil {
		return nil, err
	}

	order := ` ORDER BY c.seq ASC`
	if opts.Newest {
		order = ` ORDER BY c.seq DESC`
	}
	listQuery := `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
	` + scope + where + order
	listArgs := append([]any{}, args...)
	if opts.Limit > 0 {
		listQuery += ` LIMIT ? OFFSET ?`
		listArgs = append(listArgs, opts.Limit, opts.Offset)
	} else if opts.Offset > 0 {
		listQuery += ` LIMIT -1 OFFSET ?`
		listArgs = append(listArgs, opts.Offset)
	}

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := scanCapture(rows, false)
		if err != nil {
			return nil, err
		}
		page.Captures = append(page.Captures, *record)
	}
	return page, rows.Err()
}

func (s *Store) GetCaptureBySeq(sessionID string, seq int) (*CaptureRecord, error) {
	return s.GetCaptureBySeqContext(context.Background(), sessionID, seq)
}

func (s *Store) GetCaptureBySeqContext(ctx context.Context, sessionID string, seq int) (*CaptureRecord, error) {
	if seq <= 0 {
		return nil, errors.New("output number must be positive")
	}
	rootID, err := s.ResolveRootContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, s.deleted_at, s.ended_at
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND c.seq = ? AND s.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
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

// CountCapturesContext returns the number of visible captures for one root
// session, optionally filtered by agent, without materializing every row.
func (s *Store) CountCapturesContext(ctx context.Context, sessionID, agent string) (int, error) {
	rootID, err := s.ResolveRootContext(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	agent = strings.TrimSpace(agent)
	query := `
		SELECT COUNT(c.id)
		FROM captures c
		JOIN sessions s ON s.id = c.session_id
		WHERE c.session_id = ? AND s.deleted_at IS NULL
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
	`
	args := []any{rootID}
	if agent != "" {
		if len([]byte(agent)) > maxAgentBytes || !identifierPattern.MatchString(agent) {
			return 0, errors.New("agent filter contains unsupported characters or is too long")
		}
		query += ` AND c.agent = ?`
		args = append(args, agent)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) RootSessionExistsContext(ctx context.Context, sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := validateIdentifier("session id", sessionID, false); err != nil {
		return false, err
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sessions
			WHERE id = ? AND parent_id IS NULL AND deleted_at IS NULL
		)
	`, sessionID).Scan(&exists)
	return exists, err
}

// ListRootSessions lists the most recently active root sessions. It is the
// unfiltered shorthand for ListRootSessionsContext.
func (s *Store) ListRootSessions(limit int) ([]SessionSummary, error) {
	return s.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: limit})
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
		  AND julianday(c.created_at) >= julianday('now', '-30 days')
		WHERE s.parent_id IS NULL
		  AND s.deleted_at IS NULL
	`)
	err := row.Scan(&st.Sessions, &st.Captures, &st.TotalBytes)
	return st, err
}

func (s *Store) Search(sessionID, query string, contextLines int) ([]SearchResult, error) {
	return s.SearchWithModeContext(context.Background(), sessionID, query, contextLines, SearchModeRegex, 0, 0, 0)
}

type SearchMode string

const (
	SearchModeRegex SearchMode = "regex"
	SearchModeFTS5  SearchMode = "fts5"
)

func (s *Store) SearchWithMode(sessionID, query string, contextLines int, mode SearchMode) ([]SearchResult, error) {
	return s.SearchWithModeContext(context.Background(), sessionID, query, contextLines, mode, 0, 0, 0)
}

// SearchOptions describes one search request. An empty SessionID searches
// every live root session; a non-empty one is resolved to its root first.
type SearchOptions struct {
	SessionID     string
	Agent         string
	Mode          SearchMode
	ContextLines  int
	MaxResults    int
	MaxMatches    int
	MaxCandidates int
}

// SearchWithModeContext runs a cancellable search scoped to one session.
// Positive limits bound the number of returned captures, matched lines, and
// regex candidate captures. A zero limit preserves the unbounded internal API
// used by the local TUI.
func (s *Store) SearchWithModeContext(ctx context.Context, sessionID, query string, contextLines int, mode SearchMode, maxResults, maxMatches, maxCandidates int) ([]SearchResult, error) {
	return s.SearchContext(ctx, query, SearchOptions{
		SessionID:     sessionID,
		Mode:          mode,
		ContextLines:  contextLines,
		MaxResults:    maxResults,
		MaxMatches:    maxMatches,
		MaxCandidates: maxCandidates,
	})
}

// SearchContext runs a cancellable search with an explicit scope. It is the
// single entry point behind every session-scoped and cross-session search.
func (s *Store) SearchContext(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if opts.MaxResults < 0 || opts.MaxMatches < 0 || opts.MaxCandidates < 0 {
		return nil, errors.New("search limits must not be negative")
	}
	if opts.ContextLines < 0 {
		opts.ContextLines = 3
	}
	if opts.ContextLines > 20 {
		opts.ContextLines = 20
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if len([]byte(query)) > maxSearchQueryBytes {
		return nil, fmt.Errorf("query exceeds %d bytes", maxSearchQueryBytes)
	}

	scope, err := s.resolveSearchScope(ctx, opts)
	if err != nil {
		return nil, err
	}

	switch opts.Mode {
	case SearchModeRegex, "":
		return s.searchRegexContext(ctx, scope, query, opts)
	case SearchModeFTS5:
		return s.searchFTS5Context(ctx, scope, query, opts)
	default:
		return nil, fmt.Errorf("unsupported search mode: %q", opts.Mode)
	}
}

// searchScope holds the resolved SQL predicates shared by both search engines.
type searchScope struct {
	rootID string
	agent  string
}

func (sc searchScope) global() bool { return sc.rootID == "" }

// predicate returns the WHERE fragment and arguments for this scope. The
// caller supplies the alias used for the captures and sessions tables.
func (sc searchScope) predicate() (string, []any) {
	var clause strings.Builder
	args := []any{}
	if sc.global() {
		clause.WriteString(` ses.parent_id IS NULL AND ses.deleted_at IS NULL`)
	} else {
		clause.WriteString(` c.session_id = ? AND ses.deleted_at IS NULL`)
		args = append(args, sc.rootID)
	}
	clause.WriteString(` AND julianday(c.created_at) >= julianday('now', '-30 days')`)
	if sc.agent != "" {
		clause.WriteString(` AND c.agent = ?`)
		args = append(args, sc.agent)
	}
	return clause.String(), args
}

func (s *Store) resolveSearchScope(ctx context.Context, opts SearchOptions) (searchScope, error) {
	scope := searchScope{}
	if agent := strings.TrimSpace(opts.Agent); agent != "" {
		if len([]byte(agent)) > maxAgentBytes || !identifierPattern.MatchString(agent) {
			return scope, errors.New("agent filter contains unsupported characters or is too long")
		}
		scope.agent = agent
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		return scope, nil
	}
	rootID, err := s.ResolveRootContext(ctx, opts.SessionID)
	if err != nil {
		return scope, err
	}
	scope.rootID = rootID
	return scope, nil
}

func (s *Store) searchRegexContext(ctx context.Context, scope searchScope, query string, opts SearchOptions) ([]SearchResult, error) {
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	where, args := scope.predicate()
	querySQL := `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.content, c.bytes, c.source_path, c.captured_at, ses.deleted_at, ses.ended_at
		FROM captures c
		JOIN sessions ses ON ses.id = c.session_id
		WHERE` + where

	// Bounded scans read the newest captures first so a truncated sweep still
	// surfaces current work; unbounded scans keep chronological order.
	switch {
	case opts.MaxCandidates > 0 && scope.global():
		querySQL += ` ORDER BY c.captured_at DESC, c.id DESC LIMIT ?`
		args = append(args, opts.MaxCandidates)
	case opts.MaxCandidates > 0:
		querySQL += ` ORDER BY c.seq DESC LIMIT ?`
		args = append(args, opts.MaxCandidates)
	case scope.global():
		querySQL += ` ORDER BY c.captured_at ASC, c.id ASC`
	default:
		querySQL += ` ORDER BY c.seq ASC`
	}

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	totalMatches := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := scanCapture(rows, true)
		if err != nil {
			return nil, err
		}
		remainingMatches := 0
		if opts.MaxMatches > 0 {
			remainingMatches = opts.MaxMatches - totalMatches
			if remainingMatches <= 0 {
				break
			}
		}
		snippet, matches := buildSnippetLimited(record.Content, re, opts.ContextLines, remainingMatches)
		if matches == 0 {
			continue
		}
		results = append(results, SearchResult{Capture: *record, Snippet: snippet, MatchCount: matches})
		totalMatches += matches
		if opts.MaxResults > 0 && len(results) >= opts.MaxResults {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) searchFTS5Context(ctx context.Context, scope searchScope, query string, opts SearchOptions) ([]SearchResult, error) {
	matchQuery, err := search.BuildLiteralFTS5Match(query)
	if err != nil {
		return nil, err
	}
	highlightStart, highlightEnd, err := newFTS5HighlightMarkers()
	if err != nil {
		return nil, err
	}

	where, scopeArgs := scope.predicate()
	orderBy := ` ORDER BY rank, c.seq ASC`
	if scope.global() {
		orderBy = ` ORDER BY rank, c.captured_at DESC, c.id DESC`
	}

	querySQL := `
		SELECT c.id, c.session_id, c.seq, c.child_session_id, c.call_id, c.agent, c.description, c.preview, c.bytes, c.source_path, c.captured_at, ses.deleted_at, ses.ended_at,
		       highlight(captures_fts, 1, ?, ?) AS content_hl
		FROM captures c
		JOIN sessions ses ON ses.id = c.session_id
		JOIN captures_fts ON captures_fts.rowid = c.id
		WHERE captures_fts MATCH ? AND` + where + orderBy

	args := append([]any{highlightStart, highlightEnd, matchQuery}, scopeArgs...)
	if opts.MaxResults > 0 {
		querySQL += ` LIMIT ?`
		args = append(args, opts.MaxResults)
	}
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("fts5 query failed: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	totalMatches := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, contentHL, err := scanCaptureWithHighlight(rows)
		if err != nil {
			return nil, err
		}
		remainingMatches := 0
		if opts.MaxMatches > 0 {
			remainingMatches = opts.MaxMatches - totalMatches
			if remainingMatches <= 0 {
				break
			}
		}
		snippet, matches := buildFTS5SnippetLimited(contentHL, opts.ContextLines, remainingMatches, highlightStart, highlightEnd)
		if matches == 0 {
			continue
		}
		results = append(results, SearchResult{Capture: *record, Snippet: snippet, MatchCount: matches})
		totalMatches += matches
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

	omitted := 0
	if len(captures) > maxHintCaptures {
		omitted = len(captures) - maxHintCaptures
		captures = captures[omitted:]
	}

	lines := []string{
		"## Prior Context Bridge Outputs",
		"",
		fmt.Sprintf("Context Bridge has %d persisted subagent outputs in this session tree.", len(captures)+omitted),
		"Only opaque output numbers and validated agent identifiers are shown here.",
	}
	if omitted > 0 {
		lines = append(lines, fmt.Sprintf("Showing the latest %d outputs; %d earlier outputs are omitted from this hint.", len(captures), omitted))
	}
	for _, capture := range captures {
		lines = append(lines, fmt.Sprintf(
			"- output #%d; agent=%s",
			capture.Seq,
			safeHintAgent(capture.Agent),
		))
	}

	lines = append(lines,
		"",
		"If a prior output is relevant and these tools are available:",
		"- `context-bridge_read`: read one numbered output in the current session tree",
		"- `context-bridge_search`: search persisted outputs in the current session tree",
		"- `context-bridge_list`: list persisted outputs in the current session tree",
		"The OpenCode adapter enforces the current session ID before each Context Bridge MCP call.",
	)

	// Add mode-specific search guidance
	lines = appendModeSpecificGuidance(lines, mode)

	return strings.Join(lines, "\n"), nil
}

func safeHintAgent(value string) string {
	if safeHintAgentPattern.MatchString(value) {
		return value
	}
	return "unknown"
}

func prepareDBPath(dbPath string, managedDirectory bool) error {
	dir := filepath.Dir(dbPath)
	if managedDirectory {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create db directory: %w", err)
		}
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		if !managedDirectory && errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("explicit database directory %s does not exist", dir)
		}
		return fmt.Errorf("inspect db directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return fmt.Errorf("database directory %s must be a real directory, not a symlink", dir)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve database directory: %w", err)
	}
	if filepath.Clean(resolvedDir) != filepath.Clean(dir) {
		return fmt.Errorf("database directory %s must not contain symlink components", dir)
	}
	if managedDirectory {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure db directory: %w", err)
		}
	} else if dirInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("explicit database directory %s must not be writable by group or other users", dir)
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := rejectSQLiteSymlink(path); err != nil {
			return err
		}
	}

	file, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create db file: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("inspect db file: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("database path %s must be a regular file", dbPath)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure db file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close db file: %w", err)
	}
	return nil
}

func rejectSQLiteSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sqlite file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("sqlite file %s must be a regular file, not a symlink", path)
	}
	return nil
}

func secureSQLiteFiles(dbPath string, managedDirectory bool) error {
	if managedDirectory {
		if err := os.Chmod(filepath.Dir(dbPath), 0o700); err != nil {
			return fmt.Errorf("secure db directory: %w", err)
		}
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := rejectSQLiteSymlink(path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure sqlite file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) pruneRetention(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := pruneRetentionTx(tx, "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneRetentionTx(tx *sql.Tx, rootID string, now time.Time) error {
	cutoff := formatDBTime(now.Add(-captureRetention))
	if _, err := tx.Exec(`DELETE FROM captures WHERE julianday(created_at) < julianday(?)`, cutoff); err != nil {
		return err
	}
	if rootID != "" {
		return enforceCaptureLimits(tx, rootID)
	}
	if _, err := tx.Exec(`
		DELETE FROM captures
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq DESC) AS row_num
				FROM captures
			) ranked
			WHERE row_num > ?
		)
	`, maxCapturesPerSession); err != nil {
		return err
	}
	return enforceGlobalCaptureLimits(tx)
}

func enforceCaptureLimits(tx *sql.Tx, rootID string) error {
	if _, err := tx.Exec(`
		DELETE FROM captures
		WHERE session_id = ? AND id NOT IN (
			SELECT id FROM captures WHERE session_id = ? ORDER BY seq DESC LIMIT ?
		)
	`, rootID, rootID, maxCapturesPerSession); err != nil {
		return err
	}
	return enforceGlobalCaptureLimits(tx)
}

func enforceGlobalCaptureLimits(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		DELETE FROM captures
		WHERE id IN (
			SELECT id FROM captures
			ORDER BY julianday(created_at) DESC, id DESC
			LIMIT -1 OFFSET ?
		)
	`, maxCapturesGlobal); err != nil {
		return err
	}
	_, err := tx.Exec(`
		DELETE FROM captures
		WHERE id IN (
			SELECT id FROM (
				SELECT
					id,
					SUM(bytes) OVER (
						ORDER BY julianday(created_at) DESC, id DESC
						ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
					) AS cumulative_bytes
				FROM captures
			) ranked
			WHERE cumulative_bytes > ?
		)
	`, maxCaptureBytesGlobal)
	return err
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

	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	if version == currentSchemaVersion {
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
				next_seq INTEGER NOT NULL DEFAULT 1,
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
		version = 2
	}

	if version < 3 {
		if err := redactLegacyCaptures(tx); err != nil {
			return fmt.Errorf("redact legacy captures: %w", err)
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = 3`); err != nil {
			return err
		}
		version = 3
	}

	if version < 4 {
		hasNextSeq, err := hasColumn(tx, "sessions", "next_seq")
		if err != nil {
			return err
		}
		if !hasNextSeq {
			if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN next_seq INTEGER NOT NULL DEFAULT 1`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`
			UPDATE sessions
			SET next_seq = MAX(
				1,
				COALESCE((SELECT MAX(c.seq) + 1 FROM captures c WHERE c.session_id = sessions.id), 1)
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = 4`); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func redactLegacyCaptures(tx *sql.Tx) error {
	type legacyCapture struct {
		id          int64
		description string
		preview     string
		content     string
	}
	var lastID int64
	for {
		rows, err := tx.Query(`
			SELECT id, description, preview, content
			FROM captures
			WHERE id > ?
			ORDER BY id ASC
			LIMIT 100
		`, lastID)
		if err != nil {
			return err
		}

		batch := make([]legacyCapture, 0, 100)
		for rows.Next() {
			var capture legacyCapture
			if err := rows.Scan(&capture.id, &capture.description, &capture.preview, &capture.content); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, capture)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		for _, capture := range batch {
			description := truncateUTF8Bytes(RedactSensitiveContent(capture.description), maxDescriptionBytes)
			preview := truncateUTF8Bytes(RedactSensitiveContent(capture.preview), maxPreviewBytes)
			content := RedactSensitiveContent(capture.content)
			content = truncateUTF8Bytes(content, MaxCaptureContentBytes)
			if _, err := tx.Exec(`
				UPDATE captures
				SET description = ?, preview = ?, content = ?, bytes = ?
				WHERE id = ?
			`, description, preview, content, len([]byte(content)), capture.id); err != nil {
				return err
			}
			lastID = capture.id
		}
	}
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
	if len([]byte(content)) > MaxCaptureContentBytes {
		return "", "", "", 0, time.Time{}, fmt.Errorf("formatted content exceeds %d bytes", MaxCaptureContentBytes)
	}
	bytes = len([]byte(content))
	return rootID, content, preview, bytes, capturedAt, nil
}

func sanitizeCaptureInput(input CaptureInput) (CaptureInput, error) {
	input.ParentSessionID = strings.TrimSpace(input.ParentSessionID)
	input.ChildSessionID = strings.TrimSpace(input.ChildSessionID)
	input.CallID = strings.TrimSpace(input.CallID)
	input.Agent = normalizeAgent(input.Agent)
	input.Description = strings.TrimSpace(input.Description)
	input.Content = strings.TrimSpace(input.Content)

	for name, value := range map[string]string{
		"parent session id": input.ParentSessionID,
		"child session id":  input.ChildSessionID,
		"call id":           input.CallID,
	} {
		if len([]byte(value)) > maxIdentifierBytes {
			return CaptureInput{}, fmt.Errorf("%s exceeds %d bytes", name, maxIdentifierBytes)
		}
	}
	if err := validateIdentifier("parent session id", input.ParentSessionID, false); err != nil {
		return CaptureInput{}, err
	}
	if err := validateIdentifier("child session id", input.ChildSessionID, true); err != nil {
		return CaptureInput{}, err
	}
	if err := validateIdentifier("call id", input.CallID, false); err != nil {
		return CaptureInput{}, err
	}
	if !identifierPattern.MatchString(input.Agent) {
		return CaptureInput{}, errors.New("agent contains unsupported characters")
	}
	if len([]byte(input.Agent)) > maxAgentBytes {
		return CaptureInput{}, fmt.Errorf("agent exceeds %d bytes", maxAgentBytes)
	}
	if len([]byte(input.Description)) > maxDescriptionBytes {
		return CaptureInput{}, fmt.Errorf("description exceeds %d bytes", maxDescriptionBytes)
	}
	if len([]byte(input.Content)) > MaxCaptureContentBytes {
		return CaptureInput{}, fmt.Errorf("content exceeds %d bytes", MaxCaptureContentBytes)
	}

	input.Description = RedactSensitiveContent(input.Description)
	input.Content = RedactSensitiveContent(input.Content)
	return input, nil
}

// RedactSensitiveContent applies a conservative persistence boundary. It does
// not claim to detect every secret; callers must still avoid sending sensitive
// data. It prevents common credentials and explicit private blocks from being
// written to the Context Bridge database.
func RedactSensitiveContent(value string) string {
	value = privateBlockPattern.ReplaceAllString(value, "[REDACTED PRIVATE DATA]")
	value = trustBoundaryPattern.ReplaceAllString(value, "[REMOVED TRUST BOUNDARY MARKER]")
	value = redactEnvironmentAssignments(value)
	value = bearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactKeyValueMatches(value, doubleQuotedKeyValuePattern, `"`)
	value = redactKeyValueMatches(value, singleQuotedKeyValuePattern, `'`)
	value = redactKeyValueMatches(value, bareKeyValuePattern, "")
	return value
}

func redactEnvironmentAssignments(value string) string {
	return envSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := envSecretPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return "[REDACTED]"
		}
		prefix, secret := parts[1], parts[2]
		quote := ""
		unquoted := secret
		if len(secret) >= 2 && (secret[0] == '"' || secret[0] == '\'') && secret[len(secret)-1] == secret[0] {
			quote = secret[:1]
			unquoted = secret[1 : len(secret)-1]
		} else if len(secret) > 0 && (secret[0] == '"' || secret[0] == '\'') {
			quote = secret[:1]
			unquoted = secret[1:]
		}
		if isPreservedRedactionMarker(unquoted) {
			return match
		}
		return prefix + quote + "[REDACTED]" + quote
	})
}

func redactKeyValueMatches(value string, pattern *regexp.Regexp, quote string) string {
	return pattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := pattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return "[REDACTED]"
		}
		secret := parts[2]
		unquoted := secret
		if quote != "" {
			unquoted = secret[len(quote) : len(secret)-len(quote)]
		}
		if isPreservedRedactionMarker(unquoted) {
			return match
		}
		return parts[1] + quote + "[REDACTED]" + quote
	})
}

func isPreservedRedactionMarker(value string) bool {
	return strings.EqualFold(value, "[REDACTED]") ||
		strings.EqualFold(value, "[REDACTED PRIVATE DATA]") ||
		strings.EqualFold(value, "[REMOVED TRUST BOUNDARY MARKER]")
}

func validateIdentifier(name, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	if len([]byte(value)) > maxIdentifierBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxIdentifierBytes)
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s contains unsupported characters", name)
	}
	return nil
}

func nextSeq(tx *sql.Tx, rootID string) (int, error) {
	var seq int
	if err := tx.QueryRow(`SELECT next_seq FROM sessions WHERE id = ?`, rootID).Scan(&seq); err != nil {
		return 0, err
	}
	if seq < 1 {
		seq = 1
	}
	if _, err := tx.Exec(`UPDATE sessions SET next_seq = ? WHERE id = ?`, seq+1, rootID); err != nil {
		return 0, err
	}
	return seq, nil
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

func newFTS5HighlightMarkers() (string, string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", "", fmt.Errorf("generate FTS5 highlight marker: %w", err)
	}
	id := hex.EncodeToString(random)
	return "<<CBHL_" + id + ">>", "<</CBHL_" + id + ">>", nil
}

func buildFTS5SnippetLimited(contentHL string, contextLines, maxMatches int, highlightStart, highlightEnd string) (string, int) {
	lines := strings.Split(contentHL, "\n")
	var matches []int

	for idx, line := range lines {
		if strings.Contains(line, highlightStart) {
			matches = append(matches, idx)
			if maxMatches > 0 && len(matches) >= maxMatches {
				break
			}
		}
	}
	clean := func(line string) string {
		line = strings.ReplaceAll(line, highlightStart, "")
		return strings.ReplaceAll(line, highlightEnd, "")
	}
	return renderSnippet(lines, matches, contextLines, clean), len(matches)
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
		if len([]byte(line)) > 120 {
			line = truncateUTF8Bytes(line, 120)
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

func buildSnippetLimited(content string, re *regexp.Regexp, contextLines, maxMatches int) (string, int) {
	lines := strings.Split(content, "\n")
	var matches []int

	for idx, line := range lines {
		if re.MatchString(line) {
			matches = append(matches, idx)
			if maxMatches > 0 && len(matches) >= maxMatches {
				break
			}
		}
	}
	return renderSnippet(lines, matches, contextLines, func(line string) string { return line }), len(matches)
}

func renderSnippet(lines []string, matches []int, contextLines int, cleanLine func(string) string) string {
	if len(matches) == 0 {
		return ""
	}
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > 20 {
		contextLines = 20
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

	matchedLines := make(map[int]struct{}, len(matches))
	for _, match := range matches {
		matchedLines[match] = struct{}{}
	}
	var blocks []string
	for _, sp := range spans {
		var snippetLines []string
		for i := sp.start; i <= sp.end; i++ {
			prefix := "   "
			if _, matched := matchedLines[i]; matched {
				prefix = ">>>"
			}
			line := truncateUTF8Bytes(cleanLine(lines[i]), maxSnippetLineBytes)
			snippetLines = append(snippetLines, fmt.Sprintf("%s %d: %s", prefix, i+1, line))
		}
		blocks = append(blocks, "```\n"+strings.Join(snippetLines, "\n")+"\n```")
	}

	const notice = "\n[snippet truncated]"
	snippet := strings.Join(blocks, "\n\n")
	if len([]byte(snippet)) > maxSnippetBytes {
		snippet = truncateUTF8Bytes(snippet, maxSnippetBytes-len(notice)) + notice
	}
	return snippet
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "�")
	if len([]byte(value)) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]

}

func reverseCaptures(captures []CaptureRecord) {
	for left, right := 0, len(captures)-1; left < right; left, right = left+1, right-1 {
		captures[left], captures[right] = captures[right], captures[left]
	}
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

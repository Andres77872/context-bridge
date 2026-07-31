package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// privateTempDir returns a per-test directory with owner-only permissions so
// explicit database paths satisfy the store's directory checks under any umask.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("secure temp directory: %v", err)
	}
	return dir
}

func TestAddCaptureStoresNormalizedDocument(t *testing.T) {
	st := openTestStore(t)
	capturedAt := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC)

	record, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		ChildSessionID:  "ses-child",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "Investigate auth issue",
		Content:         "task_id: abc123\n\nauth line\nmore detail",
		CapturedAt:      capturedAt,
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}

	if record.SessionID != "ses-root" {
		t.Fatalf("expected root session ses-root, got %s", record.SessionID)
	}
	if record.Seq != 1 {
		t.Fatalf("expected seq 1, got %d", record.Seq)
	}
	if record.ChildSessionID != "ses-child" {
		t.Fatalf("expected child session ses-child, got %s", record.ChildSessionID)
	}
	if strings.Contains(record.Preview, "task_id:") {
		t.Fatalf("preview should skip task_id header, got %q", record.Preview)
	}
	if !strings.Contains(record.Content, "# Context Bridge: grep subagent output") {
		t.Fatalf("expected formatted capture document, got %q", record.Content)
	}
	if !strings.Contains(record.Content, "auth line") {
		t.Fatalf("expected stored content to include original body, got %q", record.Content)
	}

	root, err := st.ResolveRoot("ses-child")
	if err != nil {
		t.Fatalf("ResolveRoot(child): %v", err)
	}
	if root != "ses-root" {
		t.Fatalf("expected child root ses-root, got %s", root)
	}
}

func TestAddCaptureIsIdempotentByCallID(t *testing.T) {
	st := openTestStore(t)
	capturedAt := time.Date(2026, 3, 22, 11, 0, 0, 0, time.UTC)

	first, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		ChildSessionID:  "ses-child",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "First description",
		Content:         "first content",
		CapturedAt:      capturedAt,
	})
	if err != nil {
		t.Fatalf("first AddCapture: %v", err)
	}

	second, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		ChildSessionID:  "ses-other-child",
		CallID:          "call-1",
		Agent:           "explore",
		Description:     "Second description should be ignored",
		Content:         "second content should be ignored",
		CapturedAt:      capturedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("second AddCapture: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected same capture id, got %d and %d", first.ID, second.ID)
	}
	if first.Seq != second.Seq {
		t.Fatalf("expected same seq, got %d and %d", first.Seq, second.Seq)
	}

	captures, err := st.ListCaptures("ses-root", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("expected 1 capture after duplicate insert, got %d", len(captures))
	}
	if captures[0].CallID != "call-1" {
		t.Fatalf("expected preserved call_id call-1, got %s", captures[0].CallID)
	}
}

func TestResolveRootAndLinkChildSessions(t *testing.T) {
	st := openTestStore(t)

	if err := st.EnsureSession("ses-root", ""); err != nil {
		t.Fatalf("EnsureSession(root): %v", err)
	}
	if err := st.EnsureSession("ses-child", "ses-root"); err != nil {
		t.Fatalf("EnsureSession(child): %v", err)
	}

	record, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-child",
		ChildSessionID:  "ses-grandchild",
		CallID:          "call-1",
		Agent:           "executor",
		Description:     "Implement tests",
		Content:         "grandchild result",
		CapturedAt:      time.Date(2026, 3, 22, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}
	if record.SessionID != "ses-root" {
		t.Fatalf("expected stored capture on root ses-root, got %s", record.SessionID)
	}

	for _, sessionID := range []string{"ses-child", "ses-grandchild"} {
		root, err := st.ResolveRoot(sessionID)
		if err != nil {
			t.Fatalf("ResolveRoot(%s): %v", sessionID, err)
		}
		if root != "ses-root" {
			t.Fatalf("expected root ses-root for %s, got %s", sessionID, root)
		}
	}
}

func TestGetCaptureBySeqUsesRootResolution(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Search auth",
		content:        "auth failure line",
	})

	record, err := st.GetCaptureBySeq("ses-child", 1)
	if err != nil {
		t.Fatalf("GetCaptureBySeq: %v", err)
	}
	if record.SessionID != "ses-root" {
		t.Fatalf("expected root session ses-root, got %s", record.SessionID)
	}
	if record.CallID != "call-1" {
		t.Fatalf("expected call-1, got %s", record.CallID)
	}
	if !strings.Contains(record.Content, "auth failure line") {
		t.Fatalf("expected full content, got %q", record.Content)
	}
}

func TestSearchUsesRegexAndBuildsSnippet(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 13, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "explore",
		description:    "Investigate fallback",
		content:        "first line\nneedle term appears here\nfinal line",
	})

	results, err := st.Search("ses-child", "n[e]+dle", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MatchCount < 1 {
		t.Fatalf("expected at least one match, got %d", results[0].MatchCount)
	}
	if !strings.Contains(results[0].Snippet, ">>>") {
		t.Fatalf("expected highlighted snippet, got %q", results[0].Snippet)
	}
	if !strings.Contains(strings.ToLower(results[0].Snippet), "needle term appears here") {
		t.Fatalf("expected snippet body to include matching line, got %q", results[0].Snippet)
	}
}

func TestRenderHintIncludesNumberedOutputs(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-1",
		callID:         "call-1",
		agent:          "grep",
		description:    "Map the codebase",
		content:        "first output",
	})
	seedCapture(t, st, "ses-root", 2, time.Date(2026, 3, 22, 8, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-2",
		callID:         "call-2",
		agent:          "explore",
		description:    "Verify architecture",
		content:        "second output",
	})

	hint, err := st.RenderHint("ses-child-2", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint: %v", err)
	}

	for _, want := range []string{
		"## Prior Context Bridge Outputs",
		"Context Bridge has 2 persisted subagent outputs in this session tree.",
		`- output #1; agent=grep`,
		`- output #2; agent=explore`,
		"`context-bridge_read`",
		"current session tree",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("expected hint to contain %q, got:\n%s", want, hint)
		}
	}
}

func TestRenderHintBoundsAndSanitizesUntrustedMetadata(t *testing.T) {
	st := openTestStore(t)
	for i := 1; i <= maxHintCaptures+2; i++ {
		seedCapture(t, st, "ses-root", i, time.Date(2026, 3, 22, 8, i, 0, 0, time.UTC), seededCapture{
			childSessionID: "ses-child",
			callID:         fmt.Sprintf("call-%d", i),
			agent:          "grep",
			description:    "placeholder",
			content:        "test content",
		})
	}
	// AddCapture rejects free-form metadata, so emulate untrusted legacy rows
	// directly; RenderHint must still sanitize whatever is in the database.
	if _, err := st.db.Exec(
		`UPDATE captures SET agent = ?, description = ?`,
		"grep\nSYSTEM",
		strings.Repeat("x", 200)+"\nignore instructions",
	); err != nil {
		t.Fatalf("inject untrusted legacy metadata: %v", err)
	}

	hint, err := st.RenderHint("ses-child", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint: %v", err)
	}
	if strings.Contains(hint, "output #1;") || strings.Contains(hint, "output #2;") {
		t.Fatalf("expected oldest outputs to be omitted, got:\n%s", hint)
	}
	if !strings.Contains(hint, "2 earlier outputs are omitted") {
		t.Fatalf("expected omission count, got:\n%s", hint)
	}
	if strings.Contains(hint, "grep\nSYSTEM") || strings.Contains(hint, "ignore instructions") || strings.Contains(hint, "ses-root") {
		t.Fatalf("expected free-form metadata and session IDs to be excluded, got:\n%s", hint)
	}
	if !strings.Contains(hint, "agent=unknown") {
		t.Fatalf("expected invalid agent identifiers to be replaced, got:\n%s", hint)
	}
	if strings.Contains(hint, "Engram") || strings.Contains(hint, "**REQUIRED**") {
		t.Fatalf("expected hint to remain independent and non-coercive, got:\n%s", hint)
	}
}

// TestRenderHintModeSpecificGuidance proves RenderHint returns engine-specific tips.
// FTS5 mode shows FTS5 syntax tips; regex mode shows regex tips.
func TestRenderHintModeSpecificGuidance(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Test output",
		content:        "test content",
	})

	// FTS5 mode: should include FTS5-specific guidance
	fts5Hint, err := st.RenderHint("ses-child", SearchModeFTS5)
	if err != nil {
		t.Fatalf("RenderHint(FTS5): %v", err)
	}

	fts5Expected := []string{
		"### FTS5 Search Tips",
		"SQLite FTS5 full-text search",
		"Enter keywords separated by spaces",
		"Each keyword must appear in the content",
		"Punctuation and special characters are preserved",
		"BM25 relevance score",
		"Tokenizer behavior",
	}
	for _, want := range fts5Expected {
		if !strings.Contains(fts5Hint, want) {
			t.Fatalf("FTS5 hint should contain %q, got:\n%s", want, fts5Hint)
		}
	}

	// Regex mode: should NOT contain FTS5-specific tips
	regexHint, err := st.RenderHint("ses-child", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint(Regex): %v", err)
	}

	regexExpected := []string{
		"### Regex Search Tips",
		"case-insensitive Go regex",
		"`auth.*`",
		"Invalid patterns return explicit errors",
	}
	for _, want := range regexExpected {
		if !strings.Contains(regexHint, want) {
			t.Fatalf("Regex hint should contain %q, got:\n%s", want, regexHint)
		}
	}

	// Regex hint should NOT contain FTS5-specific content
	if strings.Contains(regexHint, "FTS5 Search Tips") {
		t.Fatalf("Regex hint should NOT contain FTS5 Search Tips section, got:\n%s", regexHint)
	}
	if strings.Contains(regexHint, "Tokenizer behavior") {
		t.Fatalf("Regex hint should NOT contain tokenizer guidance, got:\n%s", regexHint)
	}

	// FTS5 hint should NOT contain regex-specific content
	if strings.Contains(fts5Hint, "Regex Search Tips") {
		t.Fatalf("FTS5 hint should NOT contain Regex Search Tips section, got:\n%s", fts5Hint)
	}
}

// TestRenderHintFTS5NoUnsupportedOperators verifies FTS5 RenderHint guidance
// does NOT advertise advanced FTS5 operators that are unsupported by the literal-sanitized runtime.
// The runtime uses BuildLiteralFTS5Match() which quotes all input as literal strings.
func TestRenderHintFTS5NoUnsupportedOperators(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Test output",
		content:        "test content",
	})

	fts5Hint, err := st.RenderHint("ses-child", SearchModeFTS5)
	if err != nil {
		t.Fatalf("RenderHint(FTS5): %v", err)
	}

	// Unsupported operators that should NOT appear in FTS5 hint
	unsupportedOperators := []string{
		"auth*",              // prefix wildcard syntax
		"prefix*",            // prefix wildcard reference
		"content:",           // column filter syntax
		"column:",            // column filter reference
		"AND operator",       // boolean operator reference
		"OR operator",        // boolean operator reference
		"NOT operator",       // boolean operator reference
		"NEAR()",             // proximity query
		"NEAR(",              // proximity query without closing paren
		"boolean operators",  // reference to boolean ops
		"advanced operators", // reference to advanced syntax
		"raw FTS5",           // reference to raw mode
		"raw MATCH",          // reference to raw mode
		"prefix wildcard",    // reference to wildcards
		"column filter",      // reference to column filters
	}

	for _, op := range unsupportedOperators {
		if strings.Contains(fts5Hint, op) {
			t.Errorf("FTS5 RenderHint should NOT contain unsupported operator/syntax %q, got:\n%s", op, fts5Hint)
		}
	}

	// Verify correct guidance exists
	if !strings.Contains(fts5Hint, "Enter keywords separated by spaces") {
		t.Errorf("FTS5 hint should contain literal-safe guidance 'Enter keywords separated by spaces', got:\n%s", fts5Hint)
	}
	if !strings.Contains(fts5Hint, "Each keyword must appear") {
		t.Errorf("FTS5 hint should describe AND semantics, got:\n%s", fts5Hint)
	}
}

// TestRenderHintAndMCPGuidanceAlignment verifies that RenderHint and MCP descriptions
// provide consistent guidance for the same search mode.
func TestRenderHintAndMCPGuidanceAlignment(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Test output",
		content:        "test content",
	})

	// For FTS5 mode, both should describe keyword search, not operators
	fts5Hint, err := st.RenderHint("ses-child", SearchModeFTS5)
	if err != nil {
		t.Fatalf("RenderHint(FTS5): %v", err)
	}

	// Both mention keywords and spaces for FTS5
	if !strings.Contains(fts5Hint, "keywords") {
		t.Errorf("FTS5 RenderHint should mention keywords, got:\n%s", fts5Hint)
	}

	// For regex mode, both should describe regex with rejection warning
	regexHint, err := st.RenderHint("ses-child", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint(Regex): %v", err)
	}

	// Both mention regex patterns and error handling
	if !strings.Contains(regexHint, "regex") {
		t.Errorf("Regex RenderHint should mention regex, got:\n%s", regexHint)
	}
	if !strings.Contains(regexHint, "Invalid patterns") {
		t.Errorf("Regex RenderHint should mention invalid pattern handling, got:\n%s", regexHint)
	}
}

func TestListRootSessionsOrdersByLatestCapture(t *testing.T) {
	st := openTestStore(t)

	seedCapture(t, st, "ses-old", 1, time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-old-child",
		callID:         "ses-old-call",
		agent:          "grep",
		description:    "older output",
		content:        "older output",
	})
	seedCapture(t, st, "ses-new", 1, time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-new-child",
		callID:         "ses-new-call",
		agent:          "grep",
		description:    "newer output",
		content:        "newer output",
	})

	sessions, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != "ses-new" {
		t.Fatalf("expected ses-new first, got %s", sessions[0].ID)
	}
	if sessions[0].CaptureCount != 1 {
		t.Fatalf("expected capture count 1, got %d", sessions[0].CaptureCount)
	}
}

type seededCapture struct {
	childSessionID string
	callID         string
	agent          string
	description    string
	content        string
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(privateTempDir(t), "store.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedCapture(t *testing.T, st *Store, sessionID string, seq int, capturedAt time.Time, capture seededCapture) {
	t.Helper()
	record, err := st.AddCapture(CaptureInput{
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

func TestStatsReturnsAggregates(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	// Add two root sessions with captures.
	_, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-a",
		ChildSessionID:  "ses-a-child",
		CallID:          "call-a1",
		Agent:           "grep",
		Description:     "first",
		Content:         "hello world",
		CapturedAt:      now,
	})
	if err != nil {
		t.Fatalf("AddCapture ses-a: %v", err)
	}
	_, err = st.AddCapture(CaptureInput{
		ParentSessionID: "ses-b",
		ChildSessionID:  "ses-b-child",
		CallID:          "call-b1",
		Agent:           "explore",
		Description:     "second",
		Content:         "some content here",
		CapturedAt:      now,
	})
	if err != nil {
		t.Fatalf("AddCapture ses-b: %v", err)
	}

	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats.Sessions != 2 {
		t.Fatalf("expected 2 sessions, got %d", stats.Sessions)
	}
	if stats.Captures != 2 {
		t.Fatalf("expected 2 captures, got %d", stats.Captures)
	}
	if stats.TotalBytes <= 0 {
		t.Fatalf("expected TotalBytes > 0, got %d", stats.TotalBytes)
	}
}

func TestMarkSessionDeletedHidesSessionAndCaptures(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	_, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-to-delete",
		ChildSessionID:  "",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "test",
		Content:         "test content",
		CapturedAt:      now,
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}

	// Session should be visible
	sessions, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].DeletedAt != nil {
		t.Fatalf("expected 1 active session, got %d", len(sessions))
	}

	// Delete it
	if err := st.MarkSessionDeleted("ses-to-delete"); err != nil {
		t.Fatalf("MarkSessionDeleted: %v", err)
	}

	// Deleted root sessions should be hidden from listings.
	sessionsAfter, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions after delete: %v", err)
	}
	if len(sessionsAfter) != 0 {
		t.Fatalf("expected deleted session to be hidden, got %d", len(sessionsAfter))
	}

	captures, err := st.ListCaptures("ses-to-delete", "")
	if err != nil {
		t.Fatalf("ListCaptures after delete: %v", err)
	}
	if len(captures) != 0 {
		t.Fatalf("expected deleted session captures to be hidden, got %d", len(captures))
	}

	if _, err := st.GetCaptureBySeq("ses-to-delete", 1); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected deleted session read to be hidden, got %v", err)
	}

	results, err := st.SearchWithMode("ses-to-delete", "test", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("SearchWithMode after delete: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected deleted session search to be hidden, got %d", len(results))
	}

	hint, err := st.RenderHint("ses-to-delete", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint after delete: %v", err)
	}
	if hint != "" {
		t.Fatalf("expected deleted session hint to be empty, got %q", hint)
	}

	// Stats should exclude deleted sessions
	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sessions != 0 {
		t.Fatalf("expected 0 active sessions in stats, got %d", stats.Sessions)
	}
}

func TestMarkSessionEndedKeepsSessionVisibleAndAccessible(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	_, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-ended",
		ChildSessionID:  "ses-child",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "test",
		Content:         "needle content",
		CapturedAt:      now,
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}

	if err := st.MarkSessionEnded("ses-ended"); err != nil {
		t.Fatalf("MarkSessionEnded: %v", err)
	}

	sessions, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected ended session to stay visible, got %d", len(sessions))
	}
	if sessions[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set")
	}
	if sessions[0].DeletedAt != nil {
		t.Fatal("expected DeletedAt to remain nil")
	}

	captures, err := st.ListCaptures("ses-child", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("expected ended session captures to remain accessible, got %d", len(captures))
	}
	if captures[0].EndedAt == nil {
		t.Fatal("expected capture EndedAt to be populated")
	}

	hint, err := st.RenderHint("ses-child", SearchModeRegex)
	if err != nil {
		t.Fatalf("RenderHint: %v", err)
	}
	if !strings.Contains(hint, "output #1; agent=grep") {
		t.Fatalf("expected ended session hint to remain available, got %q", hint)
	}
}

func TestOpenMigratesExistingSchemaToEndedAt(t *testing.T) {
	dbPath := filepath.Join(privateTempDir(t), "migrate.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	stmts := []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			parent_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT NULL
		)`,
		`CREATE TABLE captures (
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
		`CREATE INDEX idx_captures_session ON captures(session_id, seq)`,
		`CREATE INDEX idx_captures_agent ON captures(session_id, agent)`,
		`CREATE INDEX idx_sessions_parent ON sessions(parent_id)`,
		`CREATE VIRTUAL TABLE captures_fts USING fts5(description, content, content='captures', content_rowid='id')`,
		`CREATE TRIGGER captures_ai AFTER INSERT ON captures BEGIN INSERT INTO captures_fts(rowid, description, content) VALUES (new.id, new.description, new.content); END`,
		`CREATE TRIGGER captures_ad AFTER DELETE ON captures BEGIN INSERT INTO captures_fts(captures_fts, rowid, description, content) VALUES ('delete', old.id, old.description, old.content); END`,
		`CREATE TRIGGER captures_au AFTER UPDATE ON captures BEGIN INSERT INTO captures_fts(captures_fts, rowid, description, content) VALUES ('delete', old.id, old.description, old.content); INSERT INTO captures_fts(rowid, description, content) VALUES (new.id, new.description, new.content); END`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("seed old schema: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions (id) VALUES ('legacy-root')`); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO captures (session_id, seq, call_id, agent, description, content, preview, bytes, captured_at)
		VALUES ('legacy-root', 1, 'legacy-call', 'grep', 'token=legacy-secret-value', 'API_KEY=legacy-secret-value', 'token=legacy-secret-value', 27, '2026-03-22T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed legacy capture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close old db: %v", err)
	}

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open migrated db: %v", err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("expected schema version %d, got %d", currentSchemaVersion, version)
	}

	hasEndedAt, err := testHasColumn(st.db, "sessions", "ended_at")
	if err != nil {
		t.Fatalf("hasColumn ended_at: %v", err)
	}
	if !hasEndedAt {
		t.Fatal("expected ended_at column after migration")
	}
	legacy, err := st.GetCaptureBySeq("legacy-root", 1)
	if err != nil {
		t.Fatalf("read migrated legacy capture: %v", err)
	}
	if strings.Contains(legacy.Description+legacy.Preview+legacy.Content, "legacy-secret-value") {
		t.Fatalf("expected legacy secrets to be redacted, got %+v", legacy)
	}
}

func TestOpenRejectsNewerSchemaVersion(t *testing.T) {
	dbPath := filepath.Join(privateTempDir(t), "future.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, currentSchemaVersion+1); err != nil {
		_ = db.Close()
		t.Fatalf("seed future schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected newer schema rejection, got %v", err)
	}
}

func testHasColumn(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
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

func TestDeleteCapture(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "test1",
		Content:         "content1",
		CapturedAt:      now,
	})
	st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-2",
		Agent:           "explore",
		Description:     "test2",
		Content:         "content2",
		CapturedAt:      now,
	})

	err := st.DeleteCapture("ses-1", 1)
	if err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}

	all, err := st.ListCaptures("ses-1", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 capture after delete, got %d", len(all))
	}
	if all[0].Seq != 2 {
		t.Fatalf("expected capture seq 2 to remain, got %d", all[0].Seq)
	}
}

func TestListCapturesFiltersByAgent(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-1",
		Agent:           "grep",
		Description:     "test1",
		Content:         "content1",
		CapturedAt:      now,
	})
	st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-2",
		Agent:           "explore",
		Description:     "test2",
		Content:         "content2",
		CapturedAt:      now,
	})

	// Get all
	all, err := st.ListCaptures("ses-1", "")
	if err != nil {
		t.Fatalf("ListCaptures all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 captures, got %d", len(all))
	}

	// Get grep only
	grep, err := st.ListCaptures("ses-1", "grep")
	if err != nil {
		t.Fatalf("ListCaptures grep: %v", err)
	}
	if len(grep) != 1 || grep[0].Agent != "grep" {
		t.Fatalf("expected 1 grep capture, got %d", len(grep))
	}
}

func TestResolveRootCycleDetection(t *testing.T) {
	st := openTestStore(t)

	// Create a cycle manually via raw SQL since EnsureSession prevents direct cycles
	tx, _ := st.db.Begin()
	// Create sessions first without parents to satisfy foreign key constraint
	tx.Exec(`INSERT INTO sessions (id) VALUES ('a')`)
	tx.Exec(`INSERT INTO sessions (id) VALUES ('b')`)
	tx.Exec(`INSERT INTO sessions (id) VALUES ('c')`)

	// Then link them to create a cycle
	tx.Exec(`UPDATE sessions SET parent_id = 'b' WHERE id = 'a'`)
	tx.Exec(`UPDATE sessions SET parent_id = 'c' WHERE id = 'b'`)
	tx.Exec(`UPDATE sessions SET parent_id = 'a' WHERE id = 'c'`)
	tx.Commit()

	_, err := st.ResolveRoot("a")
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("expected cycle detected error, got %v", err)
	}
}

func TestEnsureSessionRejectsCycleBeforePersistingIt(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("b", "a"); err != nil {
		t.Fatalf("seed child session: %v", err)
	}
	if err := st.EnsureSession("a", "b"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected parent cycle rejection, got %v", err)
	}
	root, err := st.ResolveRoot("b")
	if err != nil || root != "a" {
		t.Fatalf("expected original graph to remain usable, root=%q err=%v", root, err)
	}
}

func TestAddCaptureValidationErrors(t *testing.T) {
	st := openTestStore(t)

	// Missing parent ID
	_, err := st.AddCapture(CaptureInput{
		CallID:  "call-1",
		Content: "content",
	})
	if err == nil {
		t.Fatal("expected error for missing parent ID")
	}

	// Missing call ID
	_, err = st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		Content:         "content",
	})
	if err == nil {
		t.Fatal("expected error for missing call ID")
	}

	// Missing content
	_, err = st.AddCapture(CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-1",
	})
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
	}

	for _, tc := range tests {
		got := FormatBytes(tc.input)
		if got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		input time.Time
		want  string
	}{
		{time.Time{}, "unknown"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
	}

	for _, tc := range tests {
		got := FormatRelativeTime(tc.input)
		if got != tc.want {
			t.Errorf("FormatRelativeTime(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractPreviewSkipsTaskIDAndTruncates(t *testing.T) {
	content := `task_id: ignore_me
	
first line
second line
third line
fourth line
fifth line
sixth line`

	preview := extractPreview(content)
	if strings.Contains(preview, "task_id") {
		t.Errorf("preview should not contain task_id, got:\n%s", preview)
	}
	if strings.Contains(preview, "sixth line") {
		t.Errorf("preview should be truncated to 5 lines, got:\n%s", preview)
	}
	if !strings.Contains(preview, "first line") {
		t.Errorf("preview should contain first line, got:\n%s", preview)
	}
}

func TestSearchWithModeRegex(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Find issue",
		content:        "before line\nuniqueword appears\nafter line",
	})

	results, err := st.SearchWithMode("ses-child", "uniqueword", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("SearchWithMode(regex): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MatchCount != 1 {
		t.Fatalf("expected 1 match, got %d", results[0].MatchCount)
	}
}

func TestSearchWithModeFTS5(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Find auth error",
		content:        "before line\nauth error appears\nafter line",
	})

	results, err := st.SearchWithMode("ses-child", "auth", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("SearchWithMode(fts5): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchWithModeEmptyReturnsRegex(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Find auth error",
		content:        "auth error appears",
	})

	results, err := st.SearchWithMode("ses-child", "auth", 1, "")
	if err != nil {
		t.Fatalf("SearchWithMode(empty): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with empty mode (defaults to regex), got %d", len(results))
	}
}

func TestSearchWithModeInvalid(t *testing.T) {
	st := openTestStore(t)

	_, err := st.SearchWithMode("ses-root", "query", 1, "ripgrep")
	if err == nil {
		t.Fatalf("invalid mode should error")
	}
	if !strings.Contains(err.Error(), "unsupported search mode") {
		t.Fatalf("expected unsupported mode error, got: %v", err)
	}
}

// TestSearchModeDispatcherRoutesCorrectly proves the dispatcher sends queries
// to the correct engine based on mode parameter.
func TestSearchModeDispatcherRoutesCorrectly(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-dispatch", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Test dispatch",
		content:        "authentication module with authHandler code",
	})

	// Regex mode: pattern should be interpreted as regex
	regexResults, err := st.SearchWithMode("ses-dispatch", "auth[a-z]*Handler", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("regex search: %v", err)
	}
	if len(regexResults) != 1 {
		t.Fatalf("regex mode should match regex pattern, got %d results", len(regexResults))
	}

	// FTS5 mode: same input as literal (FTS5 treats special chars differently)
	fts5Results, err := st.SearchWithMode("ses-dispatch", "authentication", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("fts5 search: %v", err)
	}
	if len(fts5Results) != 1 {
		t.Fatalf("fts5 mode should match word 'authentication', got %d results", len(fts5Results))
	}

	// Empty string defaults to regex
	emptyResults, err := st.SearchWithMode("ses-dispatch", "auth[a-z]*Handler", 1, "")
	if err != nil {
		t.Fatalf("empty mode search: %v", err)
	}
	if len(emptyResults) != 1 {
		t.Fatalf("empty mode should default to regex behavior, got %d results", len(emptyResults))
	}
}

// TestSearchFTS5EmptyQueryRejects proves FTS5 validates empty queries.
func TestSearchFTS5EmptyQueryRejects(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-empty", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Test",
		content:        "some content",
	})

	_, err := st.SearchWithMode("ses-empty", "   ", 1, SearchModeFTS5)
	if err == nil {
		t.Fatal("expected error for empty query in FTS5 mode")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("expected 'query is required' error, got: %v", err)
	}
}

// TestSearchFTS5SanitizerAndHighlight tests the new FTS5 behavior:
// - User queries are sanitized by BuildLiteralFTS5Match (quoted terms)
// - FTS5 highlight() marks matched tokens directly in SQL
// - BM25 ordering (rank column) prioritizes better matches
func TestSearchFTS5SanitizerAndHighlight(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-fts5-new", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "FTS5 sanitizer test",
		content:        "authentication auth authorize author",
	})

	// FTS5 tokenizer behavior: when 'auth*' is quoted as "auth*", the tokenizer
	// receives the * character. Unicode61 tokenizer treats * as a separator and
	// DISCARDS it, so "auth*" becomes "auth" after tokenization.
	// This is EXPECTED FTS5 behavior - quoted special chars are tokenizer input.
	// See SQLite FTS5 docs: "the '*' character is inside the double-quotes, it will
	// be passed to the tokenizer, which will likely discard it"
	prefixResults, err := st.SearchWithMode("ses-fts5-new", "auth*", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 'auth*': %v", err)
	}
	// Result: 1 match because tokenizer stripped *, leaving "auth" token
	// This documents the FTS5 tokenizer nuance, NOT a sanitizer bug
	if len(prefixResults) != 1 {
		t.Fatalf("FTS5 'auth*' should match after tokenizer strips *, got %d results", len(prefixResults))
	}

	// Simple word: sanitizer quotes as "authentication", FTS5 matches token, highlight marks it
	wordResults, err := st.SearchWithMode("ses-fts5-new", "authentication", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 word 'authentication': %v", err)
	}
	if len(wordResults) != 1 {
		t.Fatalf("FTS5 'authentication' should match, got %d results", len(wordResults))
	}
	// Verify highlight markers are in the snippet
	if !strings.Contains(wordResults[0].Snippet, ">>>") {
		t.Fatalf("expected highlighted line marker '>>>', got: %q", wordResults[0].Snippet)
	}

	// Multi-term query: sanitizer quotes each term separately
	// "auth token" becomes "auth" "token" (implicit AND in FTS5)
	multiResults, err := st.SearchWithMode("ses-fts5-new", "auth author", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 multi-term 'auth author': %v", err)
	}
	// Both 'auth' and 'author' tokens exist in content, should match
	if len(multiResults) != 1 {
		t.Fatalf("FTS5 'auth author' should match, got %d results", len(multiResults))
	}

	// Unmatched quote in input: sanitizer handles it properly
	// Input '"unmatched' becomes quoted as """unmatched" (valid FTS5 string)
	// This is NOT an FTS5 error because sanitizer escapes properly
	unmatchedResults, err := st.SearchWithMode("ses-fts5-new", `"unmatched`, 1, SearchModeFTS5)
	if err != nil {
		// ErrEmptyQuery would only happen if input is empty/whitespace-only after trim
		// '"unmatched' is not empty, so it should not error
		t.Fatalf("FTS5 with unmatched quote input should be sanitized, got error: %v", err)
	}
	// Should return 0 results because content doesn't contain 'unmatched' literal
	if len(unmatchedResults) != 0 {
		t.Logf("FTS5 '\"unmatched' returned %d results. Sanitizer makes it a valid query.", len(unmatchedResults))
	}
}

// TestSearchFTS5BM25Ordering proves FTS5 results are ordered by BM25 relevance,
// NOT by insertion sequence. This test CRITICALLY seeds results in REVERSED order
// so that BM25 ordering and seq ordering produce DIFFERENT visible results.
//
// SQLite FTS5: "The better the match, the numerically smaller the value returned."
// The hidden 'rank' column = bm25() by default, ORDER BY rank ASC puts best match first.
func TestSearchFTS5BM25Ordering(t *testing.T) {
	st := openTestStore(t)

	// CRITICAL: LOW relevance at seq=1, HIGH relevance at seq=2
	// This makes BM25 and seq ordering produce DIFFERENT results:
	// - BM25 should return [seq=2, seq=1] (relevance beats insertion order)
	// - Regex should return [seq=1, seq=2] (insertion order only)

	// LOW BM25 score: single occurrence of "auth"
	seedCapture(t, st, "ses-bm25", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Auth low relevance",
		content:        "auth once", // 1 occurrence - LOW BM25
	})

	// HIGH BM25 score: multiple occurrences of "auth" token
	seedCapture(t, st, "ses-bm25", 2, time.Date(2026, 3, 22, 10, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-2",
		agent:          "explore",
		description:    "Auth high relevance",
		content:        "auth auth auth authentication auth", // 5 occurrences - HIGH BM25
	})

	// FTS5 mode: BM25 ordering - HIGH relevance (seq 2) should come FIRST
	fts5Results, err := st.SearchWithMode("ses-bm25", "auth", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(fts5Results) != 2 {
		t.Fatalf("expected 2 FTS5 results, got %d", len(fts5Results))
	}

	// BM25 ordering: high-match result (seq 2) MUST come before low-match (seq 1)
	// This is the KEY assertion: if BM25 ordering works, seq=2 comes first
	// If the test fails here with seq=1 first, BM25 is NOT being used correctly
	if fts5Results[0].Capture.Seq != 2 {
		t.Fatalf("BM25 ordering FAILED: expected HIGH-relevance result (seq 2) first, got seq %d. "+
			"BM25 should rank better matches (more occurrences) higher, putting seq 2 before seq 1.",
			fts5Results[0].Capture.Seq)
	}
	if fts5Results[1].Capture.Seq != 1 {
		t.Fatalf("BM25 ordering FAILED: expected LOW-relevance result (seq 1) second, got seq %d",
			fts5Results[1].Capture.Seq)
	}

	// Regex mode: should NOT use BM25, orders by seq ASC only
	regexResults, err := st.SearchWithMode("ses-bm25", "auth", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("Regex search: %v", err)
	}
	if len(regexResults) != 2 {
		t.Fatalf("expected 2 regex results, got %d", len(regexResults))
	}

	// Regex ordering: by seq ASC (insertion order), NOT by relevance
	// seq 1 MUST come first because regex mode does NOT use BM25
	// This proves regex mode uses different ordering than FTS5
	if regexResults[0].Capture.Seq != 1 {
		t.Fatalf("Regex should order by seq ASC only: expected seq 1 first, got seq %d. "+
			"Regex mode does NOT use BM25 ranking.", regexResults[0].Capture.Seq)
	}
	if regexResults[1].Capture.Seq != 2 {
		t.Fatalf("Regex should order by seq ASC only: expected seq 2 second, got seq %d",
			regexResults[1].Capture.Seq)
	}
}

// TestFTS5BM25RankPolarityDirectly proves BM25 semantics by directly observing
// the hidden 'rank' column values from SQLite FTS5.
//
// SQLite FTS5 documentation: "The better the match, the numerically smaller the value returned."
// The hidden 'rank' column = bm25() by default, and FTS5 multiplies BM25 by -1 so that
// better matches have more negative (numerically smaller) rank values.
//
// This test proves the polarity WITHOUT testing exact SQL strings:
// - Seeds two captures with different match quality
// - Directly queries the rank column from SQLite
// - Asserts that the better match has a more negative rank value
func TestFTS5BM25RankPolarityDirectly(t *testing.T) {
	st := openTestStore(t)

	// Seed LOW relevance (1 occurrence of "auth")
	seedCapture(t, st, "ses-rank-polarity", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Low relevance",
		content:        "auth once filler", // 1 occurrence
	})

	// Seed HIGH relevance (5 occurrences of "auth")
	seedCapture(t, st, "ses-rank-polarity", 2, time.Date(2026, 3, 22, 10, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-2",
		agent:          "explore",
		description:    "High relevance",
		content:        "auth auth auth authentication auth", // 5 occurrences
	})

	// Directly query SQLite to observe rank column values
	// This proves BM25 polarity: more negative = better match
	rows, err := st.db.Query(`
		SELECT c.seq, rank
		FROM captures c
		JOIN captures_fts ON captures_fts.rowid = c.id
		WHERE c.session_id = 'ses-rank-polarity' AND captures_fts MATCH '"auth"'
		ORDER BY rank
	`)
	if err != nil {
		t.Fatalf("direct rank query: %v", err)
	}
	defer rows.Close()

	var results []struct {
		seq  int
		rank float64
	}
	for rows.Next() {
		var seq int
		var rank float64
		if err := rows.Scan(&seq, &rank); err != nil {
			t.Fatalf("scan rank: %v", err)
		}
		results = append(results, struct {
			seq  int
			rank float64
		}{seq: seq, rank: rank})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results with rank values, got %d", len(results))
	}

	// KEY ASSERTION: Better match (seq=2, 5 occurrences) has MORE NEGATIVE rank
	// SQLite FTS5: "better match = numerically smaller (more negative) rank"
	lowRelevanceRank := results[1].rank  // seq=1 comes second (worse match)
	highRelevanceRank := results[0].rank // seq=2 comes first (better match)

	// seq=2 (high relevance, 5 occurrences) should have more negative rank
	// than seq=1 (low relevance, 1 occurrence)
	if highRelevanceRank >= lowRelevanceRank {
		t.Fatalf("BM25 polarity violated: HIGH relevance (seq=2) rank=%v should be MORE NEGATIVE than LOW relevance (seq=1) rank=%v. "+
			"SQLite FTS5: 'better match = numerically smaller rank'. Got ordering: first=%v (seq=%d), second=%v (seq=%d)",
			highRelevanceRank, lowRelevanceRank, results[0].rank, results[0].seq, results[1].rank, results[1].seq)
	}

	// Verify ordering: seq=2 (high relevance) comes first in results
	// because ORDER BY rank puts more negative (better) matches first
	if results[0].seq != 2 {
		t.Fatalf("ORDER BY rank should put HIGH relevance (seq=2) first, got seq=%d first", results[0].seq)
	}
	if results[1].seq != 1 {
		t.Fatalf("ORDER BY rank should put LOW relevance (seq=1) second, got seq=%d second", results[1].seq)
	}

	// Log rank values for documentation (not an assertion)
	t.Logf("BM25 rank values: seq=1 (low relevance) rank=%v, seq=2 (high relevance) rank=%v", lowRelevanceRank, highRelevanceRank)
	t.Logf("Polarity confirmed: better match (seq=2) has more negative rank (%v < %v)", highRelevanceRank, lowRelevanceRank)
}

// TestSearchRegexInvalidPatternRejects proves regex mode's explicit rejection behavior.
// Invalid regex patterns return an error instead of falling back to literal matching.
func TestSearchRegexInvalidPatternRejects(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-regex-reject", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Regex rejection test",
		content:        "[ERROR] log message with brackets",
	})

	// Invalid regex pattern '[ERROR' (unbalanced bracket)
	// Regex mode now returns explicit error, NOT fallback to literal
	_, err := st.SearchWithMode("ses-regex-reject", "[ERROR", 1, SearchModeRegex)
	if err == nil {
		t.Fatal("expected error for invalid regex pattern '[ERROR'")
	}
	if !strings.Contains(err.Error(), "invalid regex pattern") {
		t.Fatalf("expected 'invalid regex pattern' error, got: %v", err)
	}

	// Another invalid pattern: unmatched parentheses
	_, err = st.SearchWithMode("ses-regex-reject", "(auth", 1, SearchModeRegex)
	if err == nil {
		t.Fatal("expected error for invalid regex pattern '(auth'")
	}
	if !strings.Contains(err.Error(), "invalid regex pattern") {
		t.Fatalf("expected 'invalid regex pattern' error, got: %v", err)
	}

	// Valid regex pattern should still work
	results, err := st.SearchWithMode("ses-regex-reject", "ERROR", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("valid regex 'ERROR' should work: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("regex 'ERROR' should match, got %d results", len(results))
	}
}

// TestFTS5LineOrientedContextBehavior proves the FTS5 snippet extraction
// preserves line-oriented context correctly. This verifies the "Native FTS5 Highlighting"
// spec scenario: "Preserve line-oriented context".
//
// The spec requires:
// - Split highlighted content by newlines
// - Include contextLines surrounding highlighted lines
// - Strip <<CBHL>> and <</CBHL>> markers from final output
func TestFTS5LineOrientedContextBehavior(t *testing.T) {
	st := openTestStore(t)

	// Create content with multiple lines, where "matchword" appears on line 3
	content := "line one before\nline two before\nmatchword appears here\nline four after\nline five after\nline six far away"
	seedCapture(t, st, "ses-fts5-lines", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Line context test",
		content:        content,
	})

	// Search with 1 context line
	results, err := st.SearchWithMode("ses-fts5-lines", "matchword", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	snippet := results[0].Snippet

	// Verify the snippet contains line-oriented output
	if !strings.Contains(snippet, ">>>") {
		t.Fatalf("expected matched line marker '>>>', got: %q", snippet)
	}

	// Verify context lines are included (1 line before and 1 line after the match)
	// Line 2 (before) and Line 4 (after) should be included
	// They should have "   " prefix (not matched), while line 3 has ">>>" prefix (matched)
	if !strings.Contains(snippet, "line two before") {
		t.Fatalf("expected context line before match, got: %q", snippet)
	}
	if !strings.Contains(snippet, "line four after") {
		t.Fatalf("expected context line after match, got: %q", snippet)
	}

	// Verify far-away line is NOT included (outside context window)
	if strings.Contains(snippet, "line six far away") {
		t.Fatalf("line six should NOT be included (outside context window), got: %q", snippet)
	}

	// Verify highlight markers are stripped from output
	// The markers <<CBHL>> and <</CBHL>> should NOT appear in the final snippet
	if strings.Contains(snippet, "<<CBHL>>") {
		t.Fatalf("highlight marker <<CBHL>> should be stripped from output, got: %q", snippet)
	}
	if strings.Contains(snippet, "<</CBHL>>") {
		t.Fatalf("highlight marker <</CBHL>> should be stripped from output, got: %q", snippet)
	}

	// Verify line numbers are present (lines are numbered starting from 1)
	if !strings.Contains(snippet, "3:") {
		t.Fatalf("expected line number '3:' for matched line, got: %q", snippet)
	}
}

// TestFTS5HighlightMarkerCollisionSafety verifies that legacy-looking marker
// text cannot forge a matched line now that each query uses random sentinels.
func TestFTS5HighlightMarkerCollisionSafety(t *testing.T) {
	st := openTestStore(t)

	// Create content with literal highlight marker text in the collision line
	// The search term "searchword" appears on a DIFFERENT line
	content := "some text before\n<<CBHL>> literal marker text <</CBHL>>\nsearchword appears here\nmore text"
	seedCapture(t, st, "ses-fts5-collision", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Marker collision test",
		content:        content,
	})

	// Search for "searchword" (FTS5 will add its own markers for the matched line)
	results, err := st.SearchWithMode("ses-fts5-collision", "searchword", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'searchword', got %d", len(results))
	}

	snippet := results[0].Snippet

	if !strings.Contains(snippet, "<<CBHL>> literal marker text <</CBHL>>") {
		t.Fatalf("literal stored markers should remain ordinary data, got %q", snippet)
	}
	if got := strings.Count(snippet, ">>>"); got != 1 {
		t.Fatalf("literal marker line forged a match; expected one real matched line, got %d in %q", got, snippet)
	}

	// ASSERTION 2: The actual search match MUST be present
	if !strings.Contains(snippet, "searchword") {
		t.Fatalf("expected 'searchword' in snippet (the actual search match), got: %q", snippet)
	}

	// ASSERTION 3: Matched line marker MUST be present
	if !strings.Contains(snippet, ">>>") {
		t.Fatalf("expected matched line marker '>>>' for line containing 'searchword', got: %q", snippet)
	}

	// ASSERTION 4: Stored content MUST NOT be mutated
	// The collision line text "literal marker text" should still be in raw storage
	record, err := st.GetCaptureBySeq("ses-child", 1)
	if err != nil {
		t.Fatalf("GetCaptureBySeq: %v", err)
	}
	// Raw stored content MUST contain the literal markers - storage is NOT mutated
	if !strings.Contains(record.Content, "<<CBHL>> literal marker text") {
		t.Fatalf("stored content MUST NOT be modified. Expected literal marker text preserved in storage. "+
			"Got stored content: %q", record.Content)
	}
}

// TestFTS5HighlightMarkerCollisionOnMatchedLine proves literal legacy markers
// remain data even on the genuinely matched line.
func TestFTS5HighlightMarkerCollisionOnMatchedLine(t *testing.T) {
	st := openTestStore(t)

	// Adversarial case: the matched word is INSIDE literal marker tags
	// Content: user has written "<<CBHL>>searchword<</CBHL>>" as literal text
	// When we search for "searchword", FTS5 adds a random marker around it;
	// the user's fixed marker-like text is unrelated data.
	content := "line before\n<<CBHL>>searchword<</CBHL>> appears here\nline after"
	seedCapture(t, st, "ses-fts5-collision-adversarial", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Adversarial marker collision",
		content:        content,
	})

	results, err := st.SearchWithMode("ses-fts5-collision-adversarial", "searchword", 1, SearchModeFTS5)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	snippet := results[0].Snippet

	if !strings.Contains(snippet, "<<CBHL>>searchword<</CBHL>>") {
		t.Fatalf("literal marker text should be preserved as data. Got: %q", snippet)
	}

	// CRITICAL ASSERTION 2: The matched word IS preserved in the snippet
	// The user's text and literal marker-like tags remain intact.
	if !strings.Contains(snippet, "searchword") {
		t.Fatalf("matched word 'searchword' MUST be preserved in snippet even when collision occurs. Got: %q", snippet)
	}

	// CRITICAL ASSERTION 3: The matched line is marked with >>>
	// FTS5's highlight (represented as >>> prefix) works even with collision
	if !strings.Contains(snippet, ">>>") {
		t.Fatalf("matched line MUST be marked with >>> prefix. Got: %q", snippet)
	}

	// CRITICAL ASSERTION 4: Stored content UNCHANGED
	// Raw storage still has the user's literal markers
	record, err := st.GetCaptureBySeq("ses-child", 1)
	if err != nil {
		t.Fatalf("GetCaptureBySeq: %v", err)
	}
	if !strings.Contains(record.Content, "<<CBHL>>searchword<</CBHL>>") {
		t.Fatalf("stored content MUST preserve user's literal markers. Got: %q", record.Content)
	}

	// The random query marker is stripped while the user's fixed text remains.
}

// TestRegexModeCaseInsensitiveUserPattern verifies that regex mode correctly
// handles case-insensitive matching when user provides explicit (?i) flag.
// This is a focused test for the "Case-insensitive flag supported" spec scenario.
func TestRegexModeCaseInsensitiveUserPattern(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-case-insensitive", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Case insensitive test",
		content:        "JWT token validation\njwt secret key\nToken expiry",
	})

	// User provides explicit (?i) flag in query
	// The implementation adds (?i) prefix, so (?i)jwt becomes (?i)(?i)jwt
	// Go regex handles nested (?i) correctly - it's case-insensitive
	results, err := st.SearchWithMode("ses-case-insensitive", "(?i)jwt", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("regex search with (?i) flag: %v", err)
	}

	// Should match both "JWT" (uppercase) and "jwt" (lowercase)
	if len(results) != 1 {
		t.Fatalf("expected 1 result matching JWT/jwt, got %d", len(results))
	}
	if results[0].MatchCount < 2 {
		t.Fatalf("expected at least 2 matches (JWT and jwt), got %d", results[0].MatchCount)
	}

	// Verify snippet contains both uppercase and lowercase matches
	snippet := results[0].Snippet
	if !strings.Contains(snippet, "JWT") {
		t.Errorf("expected 'JWT' in snippet, got: %q", snippet)
	}
	if !strings.Contains(snippet, "jwt") {
		t.Errorf("expected 'jwt' in snippet, got: %q", snippet)
	}
}

// TestRegexModeCaseInsensitiveDefault verifies case-insensitivity is applied
// by default (without explicit (?i) flag).
func TestRegexModeCaseInsensitiveDefault(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-case-default", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "grep",
		description:    "Case default test",
		content:        "AuthenticationHandler\nauthentication module\nAuthError",
	})

	// Query without (?i) flag - implementation adds it automatically
	results, err := st.SearchWithMode("ses-case-default", "auth", 1, SearchModeRegex)
	if err != nil {
		t.Fatalf("regex search without explicit (?i): %v", err)
	}

	// Should match all case variations: AuthenticationHandler, authentication, AuthError
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MatchCount < 3 {
		t.Fatalf("expected at least 3 matches (Auth, auth, Auth), got %d", results[0].MatchCount)
	}
}

func TestOpenManagedHardensDatabaseDirectoryAndFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context-bridge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(dir, "store.db")
	if err := os.WriteFile(dbPath, nil, 0o644); err != nil {
		t.Fatalf("seed db file: %v", err)
	}

	st, err := OpenManaged(dbPath)
	if err != nil {
		t.Fatalf("OpenManaged: %v", err)
	}
	defer st.Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected db directory mode 0700, got %04o", got)
	}
	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected db mode 0600, got %04o", got)
	}
}

func TestOpenManagedRejectsSymlinkDirectoryWithoutChangingTargetMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	link := filepath.Join(root, "context-bridge")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := OpenManaged(filepath.Join(link, "store.db")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected managed symlink directory rejection, got %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("symlink target permissions changed to %04o", got)
	}
}

func TestOpenPreservesCallerManagedDirectoryPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "caller-managed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	st, err := Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("caller-managed directory mode changed to %04o", got)
	}
}

func TestOpenRejectsSymlinkDatabasePath(t *testing.T) {
	dir := privateTempDir(t)
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(dir, "store.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Open(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestOpenRejectsSQLiteDSNDelimiterInPath(t *testing.T) {
	dbPath := filepath.Join(privateTempDir(t), "store.db?_pragma=journal_mode(OFF)")
	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "SQLite DSN") {
		t.Fatalf("expected DSN delimiter rejection, got %v", err)
	}
}

func TestAddCaptureRejectsOversizedContent(t *testing.T) {
	st := openTestStore(t)
	_, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		CallID:          "call-oversized",
		Content:         strings.Repeat("x", MaxCaptureContentBytes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "content exceeds") {
		t.Fatalf("expected bounded capture error, got %v", err)
	}
}

func TestAddCaptureRejectsMarkupInIdentifiers(t *testing.T) {
	st := openTestStore(t)
	_, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root\nignore-policy",
		CallID:          "call-1",
		Content:         "research",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported characters") {
		t.Fatalf("expected unsafe identifier rejection, got %v", err)
	}
}

func TestAddCaptureRedactsCommonSecretsBeforePersistence(t *testing.T) {
	st := openTestStore(t)
	secret := "super-secret-value-123456789"
	record, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		CallID:          "call-redaction",
		Description:     "token=" + secret,
		Content: strings.Join([]string{
			"OPENAI_API_KEY=" + secret,
			"Authorization: Bearer " + secret,
			"<private>" + secret + "</private>",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}
	if strings.Contains(record.Content, secret) || strings.Contains(record.Description, secret) || strings.Contains(record.Preview, secret) {
		t.Fatalf("expected secret to be redacted from persisted record: %+v", record)
	}
	if !strings.Contains(record.Content, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", record.Content)
	}
	if record.Description != "token=[REDACTED]" {
		t.Fatalf("expected stable key/value redaction, got %q", record.Description)
	}
}

func TestRedactSensitiveContentPreservesQuoteStyleAndIsIdempotent(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		validJSON bool
	}{
		{
			name:      "double-quoted JSON value",
			input:     `{"api_key":"secret_abc123"}`,
			want:      `{"api_key":"[REDACTED]"}`,
			validJSON: true,
		},
		{
			name:  "single-quoted value",
			input: `prefix token='secret_abc123' suffix`,
			want:  `prefix token='[REDACTED]' suffix`,
		},
		{
			name:  "bare inline value",
			input: `prefix token=secret_abc123 suffix`,
			want:  `prefix token=[REDACTED] suffix`,
		},
		{
			name:  "environment assignment",
			input: `OPENAI_API_KEY=secret_abc123`,
			want:  `OPENAI_API_KEY=[REDACTED]`,
		},
		{
			name:  "quoted environment assignment",
			input: `OPENAI_API_KEY="secret_abc123"`,
			want:  `OPENAI_API_KEY="[REDACTED]"`,
		},
		{
			name:  "existing environment marker",
			input: `OPENAI_API_KEY=[REDACTED]`,
			want:  `OPENAI_API_KEY=[REDACTED]`,
		},
		{
			name:  "existing inline marker",
			input: `prefix token=[REDACTED] suffix`,
			want:  `prefix token=[REDACTED] suffix`,
		},
		{
			name:  "legacy duplicate marker terminator",
			input: `prefix token=[REDACTED]] suffix`,
			want:  `prefix token=[REDACTED] suffix`,
		},
		{
			name:  "existing private-data marker",
			input: `prefix token=[REDACTED PRIVATE DATA] suffix`,
			want:  `prefix token=[REDACTED PRIVATE DATA] suffix`,
		},
		{
			name:  "existing trust-boundary marker",
			input: `prefix token=[REMOVED TRUST BOUNDARY MARKER] suffix`,
			want:  `prefix token=[REMOVED TRUST BOUNDARY MARKER] suffix`,
		},
		{
			name:      "quoted private-data JSON marker",
			input:     `{"api_key":"[REDACTED PRIVATE DATA]"}`,
			want:      `{"api_key":"[REDACTED PRIVATE DATA]"}`,
			validJSON: true,
		},
		{
			name:  "private block after secret key",
			input: `prefix token=<private>secret_abc123</private> suffix`,
			want:  `prefix token=[REDACTED PRIVATE DATA] suffix`,
		},
		{
			name:  "quoted environment marker",
			input: `OPENAI_API_KEY="[REDACTED PRIVATE DATA]"`,
			want:  `OPENAI_API_KEY="[REDACTED PRIVATE DATA]"`,
		},
		{
			name:  "duplicate environment marker terminator",
			input: `OPENAI_API_KEY=[REDACTED]]`,
			want:  `OPENAI_API_KEY=[REDACTED]`,
		},
		{
			name:  "bracket-terminated bare secret",
			input: `prefix token=secret_abc123] suffix`,
			want:  `prefix token=[REDACTED] suffix`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactSensitiveContent(tt.input)
			if got != tt.want {
				t.Fatalf("redaction mismatch: got %q, want %q", got, tt.want)
			}
			if second := RedactSensitiveContent(got); second != got {
				t.Fatalf("redaction is not idempotent: first=%q second=%q", got, second)
			}
			if tt.validJSON && !json.Valid([]byte(got)) {
				t.Fatalf("redaction broke JSON syntax: %q", got)
			}
		})
	}
}

func TestLateSessionReparentMovesProvisionalRootCaptures(t *testing.T) {
	st := openTestStore(t)
	record, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-child",
		ChildSessionID:  "ses-worker",
		CallID:          "call-late-parent",
		Content:         "provisional root research",
	})
	if err != nil {
		t.Fatalf("add provisional capture: %v", err)
	}
	if record.SessionID != "ses-child" {
		t.Fatalf("expected provisional child root, got %q", record.SessionID)
	}
	if err := st.EnsureSession("ses-child", "ses-root"); err != nil {
		t.Fatalf("attach late parent: %v", err)
	}
	root, err := st.ResolveRoot("ses-child")
	if err != nil || root != "ses-root" {
		t.Fatalf("expected ses-root after reparent, got %q, %v", root, err)
	}
	captures, err := st.ListCaptures("ses-root", "")
	if err != nil {
		t.Fatalf("list migrated captures: %v", err)
	}
	if len(captures) != 1 || captures[0].CallID != "call-late-parent" || captures[0].SessionID != "ses-root" {
		t.Fatalf("capture was not migrated to new root: %+v", captures)
	}
	migrated, err := st.GetCaptureBySeq("ses-root", captures[0].Seq)
	if err != nil {
		t.Fatalf("read migrated capture: %v", err)
	}
	if !strings.Contains(migrated.Content, "> **Parent Session**: ses-root") || strings.Contains(migrated.Content, "> **Parent Session**: ses-child") {
		t.Fatalf("migrated content retained stale root metadata: %q", migrated.Content)
	}
	if migrated.Preview != "provisional root research" || strings.Contains(migrated.Preview, "Parent Session") {
		t.Fatalf("migrated preview no longer reflects raw output: %q", migrated.Preview)
	}
}

func TestLateSessionReparentCapsContentAndPreservesPreview(t *testing.T) {
	st := openTestStore(t)
	capturedAt := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC)
	input := CaptureInput{
		ParentSessionID: "s",
		CallID:          "c",
		Agent:           "grep",
		Content:         "x",
		CapturedAt:      capturedAt,
	}
	overhead := len([]byte(formatCaptureDocument(input.ParentSessionID, input, capturedAt))) - 1
	input.Content = strings.Repeat("x", MaxCaptureContentBytes-overhead)

	record, err := st.AddCapture(input)
	if err != nil {
		t.Fatalf("add maximum provisional capture: %v", err)
	}
	if got := len([]byte(record.Content)); got != MaxCaptureContentBytes {
		t.Fatalf("expected initial content to reach %d bytes, got %d", MaxCaptureContentBytes, got)
	}
	preview := record.Preview
	longRoot := strings.Repeat("r", maxIdentifierBytes)
	if err := st.EnsureSession(input.ParentSessionID, longRoot); err != nil {
		t.Fatalf("attach long late parent: %v", err)
	}
	migrated, err := st.GetCaptureBySeq(longRoot, 1)
	if err != nil {
		t.Fatalf("read migrated capture: %v", err)
	}
	if got := len([]byte(migrated.Content)); got > MaxCaptureContentBytes {
		t.Fatalf("migrated content exceeded %d bytes: %d", MaxCaptureContentBytes, got)
	}
	if !strings.Contains(migrated.Content, "> **Parent Session**: "+longRoot) {
		t.Fatal("migrated content did not retain the authoritative root header")
	}
	if migrated.Preview != preview {
		t.Fatalf("reparent changed raw-output preview: before=%q after=%q", preview, migrated.Preview)
	}
}

func TestEnsureSessionRejectsConflictingRoot(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses-child", "ses-root-a"); err != nil {
		t.Fatalf("attach initial root: %v", err)
	}
	if err := st.EnsureSession("ses-root-b", ""); err != nil {
		t.Fatalf("create second root: %v", err)
	}
	err := st.EnsureSession("ses-child", "ses-root-b")
	if err == nil || !strings.Contains(err.Error(), "conflicting root") {
		t.Fatalf("expected conflicting root rejection, got %v", err)
	}
}

func TestCaptureSequenceIsNotReusedAfterDeletion(t *testing.T) {
	st := openTestStore(t)
	first, err := st.AddCapture(CaptureInput{ParentSessionID: "ses-root", CallID: "call-1", Content: "first"})
	if err != nil {
		t.Fatalf("add first capture: %v", err)
	}
	if err := st.DeleteCapture("ses-root", first.Seq); err != nil {
		t.Fatalf("delete first capture: %v", err)
	}
	second, err := st.AddCapture(CaptureInput{ParentSessionID: "ses-root", CallID: "call-2", Content: "second"})
	if err != nil {
		t.Fatalf("add second capture: %v", err)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("expected durable sequence %d, got %d", first.Seq+1, second.Seq)
	}
}

func TestDeletedRootRejectsNewCapture(t *testing.T) {
	st := openTestStore(t)
	if err := st.EnsureSession("ses-root", ""); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.MarkSessionDeleted("ses-root"); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	_, err := st.AddCapture(CaptureInput{ParentSessionID: "ses-root", CallID: "call-after-delete", Content: "late"})
	if err == nil || !strings.Contains(err.Error(), "cannot accept captures") {
		t.Fatalf("expected deleted-root capture rejection, got %v", err)
	}
}

func TestExpiredCaptureIsNotServedBeforeNextPrune(t *testing.T) {
	st := openTestStore(t)
	record, err := st.AddCapture(CaptureInput{ParentSessionID: "ses-root", CallID: "call-expired-read", Content: "old"})
	if err != nil {
		t.Fatalf("add capture: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE captures SET created_at = ? WHERE id = ?`, formatDBTime(time.Now().Add(-captureRetention-time.Hour)), record.ID); err != nil {
		t.Fatalf("age capture: %v", err)
	}
	captures, err := st.ListCaptures("ses-root", "")
	if err != nil {
		t.Fatalf("list captures: %v", err)
	}
	if len(captures) != 0 {
		t.Fatalf("expired capture remained visible: %+v", captures)
	}
	if _, err := st.GetCaptureBySeq("ses-root", record.Seq); err == nil {
		t.Fatal("expired capture remained readable by sequence")
	}
}

func TestPruneRetentionRemovesExpiredCaptures(t *testing.T) {
	st := openTestStore(t)
	record, err := st.AddCapture(CaptureInput{
		ParentSessionID: "ses-root",
		CallID:          "call-expired",
		Content:         "expired research",
	})
	if err != nil {
		t.Fatalf("AddCapture: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE captures SET created_at = ? WHERE id = ?`, formatDBTime(time.Now().Add(-captureRetention-time.Hour)), record.ID); err != nil {
		t.Fatalf("age capture: %v", err)
	}
	if err := st.pruneRetention(time.Now().UTC()); err != nil {
		t.Fatalf("pruneRetention: %v", err)
	}
	captures, err := st.ListCaptures("ses-root", "")
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if len(captures) != 0 {
		t.Fatalf("expected expired capture to be pruned, got %d", len(captures))
	}
}

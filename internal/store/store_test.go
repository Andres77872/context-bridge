package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	seedImportedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), seededCapture{
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
	seedImportedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 13, 0, 0, 0, time.UTC), seededCapture{
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
	seedImportedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-1",
		callID:         "call-1",
		agent:          "grep",
		description:    "Map the codebase",
		content:        "first output",
	})
	seedImportedCapture(t, st, "ses-root", 2, time.Date(2026, 3, 22, 8, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-2",
		callID:         "call-2",
		agent:          "explore",
		description:    "Verify architecture",
		content:        "second output",
	})

	hint, err := st.RenderHint("ses-child-2")
	if err != nil {
		t.Fatalf("RenderHint: %v", err)
	}

	for _, want := range []string{
		"## Prior Research Available — READ BEFORE WORKING",
		"There are 2 prior subagent outputs from this session:",
		"- [#1] [grep] Map the codebase",
		"- [#2] [explore] Verify architecture",
		"Use `read` with `session_id=\"ses-root\"`",
		"session_id=\"ses-root\"",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("expected hint to contain %q, got:\n%s", want, hint)
		}
	}
}

func TestListRootSessionsOrdersByLatestCapture(t *testing.T) {
	st := openTestStore(t)

	seedImportedCapture(t, st, "ses-old", 1, time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-old-child",
		callID:         "ses-old-call",
		agent:          "grep",
		description:    "older output",
		content:        "older output",
	})
	seedImportedCapture(t, st, "ses-new", 1, time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC), seededCapture{
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
	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedImportedCapture(t *testing.T, st *Store, sessionID string, seq int, capturedAt time.Time, capture seededCapture) {
	t.Helper()
	_, err := st.ImportCapture(sessionID, CaptureInput{
		ParentSessionID: sessionID,
		ChildSessionID:  capture.childSessionID,
		CallID:          capture.callID,
		Agent:           capture.agent,
		Description:     capture.description,
		Content:         capture.content,
		CapturedAt:      capturedAt,
	}, seq, "", capture.description, len(capture.content), false)
	if err != nil {
		t.Fatalf("seed imported capture: %v", err)
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

func TestMarkSessionDeleted(t *testing.T) {
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

	// Should still be returned but with DeletedAt set
	sessionsAfter, err := st.ListRootSessions(10)
	if err != nil {
		t.Fatalf("ListRootSessions after delete: %v", err)
	}
	if len(sessionsAfter) != 1 {
		t.Fatalf("expected session to still be returned, got %d", len(sessionsAfter))
	}
	if sessionsAfter[0].DeletedAt == nil {
		t.Fatal("expected DeletedAt to be set")
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

func TestImportCaptureValidationErrors(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	// Missing root ID
	_, err := st.ImportCapture("", CaptureInput{CallID: "1", Content: "c"}, 1, "", "", 0, false)
	if err == nil {
		t.Fatal("expected error for missing root ID")
	}

	// Invalid seq
	_, err = st.ImportCapture("ses", CaptureInput{CallID: "1", Content: "c"}, 0, "", "", 0, false)
	if err == nil {
		t.Fatal("expected error for invalid seq")
	}

	// Missing call ID
	_, err = st.ImportCapture("ses", CaptureInput{Content: "c"}, 1, "", "", 0, false)
	if err == nil {
		t.Fatal("expected error for missing call ID")
	}

	// Missing content
	_, err = st.ImportCapture("ses", CaptureInput{CallID: "1"}, 1, "", "", 0, false)
	if err == nil {
		t.Fatal("expected error for missing content")
	}

	// Valid insert
	record, err := st.ImportCapture("ses-1", CaptureInput{
		ParentSessionID: "ses-1",
		CallID:          "call-valid",
		Agent:           "grep",
		Description:     "desc",
		Content:         "valid content",
		CapturedAt:      now,
	}, 1, "/tmp/source", "prev", 100, true)

	if err != nil {
		t.Fatalf("unexpected error on valid import: %v", err)
	}
	if record.SourcePath != "/tmp/source" {
		t.Fatalf("expected source path /tmp/source, got %q", record.SourcePath)
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

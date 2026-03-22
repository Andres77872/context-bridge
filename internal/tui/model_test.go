package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

// runBatchCmd executes a tea.Cmd and, if it returns a tea.BatchMsg,
// executes each sub-command and returns all resulting messages.
// This lets tests using tea.Batch still inspect individual messages.
func runBatchCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, sub := range batch {
			if sub != nil {
				msgs = append(msgs, sub())
			}
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// findMsg returns the first message in msgs that is assignable to the type T.
func findMsg[T any](msgs []tea.Msg) (T, bool) {
	var zero T
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	return zero, false
}

// ---- Existing navigation tests (updated for tea.Batch) ----

func TestDashboardEnterLoadsSession(t *testing.T) {
	st := openTestStore(t)
	seedSession(t, st, "ses_root", []seedCapture{{seq: 1, agent: "grep", desc: "first capture", content: "hello world"}})

	m := New(st)
	m.activeTab = TabSessions
	m.dashboardSessions = []store.SessionSummary{{ID: "ses_root", CaptureCount: 1, LastCapturedAt: time.Now().UTC()}}
	m.dashboardCursor = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if !model.loading {
		t.Fatal("expected loading after opening session")
	}
	if cmd == nil {
		t.Fatal("expected load session command")
	}

	msgs := runBatchCmd(cmd)
	sessionMsg, ok := findMsg[sessionLoadedMsg](msgs)
	if !ok {
		t.Fatalf("expected sessionLoadedMsg among %d messages", len(msgs))
	}
	if sessionMsg.sessionID != "ses_root" {
		t.Fatalf("expected ses_root, got %s", sessionMsg.sessionID)
	}
	if len(sessionMsg.captures) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(sessionMsg.captures))
	}
}

func TestSearchEnterLoadsResults(t *testing.T) {
	st := openTestStore(t)
	seedSession(t, st, "ses_root", []seedCapture{{seq: 1, agent: "grep", desc: "find auth", content: "auth bug appears here"}})

	m := New(st)
	m.activeTab = TabSessions
	m.rightPanel = PanelSearch
	m.focus = FocusSearch
	m.searchScope = "ses_root"
	m.searchInput.SetValue("auth")
	m.searchInput.Focus() // Focus is required for handleSearchInputKeys to fire.

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if !model.loading {
		t.Fatal("expected loading after submitting search")
	}
	if cmd == nil {
		t.Fatal("expected search command")
	}

	msgs := runBatchCmd(cmd)
	searchMsg, ok := findMsg[searchLoadedMsg](msgs)
	if !ok {
		t.Fatalf("expected searchLoadedMsg among %d messages", len(msgs))
	}
	if searchMsg.query != "auth" {
		t.Fatalf("expected auth query, got %s", searchMsg.query)
	}
	if len(searchMsg.results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(searchMsg.results))
	}
}

func TestSearchRequiresSelectionFromDashboard(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusSessions

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model := updated.(Model)

	if model.focus != FocusSessions {
		t.Fatalf("expected to remain on dashboard, got %v", model.focus)
	}
	if model.statusMsg != "Select a session before searching" {
		t.Fatalf("unexpected status: %q", model.statusMsg)
	}
}

func TestEscapeFromCaptureReturnsPreviousScreen(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptureDetail
	m.prevPanel = PanelCaptures

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)

	if model.focus != FocusCaptures {
		t.Fatalf("expected session screen, got %v", model.focus)
	}
}

// ---- Phase 2: q/esc consistency and input blur ----

func TestQuitOnCaptureGoesBack(t *testing.T) {
	// BUG FIX: previously q on FocusCaptureDetail quit the app. Now it goes back.
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptureDetail
	m.prevPanel = PanelCaptures

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)

	if model.focus != FocusCaptures {
		t.Fatalf("expected to return to FocusCaptures on q, got %v", model.focus)
	}
	// Must NOT issue a tea.Quit command.
	if cmd != nil {
		msg := cmd()
		if msg == (tea.QuitMsg{}) {
			t.Fatal("q on FocusCaptureDetail must not quit the application")
		}
	}
}

func TestQuitOnDashboardQuitsApp(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusSessions

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit command from dashboard q")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestSearchInputBlurredOnNavAway(t *testing.T) {
	// After pressing esc on FocusSearch, searchInput must be blurred if empty.
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusSearch
	m.searchOrigin = PanelCaptures
	m.searchInput.SetValue("")
	m.searchInput.Focus()

	if !m.searchInput.Focused() {
		t.Fatal("searchInput should start focused for this test")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)

	if model.searchInput.Focused() {
		t.Fatal("searchInput should be blurred after navigating away from FocusSearch")
	}
	if model.focus != FocusCaptures {
		t.Fatalf("expected FocusCaptures, got %v", model.focus)
	}
}

func TestQOnSessionGoesBackToDashboard(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.selectedSession = "ses_abc"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)

	if model.focus != FocusSessions {
		t.Fatalf("expected FocusSessions on q from session, got %v", model.focus)
	}
}

func TestEscOnSearchResultsGoesBackToOrigin(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusSearchResults
	m.searchOrigin = PanelCaptures

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)

	if model.focus != FocusCaptures {
		t.Fatalf("expected FocusCaptures, got %v", model.focus)
	}
}

// ---- Phase 5: inline filter ----

func TestFilterActivatesOnSlashKey(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{
		{Seq: 1, Agent: "grep", Description: "first"},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model := updated.(Model)

	if !model.filterActive {
		t.Fatal("expected filterActive=true after pressing / on FocusCaptures")
	}
	if !model.filterInput.Focused() {
		t.Fatal("expected filterInput to be focused after activating filter")
	}
}

func TestFilterEscClearsFilter(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{
		{Seq: 1, Agent: "grep"},
		{Seq: 2, Agent: "explore"},
	}
	m.activateFilter()
	m.filterInput.SetValue("grep")
	m.filterQuery = "grep"

	// Press esc while filter is active.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)

	if model.filterActive {
		t.Fatal("expected filterActive=false after pressing esc")
	}
	if model.filterQuery != "" {
		t.Fatalf("expected empty filterQuery, got %q", model.filterQuery)
	}
	if model.filterInput.Focused() {
		t.Fatal("expected filterInput blurred after deactivating filter")
	}
}

func TestFilterClientSide(t *testing.T) {
	m := New(nil)
	m.sessionCaptures = []store.CaptureRecord{
		{Seq: 1, Agent: "grep", Description: "search files"},
		{Seq: 2, Agent: "explore", Description: "explore code"},
		{Seq: 3, Agent: "grep", Description: "deep search"},
	}

	// No filter active — all captures visible.
	all := m.filteredCaptures()
	if len(all) != 3 {
		t.Fatalf("expected 3 captures with no filter, got %d", len(all))
	}

	// Filter by "grep".
	m.filterActive = true
	m.filterQuery = "grep"
	grepOnly := m.filteredCaptures()
	if len(grepOnly) != 2 {
		t.Fatalf("expected 2 grep captures, got %d", len(grepOnly))
	}
	for _, c := range grepOnly {
		if c.Agent != "grep" {
			t.Fatalf("expected only grep captures, got agent=%q", c.Agent)
		}
	}

	// Filter by description text.
	m.filterQuery = "deep"
	deepOnly := m.filteredCaptures()
	if len(deepOnly) != 1 {
		t.Fatalf("expected 1 capture with 'deep', got %d", len(deepOnly))
	}
	if deepOnly[0].Seq != 3 {
		t.Fatalf("expected seq=3, got %d", deepOnly[0].Seq)
	}
}

// ---- Phase 7: confirm layer blocks navigation ----

func TestConfirmGatesKeyRouting(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{{Seq: 1, Agent: "grep"}}
	m.confirmActive = true
	m.confirmMsg = "Are you sure?"

	// Down arrow should NOT move cursor while confirm is active.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	model := updated.(Model)
	if model.sessionCursor != 0 {
		t.Fatalf("confirm dialog should block navigation, but cursor moved to %d", model.sessionCursor)
	}
	// Confirm should still be active (we didn't press y/n/esc).
	if !model.confirmActive {
		t.Fatal("confirmActive should remain true after non-confirm key")
	}
}

func TestConfirmNClosesDialog(t *testing.T) {
	m := New(nil)
	m.focus = FocusCaptures
	m.confirmActive = true
	m.confirmMsg = "Delete this session?"
	m.confirmAction = confirmNone

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model := updated.(Model)

	if model.confirmActive {
		t.Fatal("expected confirmActive=false after pressing n")
	}
}

func TestConfirmEscClosesDialog(t *testing.T) {
	m := New(nil)
	m.confirmActive = true
	m.confirmMsg = "Are you sure?"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)

	if model.confirmActive {
		t.Fatal("expected confirmActive=false after pressing esc on confirm dialog")
	}
}

// ---- Stats loading ----

func TestStatsLoadedUpdatesModel(t *testing.T) {
	m := New(nil)
	stats := store.StoreStats{Sessions: 5, Captures: 42, TotalBytes: 1024}

	updated, _ := m.Update(statsLoadedMsg{stats: stats})
	model := updated.(Model)

	if model.stats.Sessions != 5 {
		t.Fatalf("expected 5 sessions, got %d", model.stats.Sessions)
	}
	if model.stats.Captures != 42 {
		t.Fatalf("expected 42 captures, got %d", model.stats.Captures)
	}
}

// ---- Scroll state ----

func TestDashboardScrollFollowsCursor(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.height = 10 // visibleCount = 10 - 6 = 4

	sessions := make([]store.SessionSummary, 10)
	for i := range sessions {
		sessions[i] = store.SessionSummary{ID: fmt.Sprintf("ses_%02d", i), CreatedAt: time.Now()}
	}
	m.dashboardSessions = sessions
	m.dashboardCursor = 0
	m.dashboardScroll = 0

	// Navigate down past the visible window (4 visible rows).
	for i := 0; i < 5; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = updated.(Model)
	}

	if m.dashboardCursor != 5 {
		t.Fatalf("expected cursor=5, got %d", m.dashboardCursor)
	}
	// Scroll must have advanced to keep cursor visible.
	if m.dashboardScroll == 0 {
		t.Fatal("expected dashboardScroll > 0 after cursor moved past visible window")
	}
}

// ---- Helpers ----

type seedCapture struct {
	seq     int
	agent   string
	desc    string
	content string
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "context-bridge.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedSession(t *testing.T, st *store.Store, sessionID string, captures []seedCapture) {
	t.Helper()
	for _, capture := range captures {
		_, err := st.ImportCapture(sessionID, store.CaptureInput{
			ParentSessionID: sessionID,
			ChildSessionID:  fmt.Sprintf("child-%d", capture.seq),
			CallID:          fmt.Sprintf("call-%d", capture.seq),
			Agent:           capture.agent,
			Description:     capture.desc,
			Content:         capture.content,
			CapturedAt:      time.Now().UTC().Add(time.Duration(capture.seq) * time.Minute),
		}, capture.seq, "", capture.desc, len(capture.content), false)
		if err != nil {
			t.Fatalf("seed capture %d: %v", capture.seq, err)
		}
	}
}

// ---- View-level contracts (view_test.go equivalent, in same package) ----

func withDimensions(m Model, w, h int) Model {
	m.width = w
	m.height = h
	m.ready = true
	m.syncComponentSize()
	return m
}

func TestDashboardEmptyStateHasHelpText(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	output := m.viewOverviewTab(100, 20)
	if !strings.Contains(output, "Run `context-bridge serve`") {
		t.Fatalf("empty dashboard must mention 'Run `context-bridge serve`', got:\n%s", output)
	}
}

func TestDashboardShowsStatsCard(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.stats = store.StoreStats{Sessions: 3, Captures: 12, TotalBytes: 2048}
	m.dashboardSessions = []store.SessionSummary{{ID: "ses_abc", CaptureCount: 12, CreatedAt: time.Now()}}
	output := m.viewOverviewTab(100, 20)
	if !strings.Contains(output, "3") {
		t.Fatalf("stats card must show session count, got:\n%s", output)
	}
	if !strings.Contains(output, "12") {
		t.Fatalf("stats card must show capture count, got:\n%s", output)
	}
}

func TestSessionFilterBarVisibleWhenActive(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{{Seq: 1, Agent: "grep", Description: "test"}}
	m.activateFilter()
	output := m.viewSession(100, 20)
	if !strings.Contains(output, "filter") && !strings.Contains(output, "/ ") {
		t.Fatalf("filter bar should be visible when filter active, got:\n%s", output)
	}
}

func TestSessionFilterBarPersistent(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{{Seq: 1, Agent: "grep", Description: "test"}}
	output := m.viewSession(100, 20)
	// In normal mode, it should show the dimmed filter prompt.
	if !strings.Contains(output, "type to filter list") {
		t.Fatalf("normal session help should mention filter prompt, got:\n%s", output)
	}
	if !strings.Contains(output, "s full-text search") {
		t.Fatalf("normal session help should say 's full-text search', got:\n%s", output)
	}
}

func TestScrollIndicatorVisibleWhenOverflow(t *testing.T) {
	m := withDimensions(New(nil), 120, 10) // height=10 → visibleCount=2
	sessions := make([]store.SessionSummary, 20)
	for i := range sessions {
		sessions[i] = store.SessionSummary{ID: fmt.Sprintf("ses_%02d", i), CreatedAt: time.Now()}
	}
	m.dashboardSessions = sessions
	output := m.viewSessionsPane(30, 20)
	if !strings.Contains(output, "/20") {
		t.Fatalf("scroll indicator must appear when items > visible window, got:\n%s", output)
	}
}

func TestCaptureScreenShowsAgentBadge(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.focus = FocusCaptureDetail
	m.selectedCapture = &store.CaptureRecord{
		Seq:         3,
		Agent:       "grep",
		SessionID:   "ses_abc",
		Description: "Deep analysis",
		Content:     "some content",
		CapturedAt:  time.Now(),
		Bytes:       100,
	}
	output := m.viewCapture(100, 20)
	if !strings.Contains(output, "grep") {
		t.Fatalf("capture screen must show agent badge, got:\n%s", output)
	}
}

func TestHelpTextChangesWhenFiltered(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.focus = FocusCaptures
	m.sessionCaptures = []store.CaptureRecord{{Seq: 1, Agent: "grep"}}

	normalOutput := m.viewSession(100, 20)
	m.activateFilter()
	filteredOutput := m.viewSession(100, 20)

	if normalOutput == filteredOutput {
		t.Fatal("help text should change when filter is active")
	}
	if !strings.Contains(filteredOutput, "esc clear filter") {
		t.Fatalf("filter help must say 'esc clear filter', got:\n%s", filteredOutput)
	}
}

func TestStatusBarShowsErrorWhenSet(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.errorMsg = "database connection failed"
	output := m.renderStatusBar()
	if !strings.Contains(output, "database connection failed") {
		t.Fatalf("status bar must show error message, got:\n%s", output)
	}
}

func TestConfirmDialogAppearsWhenActive(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.confirmActive = true
	m.confirmMsg = "Delete everything?"
	output := m.View()
	if !strings.Contains(output, "Delete everything?") {
		t.Fatalf("confirm dialog must appear in View() when active, got:\n%s", output)
	}
	if !strings.Contains(output, "[y] Confirm") {
		t.Fatalf("confirm dialog must show [y] Confirm prompt, got:\n%s", output)
	}
}

func TestSearchResultsEmptyStateShowsQuery(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.focus = FocusSearchResults
	m.searchQuery = "somethingobscure"
	m.searchResults = nil
	output := m.viewSearchResults(100, 20)
	if !strings.Contains(output, "somethingobscure") {
		t.Fatalf("empty search results must show the query, got:\n%s", output)
	}
}

func TestViewSearchTabEmptyState(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.activeTab = TabSearch
	m.selectedSession = ""
	m.dashboardSessions = []store.SessionSummary{{ID: "dummy"}}

	output := m.viewSearchTab(120, 30)
	if !strings.Contains(output, "Select a session from the left menu to start searching") {
		t.Fatalf("expected empty search state instructions, got:\n%s", output)
	}
}

func TestViewSearchShowsSearchInput(t *testing.T) {
	m := withDimensions(New(nil), 120, 30)
	m.activeTab = TabSearch
	m.selectedSession = "ses_123"
	m.rightPanel = PanelSearch
	m.searchInput.SetValue("test search query")

	output := m.viewSearch(100, 20)
	if !strings.Contains(output, "test search query") {
		t.Fatalf("expected search view to show input value, got:\n%s", output)
	}
	if !strings.Contains(output, "Full-Text Search") {
		t.Fatalf("expected search view to show header, got:\n%s", output)
	}
}

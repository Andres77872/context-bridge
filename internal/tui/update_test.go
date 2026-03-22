package tui

import (
	"errors"
	"testing"

	"context-bridge/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWindowSizeMsgSyncsComponents(t *testing.T) {
	m := New(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	model := updated.(Model)

	if model.width != 100 || model.height != 50 {
		t.Fatalf("expected width 100 and height 50, got %d, %d", model.width, model.height)
	}
	if !model.ready {
		t.Fatal("expected model to be ready after window size msg")
	}
}

func TestTabAndNumericKeysSwitchTabs(t *testing.T) {
	m := New(nil)

	// Test Tab
	m.activeTab = TabOverview
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if updated.(Model).activeTab != TabSessions {
		t.Fatalf("expected TabSessions, got %v", updated.(Model).activeTab)
	}

	// Test 1, 2, 3
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if updated.(Model).activeTab != TabSearch {
		t.Fatalf("expected TabSearch, got %v", updated.(Model).activeTab)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if updated.(Model).activeTab != TabOverview {
		t.Fatalf("expected TabOverview, got %v", updated.(Model).activeTab)
	}
}

func TestPluginInstalledMsgUpdatesStatus(t *testing.T) {
	m := New(nil)

	// Success
	updated, _ := m.Update(pluginInstalledMsg{path: "/tmp/plugin"})
	model := updated.(Model)
	if model.errorMsg != "" {
		t.Fatalf("expected no error, got %s", model.errorMsg)
	}
	if model.statusMsg == "" {
		t.Fatal("expected status message")
	}

	// Error
	errTest := errors.New("install failed")
	updated, _ = m.Update(pluginInstalledMsg{err: errTest})
	model = updated.(Model)
	if model.errorMsg != "install failed" {
		t.Fatalf("expected error message, got %s", model.errorMsg)
	}
}

func TestSearchEnterEmptyQuerySetsError(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSearch
	m.focus = FocusSearch
	m.searchInput.Focus()
	m.searchInput.SetValue("   ") // Whitespace only

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)

	if model.searchErr != "Query is required" {
		t.Fatalf("expected 'Query is required', got %q", model.searchErr)
	}
}

func TestSKeyFromSearchResultsReturnsToSearchInput(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSearch
	m.focus = FocusSearchResults
	m.searchQuery = "prev query"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model := updated.(Model)

	if model.focus != FocusSearch {
		t.Fatalf("expected focus FocusSearch, got %v", model.focus)
	}
	if !model.searchInput.Focused() {
		t.Fatal("expected searchInput to be focused")
	}
	if model.searchInput.Value() != "prev query" {
		t.Fatalf("expected searchInput to retain previous query, got %q", model.searchInput.Value())
	}
}

func TestLoadedMessagesErrorHandling(t *testing.T) {
	m := New(nil)
	testErr := errors.New("test error")

	// Dashboard
	m1, _ := m.Update(dashboardLoadedMsg{err: testErr})
	if m1.(Model).errorMsg != testErr.Error() {
		t.Fatalf("expected dashboard error %v", testErr)
	}

	// Session
	m2, _ := m.Update(sessionLoadedMsg{err: testErr})
	if m2.(Model).errorMsg != testErr.Error() {
		t.Fatalf("expected session error %v", testErr)
	}

	// Capture
	m3, _ := m.Update(captureLoadedMsg{err: testErr})
	if m3.(Model).errorMsg != testErr.Error() {
		t.Fatalf("expected capture error %v", testErr)
	}

	// Search
	m4, _ := m.Update(searchLoadedMsg{err: testErr})
	if m4.(Model).errorMsg != testErr.Error() {
		t.Fatalf("expected search error %v", testErr)
	}
}

func TestFilterEnterBlursInput(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.activateFilter()

	if !m.filterInput.Focused() {
		t.Fatal("expected filterInput to be focused")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)

	if model.filterInput.Focused() {
		t.Fatal("expected filterInput to be blurred after Enter")
	}
}

func TestMouseWheelInSessionList(t *testing.T) {
	m := New(nil)
	m.activeTab = TabSessions
	m.focus = FocusCaptures
	m.height = 20
	// Make sure we have more captures than the visible window
	m.sessionCaptures = make([]store.CaptureRecord, 50)

	// Scroll down
	updated, _ := m.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	model := updated.(Model)
	if model.sessionCursor != 1 {
		t.Fatalf("expected cursor 1 after wheel down, got %d", model.sessionCursor)
	}

	// Scroll up
	updated, _ = model.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	model = updated.(Model)
	if model.sessionCursor != 0 {
		t.Fatalf("expected cursor 0 after wheel up, got %d", model.sessionCursor)
	}
}

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncComponentSize()
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case dashboardLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.setError(msg.err)
			return m, nil
		}
		m.dashboardSessions = msg.sessions
		m.dashboardCursor = clampCursor(m.dashboardCursor, len(m.dashboardSessions))
		m.setStatus(fmt.Sprintf("Loaded %d sessions", len(msg.sessions)))
		return m, nil

	case statsLoadedMsg:
		if msg.err == nil {
			m.stats = msg.stats
		}
		return m, nil

	case sessionLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.setError(msg.err)
			return m, nil
		}
		m.selectedSession = msg.sessionID
		m.sessionCaptures = msg.captures
		m.sessionCursor = 0
		m.sessionScroll = 0
		m.deactivateFilter()
		m.screen = ScreenSession
		m.setStatus(fmt.Sprintf("Loaded %d outputs for %s", len(msg.captures), truncateID(msg.sessionID, 20)))
		return m, nil

	case captureLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.setError(msg.err)
			return m, nil
		}
		m.selectedCapture = msg.record
		m.screen = ScreenCapture
		m.syncComponentSize()
		m.contentViewport.GotoTop()
		if msg.record != nil {
			m.setStatus(fmt.Sprintf("Viewing output #%d", msg.record.Seq))
		}
		return m, nil

	case searchLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.searchErr = msg.err.Error()
			m.setError(msg.err)
			return m, nil
		}
		m.searchScope = msg.sessionID
		m.searchQuery = msg.query
		m.searchResults = msg.results
		m.searchCursor = 0
		m.searchResultsScroll = 0
		m.screen = ScreenSearchResults
		m.searchErr = ""
		m.setStatus(fmt.Sprintf("Found %d result(s) for %q", len(msg.results), msg.query))
		return m, nil
	}

	// Key routing with explicit precedence chain (follows Engram's model):
	//   P0 — ctrl+c: global quit, always wins
	//   P1 — confirm active: modal intercepts all keys
	//   P2 — search input focused: textinput owns the stream
	//   P3 — filter input focused: textinput owns the stream
	//   P4 — viewport scrolling on capture screen
	//   P5 — normal per-screen routing
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		// Non-key messages: forward viewport/input updates even for non-key msgs.
		var cmd tea.Cmd
		if m.screen == ScreenCapture {
			m.contentViewport, cmd = m.contentViewport.Update(msg)
		}
		return m, cmd
	}

	// P0 — global quit.
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	// P1 — confirm dialog intercepts all keys.
	if m.confirmActive {
		return m.handleConfirmKeys(key)
	}

	// P2 — search input focused.
	if m.screen == ScreenSearch && m.searchInput.Focused() {
		return m.handleSearchInputKeys(key)
	}

	// P3 — filter input focused.
	if m.filterActive && m.filterInput.Focused() {
		return m.handleFilterInputKeys(key)
	}

	// P4/P5 — screen routing.
	switch m.screen {
	case ScreenDashboard:
		return m.updateDashboardKeys(key)
	case ScreenSession:
		return m.updateSessionKeys(key)
	case ScreenCapture:
		return m.updateCaptureKeys(key)
	case ScreenSearch:
		return m.updateSearchKeys(key)
	case ScreenSearchResults:
		return m.updateSearchResultsKeys(key)
	default:
		return m, nil
	}
}

// --- Dashboard ---

func (m Model) updateDashboardKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.dashboardSessions)
	visibleCount := m.height - 6
	if visibleCount < 3 {
		visibleCount = 3
	}

	switch key.String() {
	case "q":
		return m, tea.Quit
	case "down", "j":
		m.dashboardCursor = moveCursor(m.dashboardCursor, 1, total)
		m.dashboardScroll = adjustScroll(m.dashboardCursor, m.dashboardScroll, visibleCount)
	case "up", "k":
		m.dashboardCursor = moveCursor(m.dashboardCursor, -1, total)
		m.dashboardScroll = adjustScroll(m.dashboardCursor, m.dashboardScroll, visibleCount)
	case "enter":
		selected := m.selectedDashboardSession()
		if selected == nil {
			m.setStatus("No session selected")
			return m, nil
		}
		m.loading = true
		m.prevScreen = ScreenDashboard
		m.selectedSession = selected.ID
		return m, tea.Batch(loadSessionCmd(m.store, selected.ID), m.spinner.Tick)
	case "/":
		return m.openSearchFromCurrentSelection()
	}
	return m, nil
}

// --- Session ---

func (m Model) updateSessionKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.filteredCaptures()
	total := len(visible)
	visibleCount := m.height - 7
	if m.filterActive {
		visibleCount--
	}
	if visibleCount < 3 {
		visibleCount = 3
	}

	switch key.String() {
	case "q", "esc":
		// Both q and esc go back on child screens.
		m.screen = ScreenDashboard
		m.deactivateFilter()
		return m, nil
	case "down", "j":
		m.sessionCursor = moveCursor(m.sessionCursor, 1, total)
		m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
	case "up", "k":
		m.sessionCursor = moveCursor(m.sessionCursor, -1, total)
		m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
	case "enter":
		selected := m.selectedSessionCaptureFromFiltered(visible)
		if selected == nil {
			m.setStatus("No output selected")
			return m, nil
		}
		m.loading = true
		m.prevScreen = ScreenSession
		return m, tea.Batch(loadCaptureCmd(m.store, m.selectedSession, selected.Seq), m.spinner.Tick)
	case "/":
		return m.openSearchFromCurrentSelection()
	case "f":
		m.activateFilter()
	}
	return m, nil
}

// --- Capture ---

func (m Model) updateCaptureKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Forward scrolling keys to the viewport.
	var vpCmd tea.Cmd
	m.contentViewport, vpCmd = m.contentViewport.Update(key)

	switch key.String() {
	case "q", "esc":
		// BUG FIX: previously q on ScreenCapture called tea.Quit.
		// Now both q and esc return to prevScreen, consistent with all other child screens.
		m.screen = m.prevScreen
		return m, nil
	case "/":
		return m.openSearchFromCurrentSelection()
	case "home":
		m.contentViewport.GotoTop()
		return m, nil
	case "end":
		m.contentViewport.GotoBottom()
		return m, nil
	}
	return m, vpCmd
}

// --- Search input keys (P2 handler — textinput owns the stream) ---

func (m Model) handleSearchInputKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = m.searchOrigin
		m.searchErr = ""
		m.searchInput.Blur()
		return m, nil
	case "enter":
		query := strings.TrimSpace(m.searchInput.Value())
		if query == "" {
			m.searchErr = "Query is required"
			return m, nil
		}
		m.loading = true
		m.searchErr = ""
		m.searchQuery = query
		m.searchInput.Blur()
		return m, tea.Batch(loadSearchCmd(m.store, m.searchScope, query), m.spinner.Tick)
	}
	// All other keys: let the textinput consume them.
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(key)
	return m, cmd
}

// updateSearchKeys handles the ScreenSearch screen when input is NOT focused
// (e.g. user navigated back to this screen without refocusing).
func (m Model) updateSearchKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.screen = m.searchOrigin
		m.searchErr = ""
		m.searchInput.Blur()
		return m, nil
	default:
		// Re-focus and forward the key.
		m.searchInput.Focus()
		return m.handleSearchInputKeys(key)
	}
}

// --- Filter input keys (P3 handler — textinput owns the stream) ---

func (m Model) handleFilterInputKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.deactivateFilter()
		return m, nil
	case "enter":
		// Confirm filter — keep results but blur input so navigation keys work.
		m.filterInput.Blur()
		return m, nil
	}
	// All other keys: update textinput and run client-side filter.
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(key)
	m.filterQuery = m.filterInput.Value()
	m.sessionCursor = 0
	m.sessionScroll = 0
	return m, cmd
}

// --- Search Results ---

func (m Model) updateSearchResultsKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.searchResults)
	visibleCount := m.height - 8
	if visibleCount < 3 {
		visibleCount = 3
	}

	switch key.String() {
	case "q", "esc":
		m.screen = m.searchOrigin
		return m, nil
	case "down", "j":
		m.searchCursor = moveCursor(m.searchCursor, 1, total)
		m.searchResultsScroll = adjustScroll(m.searchCursor, m.searchResultsScroll, visibleCount)
	case "up", "k":
		m.searchCursor = moveCursor(m.searchCursor, -1, total)
		m.searchResultsScroll = adjustScroll(m.searchCursor, m.searchResultsScroll, visibleCount)
	case "enter":
		selected := m.selectedSearchResult()
		if selected == nil {
			m.setStatus("No result selected")
			return m, nil
		}
		m.loading = true
		m.prevScreen = ScreenSearchResults
		m.selectedSession = selected.Capture.SessionID
		return m, tea.Batch(loadCaptureCmd(m.store, selected.Capture.SessionID, selected.Capture.Seq), m.spinner.Tick)
	case "/":
		m.screen = ScreenSearch
		m.searchOrigin = ScreenSearchResults
		m.searchInput.Focus()
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.CursorEnd()
	}
	return m, nil
}

// --- Confirm layer ---

func (m Model) handleConfirmKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y":
		return m.executeConfirmAction()
	case "n", "esc":
		m.confirmActive = false
		m.confirmMsg = ""
		m.confirmAction = confirmNone
	}
	return m, nil
}

func (m Model) executeConfirmAction() (tea.Model, tea.Cmd) {
	// No destructive actions defined yet — infrastructure only.
	m.confirmActive = false
	m.confirmMsg = ""
	m.confirmAction = confirmNone
	return m, nil
}

// --- Search open helper ---

func (m Model) openSearchFromCurrentSelection() (tea.Model, tea.Cmd) {
	scope := m.selectedSession
	origin := m.screen

	if scope == "" {
		if selected := m.selectedDashboardSession(); selected != nil {
			scope = selected.ID
			origin = ScreenDashboard
		}
	}

	if scope == "" {
		m.setStatus("Select a session before searching")
		return m, nil
	}

	m.searchScope = scope
	m.searchOrigin = origin
	m.screen = ScreenSearch
	m.searchErr = ""
	m.searchInput.Focus()
	if strings.TrimSpace(m.searchQuery) == "" {
		m.searchInput.SetValue("")
	} else {
		m.searchInput.SetValue(m.searchQuery)
	}
	m.searchInput.CursorEnd()
	return m, nil
}

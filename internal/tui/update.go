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
		m.rightPanel = PanelCaptures
		m.focus = FocusCaptures
		m.setStatus(fmt.Sprintf("Loaded %d outputs for %s", len(msg.captures), truncateID(msg.sessionID, 20)))
		return m, nil

	case captureLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.setError(msg.err)
			return m, nil
		}
		m.selectedCapture = msg.record
		m.rightPanel = PanelCaptureDetail
		m.focus = FocusCaptureDetail
		m.syncComponentSize()
		m.contentViewport.GotoTop()
		if msg.record != nil {
			m.setStatus(fmt.Sprintf("Viewing output #%d", msg.record.Seq))
		}
		return m, nil

	case pluginInstalledMsg:
		if msg.err != nil {
			m.setError(msg.err)
		} else {
			m.setStatus(fmt.Sprintf("Plugin installed successfully to %s", msg.path))
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

		var rawOutput string
		if len(msg.results) == 0 {
			rawOutput = fmt.Sprintf("No matches for %q across %d outputs.", msg.query, msg.totalCaptures)
		} else {
			var groups []string
			totalMatches := 0
			for _, result := range msg.results {
				totalMatches += result.MatchCount
				groups = append(groups, strings.Join([]string{
					fmt.Sprintf("### #%d [%s] %s", result.Capture.Seq, result.Capture.Agent, result.Capture.Description),
					fmt.Sprintf("%d match(es)", result.MatchCount),
					"",
					result.Snippet,
				}, "\n"))
			}
			rawOutput = strings.Join([]string{
				fmt.Sprintf("## Search: %q", msg.query),
				"",
				fmt.Sprintf("%d match(es) across %d outputs.", totalMatches, len(msg.results)),
				fmt.Sprintf("Use `context_bridge_read` with `session_id=%q` and the output # to read full content.", msg.rootID),
				"",
				strings.Join(groups, "\n\n"),
			}, "\n")
		}

		m.searchRawOutput = rawOutput
		m.searchCursor = 0
		m.searchResultsScroll = 0
		m.rightPanel = PanelSearchResults
		m.focus = FocusSearchResults
		m.searchErr = ""
		m.setStatus(fmt.Sprintf("Found %d result(s) for %q", len(msg.results), msg.query))
		m.syncComponentSize()
		m.contentViewport.GotoTop()
		return m, nil

	case tea.MouseMsg:
		if m.activeTab == TabOverview {
			return m, nil
		}
		if m.focus == FocusCaptureDetail {
			var cmd tea.Cmd
			m.contentViewport, cmd = m.contentViewport.Update(msg)
			return m, cmd
		} else if m.focus == FocusSessions {
			total := len(m.dashboardSessions)
			visibleCount := (m.height - 10) - 2 // innerH - headerLines
			if visibleCount < 3 {
				visibleCount = 3
			}
			if msg.Type == tea.MouseWheelUp {
				m.dashboardCursor = moveCursor(m.dashboardCursor, -1, total)
				m.dashboardScroll = adjustScroll(m.dashboardCursor, m.dashboardScroll, visibleCount)
			} else if msg.Type == tea.MouseWheelDown {
				m.dashboardCursor = moveCursor(m.dashboardCursor, 1, total)
				m.dashboardScroll = adjustScroll(m.dashboardCursor, m.dashboardScroll, visibleCount)
			}
			return m, nil
		} else if m.focus == FocusCaptures {
			visible := m.filteredCaptures()
			total := len(visible)
			visibleCount := (m.height - 10) - 8
			if visibleCount < 3 {
				visibleCount = 3
			}
			if msg.Type == tea.MouseWheelUp {
				m.sessionCursor = moveCursor(m.sessionCursor, -1, total)
				m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
			} else if msg.Type == tea.MouseWheelDown {
				m.sessionCursor = moveCursor(m.sessionCursor, 1, total)
				m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
			}
			return m, nil
		} else if m.focus == FocusSearchResults {
			var cmd tea.Cmd
			m.contentViewport, cmd = m.contentViewport.Update(msg)
			return m, cmd
		}
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		if (m.focus == FocusCaptureDetail || m.focus == FocusSearchResults) && (m.activeTab == TabSessions || m.activeTab == TabSearch) {
			m.contentViewport, cmd = m.contentViewport.Update(msg)
		}
		return m, cmd
	}

	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if m.confirmActive {
		return m.handleConfirmKeys(key)
	}

	if m.focus == FocusSearch && m.searchInput.Focused() && (m.activeTab == TabSessions || m.activeTab == TabSearch) {
		return m.handleSearchInputKeys(key)
	}

	if m.filterActive && m.filterInput.Focused() && (m.activeTab == TabSessions || m.activeTab == TabSearch) {
		return m.handleFilterInputKeys(key)
	}

	if key.String() == "tab" {
		if m.activeTab == TabOverview {
			m.activeTab = TabSessions
		} else if m.activeTab == TabSessions {
			m.activeTab = TabSearch
		} else {
			m.activeTab = TabOverview
		}
		return m, nil
	}
	if key.String() == "1" {
		m.activeTab = TabOverview
		return m, nil
	}
	if key.String() == "2" {
		m.activeTab = TabSessions
		return m, nil
	}
	if key.String() == "3" {
		m.activeTab = TabSearch
		return m, nil
	}

	if m.activeTab == TabSessions || m.activeTab == TabSearch {
		switch m.focus {
		case FocusSessions:
			return m.updateDashboardKeys(key)
		case FocusCaptures:
			return m.updateSessionKeys(key)
		case FocusCaptureDetail:
			return m.updateCaptureKeys(key)
		case FocusSearch:
			return m.updateSearchKeys(key)
		case FocusSearchResults:
			return m.updateSearchResultsKeys(key)
		}
	} else {
		switch key.String() {
		case "q":
			return m, tea.Quit
		case "i":
			return m, installPluginCmd()
		}
	}

	return m, nil
}

func (m Model) focusFromRightPanel() FocusPane {
	switch m.rightPanel {
	case PanelCaptures:
		return FocusCaptures
	case PanelCaptureDetail:
		return FocusCaptureDetail
	case PanelSearch:
		return FocusSearch
	case PanelSearchResults:
		return FocusSearchResults
	default:
		return FocusCaptures
	}
}

// --- Dashboard ---

func (m Model) updateDashboardKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.dashboardSessions)
	visibleCount := (m.height - 10) - 2
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
	case "enter", "right", "l":
		selected := m.selectedDashboardSession()
		if selected == nil {
			m.setStatus("No session selected")
			return m, nil
		}
		if selected.ID == m.selectedSession && key.String() != "enter" {
			m.focus = m.focusFromRightPanel()
			return m, nil
		}

		if m.activeTab == TabSearch {
			m.selectedSession = selected.ID
			return m.openSearchFromCurrentSelection()
		}

		m.loading = true
		m.prevPanel = PanelCaptures
		m.selectedSession = selected.ID
		return m, tea.Batch(loadSessionCmd(m.store, selected.ID), m.spinner.Tick)
	case "s":
		return m.openSearchFromCurrentSelection()
	}
	return m, nil
}

// --- Session ---

func (m Model) updateSessionKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.filteredCaptures()
	total := len(visible)
	visibleCount := (m.height - 10) - 8
	if visibleCount < 3 {
		visibleCount = 3
	}

	switch key.String() {
	case "q", "esc", "left", "h":
		if m.filterQuery != "" {
			m.filterQuery = ""
			m.sessionCursor = 0
			m.sessionScroll = 0
			return m, nil
		}
		m.focus = FocusSessions
		m.deactivateFilter()
		return m, nil
	case "down", "j":
		m.sessionCursor = moveCursor(m.sessionCursor, 1, total)
		m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
	case "up", "k":
		m.sessionCursor = moveCursor(m.sessionCursor, -1, total)
		m.sessionScroll = adjustScroll(m.sessionCursor, m.sessionScroll, visibleCount)
	case "enter", "right", "l":
		selected := m.selectedSessionCaptureFromFiltered(visible)
		if selected == nil {
			m.setStatus("No output selected")
			return m, nil
		}
		m.loading = true
		m.prevPanel = PanelCaptures
		return m, tea.Batch(loadCaptureCmd(m.store, m.selectedSession, selected.Seq), m.spinner.Tick)
	case "/":
		m.activateFilter()
		return m, nil
	case "s":
		return m.openSearchFromCurrentSelection()
	}
	return m, nil
}

// --- Capture ---

func (m Model) updateCaptureKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var vpCmd tea.Cmd
	m.contentViewport, vpCmd = m.contentViewport.Update(key)

	switch key.String() {
	case "q", "esc", "left", "h":
		m.rightPanel = m.prevPanel
		m.focus = m.focusFromRightPanel()
		return m, nil
	case "s":
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

// --- Search input keys ---

func (m Model) handleSearchInputKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		if m.searchInput.Value() != "" {
			m.searchInput.SetValue("")
			return m, nil
		}
		if m.activeTab == TabSearch {
			m.focus = FocusSessions
		} else {
			m.rightPanel = m.searchOrigin
			m.focus = m.focusFromRightPanel()
		}
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
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(key)
	return m, cmd
}

func (m Model) updateSearchKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		if m.activeTab == TabSearch {
			m.focus = FocusSessions
		} else {
			m.rightPanel = m.searchOrigin
			m.focus = m.focusFromRightPanel()
		}
		m.searchErr = ""
		m.searchInput.Blur()
		return m, nil
	default:
		m.searchInput.Focus()
		return m.handleSearchInputKeys(key)
	}
}

// --- Filter input keys ---

func (m Model) handleFilterInputKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.deactivateFilter()
		return m, nil
	case "enter":
		m.filterInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(key)
	m.filterQuery = m.filterInput.Value()
	m.sessionCursor = 0
	m.sessionScroll = 0
	return m, cmd
}

// --- Search Results ---

func (m Model) updateSearchResultsKeys(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var vpCmd tea.Cmd
	m.contentViewport, vpCmd = m.contentViewport.Update(key)

	switch key.String() {
	case "q", "esc", "left", "h":
		m.rightPanel = m.searchOrigin
		m.focus = m.focusFromRightPanel()
		return m, nil
	case "s":
		m.rightPanel = PanelSearch
		m.focus = FocusSearch
		m.searchOrigin = PanelSearchResults
		m.searchInput.Focus()
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.CursorEnd()
		return m, nil
	case "home":
		m.contentViewport.GotoTop()
		return m, nil
	case "end":
		m.contentViewport.GotoBottom()
		return m, nil
	}
	return m, vpCmd
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
	m.confirmActive = false
	m.confirmMsg = ""
	m.confirmAction = confirmNone
	return m, nil
}

// --- Search open helper ---

func (m Model) openSearchFromCurrentSelection() (tea.Model, tea.Cmd) {
	scope := m.selectedSession
	origin := m.rightPanel

	if scope == "" {
		if selected := m.selectedDashboardSession(); selected != nil {
			scope = selected.ID
			origin = PanelCaptures
		}
	}

	if scope == "" {
		m.setStatus("Select a session before searching")
		return m, nil
	}

	m.searchScope = scope
	m.searchOrigin = origin
	m.rightPanel = PanelSearch
	m.focus = FocusSearch
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

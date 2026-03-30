package tui

import (
	"fmt"
	"strings"

	"context-bridge/internal/store"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) View() string {
	if !m.ready && m.width == 0 && m.height == 0 {
		return "Loading context-bridge TUI..."
	}

	header := m.renderHeader()
	tabs := m.renderTabs()
	statusBar := m.renderStatusBar()

	// Calculate exact body dimensions
	// header(1) + tabs(1) + status(1) = 3 lines of chrome
	// strings.Join adds 3 newlines = 3 lines
	// appStyle adds Padding(1, 2) = 2 lines
	// Total reserved height = 8 lines
	bodyHeight := m.height - 8
	if bodyHeight < 5 {
		bodyHeight = 5
	}

	bodyWidth := m.width - 4 // appStyle padding(1, 2) -> 2 left + 2 right = 4
	if bodyWidth < 20 {
		bodyWidth = 20
	}

	var body string
	if m.activeTab == TabOverview {
		body = m.viewOverviewTab(bodyWidth, bodyHeight)
	} else if m.activeTab == TabSearch {
		body = m.viewSearchTab(bodyWidth, bodyHeight)
	} else {
		body = m.viewSessionsTab(bodyWidth, bodyHeight)
	}
	if m.settingsActive {
		dialog := m.renderSettingsDialog(bodyWidth - 8)
		body = lipgloss.Place(bodyWidth, bodyHeight, lipgloss.Center, lipgloss.Center, dialog)
	}

	parts := []string{header, tabs, body}
	if strings.TrimSpace(statusBar) != "" {
		parts = append(parts, statusBar)
	}
	content := strings.Join(parts, "\n")

	return appStyle.Render(content)
}

func (m Model) renderTabs() string {
	var tabs []string

	for i, name := range []string{"Overview", "Sessions", "Search"} {
		isActive := (m.activeTab == TabOverview && i == 0) || (m.activeTab == TabSessions && i == 1) || (m.activeTab == TabSearch && i == 2)
		style := tabStyle
		if isActive {
			style = activeTabStyle
		}
		tabs = append(tabs, style.Render(name))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m Model) viewOverviewTab(w, h int) string {
	var lines []string

	// Stats card
	if m.stats.Sessions > 0 || m.stats.Captures > 0 {
		card := fmt.Sprintf("  %s  ·  %s  ·  %s",
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Sessions))+" "+statLabelStyle.Render("sessions"),
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Captures))+" "+statLabelStyle.Render("outputs"),
			statLabelStyle.Render(store.FormatBytes(int(m.stats.TotalBytes))),
		)
		lines = append(lines, statCardStyle.Render(card))
	}

	lines = append(lines, "")
	lines = append(lines, heroStyle.Render(strings.TrimPrefix(heroASCII, "\n")))
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("Welcome to Context Bridge"))
	lines = append(lines, "")

	if len(m.dashboardSessions) == 0 {
		lines = append(lines, dimStyle.Render("No sessions captured yet.\nRun `context-bridge serve` and start an OpenCode session."))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  i install plugin  ·  q quit"))
	} else {
		lines = append(lines, dimStyle.Render("Press Tab to view sessions and their outputs."))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  tab/1/2 switch view  ·  i install plugin  ·  q quit"))
	}

	content := strings.Join(lines, "\n")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) viewSearchTab(w, h int) string {
	leftW := 30
	rightW := w - leftW
	if rightW < 20 {
		rightW = 20
	}

	leftPane := m.viewSessionsPane(leftW, h)

	var rightPaneContent string
	innerH := h - 2
	if innerH < 3 {
		innerH = 3
	}

	if m.confirmActive {
		dialog := m.renderConfirmDialog(rightW - 2)
		rightPaneContent = lipgloss.Place(rightW-2, innerH, lipgloss.Center, lipgloss.Center, dialog)
	} else if m.selectedSession == "" {
		rightPaneContent = m.viewEmptySearchState()
	} else {
		switch m.rightPanel {
		case PanelCaptureDetail:
			rightPaneContent = m.viewCapture(rightW, innerH)
		case PanelSearchResults:
			rightPaneContent = m.viewSearchResults(rightW, innerH)
		default:
			rightPaneContent = m.viewSearch(rightW, innerH)
		}
	}

	style := paneStyle
	if m.focus != FocusSessions || m.confirmActive {
		style = activePaneStyle
	}

	rightPane := style.Width(rightW - 2).Height(innerH).Render(rightPaneContent)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
}

func (m Model) viewEmptySearchState() string {
	var lines []string

	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("Search Session Context"))
	lines = append(lines, "")

	if len(m.dashboardSessions) == 0 {
		lines = append(lines, dimStyle.Render("No sessions available to search."))
	} else {
		lines = append(lines, dimStyle.Render("Select a session from the left menu to start searching."))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  ↑/↓ move  ·  enter start searching"))
	}

	return strings.Join(lines, "\n")
}

func (m Model) viewSessionsTab(w, h int) string {
	leftW := 30
	rightW := w - leftW
	if rightW < 20 {
		rightW = 20
	}

	leftPane := m.viewSessionsPane(leftW, h)
	rightPane := m.viewRightPane(rightW, h)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
}

func (m Model) viewSessionsPane(w, h int) string {
	var lines []string

	// Calculate inner height (subtracting 2 for borders)
	innerH := h - 2
	if innerH < 3 {
		innerH = 3
	}

	if len(m.dashboardSessions) == 0 {
		lines = append(lines, dimStyle.Render("No sessions."))
	} else {
		headerLines := 2 // " Sessions\nPress / to search"
		visibleCount := innerH - headerLines
		if visibleCount < 3 {
			visibleCount = 3
		}

		scroll := m.dashboardScroll
		total := len(m.dashboardSessions)
		end := scroll + visibleCount
		if end > total {
			end = total
		}

		for i := scroll; i < end; i++ {
			session := m.dashboardSessions[i]
			ts := formatSessionTime(session)
			meta := metaStyle.Render(ts)
			metaWidth := lipgloss.Width(ts)

			// w is outer width. inner text is w - 4. prefix is 2.
			// remaining for id and spacing is (w - 4) - 2 - metaWidth = w - 6 - metaWidth
			idAvailable := (w - 4) - 2 - metaWidth - 1 // 1 for spacing
			if idAvailable < 5 {
				idAvailable = 5
			}

			id := truncateID(session.ID, idAvailable)

			idStyle := lipgloss.NewStyle().Width(idAvailable)
			if session.DeletedAt != nil {
				idStyle = idStyle.Strikethrough(true).Foreground(colorMuted)
			}
			idCol := idStyle.Render(id)
			metaCol := lipgloss.NewStyle().Width(metaWidth).Align(lipgloss.Right).Render(meta)

			lineText := lipgloss.JoinHorizontal(lipgloss.Left, idCol, " ", metaCol)

			if i == m.dashboardCursor {
				lines = append(lines, selectedStyle.Render("▸ "+lineText))
			} else {
				if session.ID == m.selectedSession {
					lineText = lipgloss.NewStyle().Foreground(colorRose).Render(lineText)
				}
				lines = append(lines, "  "+lineText)
			}
		}

		if total > visibleCount {
			lines = append(lines, scrollStyle.Render(fmt.Sprintf(" %d-%d/%d", scroll+1, end, total)))
		}
	}

	style := paneStyle
	if m.focus == FocusSessions {
		style = activePaneStyle
	}

	header := titleStyle.Render(" Sessions") + "\n" + dimStyle.Render("Press x to delete") + "\n"

	return style.Width(w - 2).Height(innerH).Render(
		header + strings.Join(lines, "\n"),
	)
}

func (m Model) viewRightPane(w, h int) string {
	var content string

	// Calculate inner height (subtracting 2 for borders)
	innerH := h - 2
	if innerH < 3 {
		innerH = 3
	}

	if m.confirmActive {
		dialog := m.renderConfirmDialog(w - 2)
		content = lipgloss.Place(w-2, innerH, lipgloss.Center, lipgloss.Center, dialog)
	} else if m.selectedSession == "" {
		content = m.viewEmptySessionState()
	} else {
		switch m.rightPanel {
		case PanelCaptures:
			content = m.viewSession(w, innerH)
		case PanelCaptureDetail:
			content = m.viewCapture(w, innerH)
		case PanelSearch:
			content = m.viewSearch(w, innerH)
		case PanelSearchResults:
			content = m.viewSearchResults(w, innerH)
		default:
			content = "Unknown screen"
		}
	}

	style := paneStyle
	if m.focus != FocusSessions || m.confirmActive {
		style = activePaneStyle
	}

	return style.Width(w - 2).Height(innerH).Render(content)
}

func (m Model) viewEmptySessionState() string {
	var lines []string

	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("No Session Selected"))
	lines = append(lines, "")

	if len(m.dashboardSessions) == 0 {
		lines = append(lines, dimStyle.Render("No sessions available."))
	} else {
		lines = append(lines, dimStyle.Render("Select a session from the left menu to view its outputs."))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  ↑/↓ move  ·  enter open session  ·  / search"))
	}

	return strings.Join(lines, "\n")
}

func (m Model) viewSession(w, innerH int) string {
	var lines []string

	title := titleStyle.Render("Session Outputs") + "  " + metaStyle.Render(fmt.Sprintf("(%d items)", len(m.sessionCaptures)))
	lines = append(lines, title)
	lines = append(lines, "")

	if len(m.sessionCaptures) == 0 {
		lines = append(lines, dimStyle.Render("No outputs recorded for this session."))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  esc back to sessions"))
		return strings.Join(lines, "\n")
	}

	var filterLine string
	if m.filterActive {
		filterLine = filterBarStyle.Render("/ ") + m.filterInput.View()
	} else if m.filterQuery != "" {
		filterLine = filterBarStyle.Render("/ ") + m.filterQuery + dimStyle.Render(" (press / to edit, esc to clear)")
	} else {
		filterLine = dimStyle.Render("/ type to filter list...")
	}
	lines = append(lines, filterLine)
	lines = append(lines, "")

	visible := m.filteredCaptures()

	// fixed elements inside the pane: title(2) + filter(2) + preview(2) + help(2) = ~8 lines
	visibleCount := innerH - 8
	if visibleCount < 3 {
		visibleCount = 3
	}

	scroll := m.sessionScroll
	total := len(visible)
	end := scroll + visibleCount
	if end > total {
		end = total
	}

	if total == 0 && m.filterQuery != "" {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("No outputs match %q.  Press esc to clear the filter.", m.filterQuery)))
		lines = append(lines, "")
		lines = append(lines, helpStyle.Render("  esc clear filter"))
		return strings.Join(lines, "\n")
	}

	var items []string
	for i := scroll; i < end; i++ {
		c := visible[i]

		seqStr := dimStyle.Render(fmt.Sprintf("#%-3d", c.Seq))
		seqCol := lipgloss.NewStyle().Width(5).Render(seqStr)

		badge := agentBadge(c.Agent)
		badgeCol := lipgloss.NewStyle().Width(12).Render(badge)

		timeStr := store.FormatRelativeTime(c.CapturedAt)
		bytesStr := store.FormatBytes(c.Bytes)
		metaText := fmt.Sprintf("%s  %s", timeStr, bytesStr)
		if c.ChildSessionID != "" {
			metaText = "↱ subsession  " + metaText
		}
		meta := metaStyle.Render(metaText)
		metaWidth := lipgloss.Width(metaText)
		metaCol := lipgloss.NewStyle().Width(metaWidth).Align(lipgloss.Right).Render(meta)

		descAvailable := (w - 4) - 21 - metaWidth
		if descAvailable < 10 {
			descAvailable = 10
		}
		descText := truncateLine(c.Description, descAvailable)
		descCol := lipgloss.NewStyle().Width(descAvailable).Render(descText)

		line := lipgloss.JoinHorizontal(lipgloss.Left, seqCol, badgeCol, descCol, "  ", metaCol)

		if i == m.sessionCursor && m.focus == FocusCaptures {
			items = append(items, selectedStyle.Render("▸ "+line))
		} else if i == m.sessionCursor {
			items = append(items, "▸ "+line)
		} else {
			items = append(items, "  "+line)
		}
	}
	lines = append(lines, strings.Join(items, "\n"))

	if total > visibleCount {
		indicator := scrollStyle.Render(fmt.Sprintf("  showing %d–%d of %d", scroll+1, end, total))
		if end < total {
			indicator += scrollStyle.Render("  ↓ more")
		}
		lines = append(lines, indicator)
	}

	lines = append(lines, "")

	if selected := m.selectedSessionCaptureFromFiltered(visible); selected != nil {
		previewText := strings.TrimSpace(selected.Preview)
		if previewText == "" {
			previewText = strings.TrimSpace(selected.Content)
		}
		if previewText != "" {
			previewLine := truncateLine(previewText, w-8)
			lines = append(lines, panelStyle.Width(w-6).Render(dimStyle.Render(previewLine)))
			lines = append(lines, "")
		}
	}

	if m.filterActive {
		lines = append(lines, helpStyle.Render("  type to filter  ·  enter confirm  ·  esc clear filter"))
	} else if m.focus == FocusCaptures {
		lines = append(lines, helpStyle.Render("  j/k move  ·  enter open  ·  / filter  ·  s full-text search  ·  x delete  ·  esc back"))
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewCapture(w, innerH int) string {
	if m.selectedCapture == nil {
		return panelStyle.Render("No capture loaded.")
	}

	var headerLines []string
	badge := agentBadge(m.selectedCapture.Agent)
	title := titleStyle.Render(fmt.Sprintf("Output #%d", m.selectedCapture.Seq)) + "  " + badge
	headerLines = append(headerLines, title)
	headerLines = append(headerLines, metaStyle.Render(fmt.Sprintf("%s  ·  %s",
		store.FormatBytes(m.selectedCapture.Bytes),
		m.selectedCapture.CapturedAt.Format("2006-01-02 15:04 UTC"),
	)))
	if desc := strings.TrimSpace(m.selectedCapture.Description); desc != "" {
		headerLines = append(headerLines, dimStyle.Render(truncateLine(desc, w-4)))
	}
	header := strings.Join(headerLines, "\n")

	body := m.contentViewport.View()
	if strings.TrimSpace(body) == "" {
		body = dimStyle.Render("No content")
	}
	contentBox := contentStyle.Render(body)

	var footerLines []string
	scrollPercent := fmt.Sprintf("%3.f%%", m.contentViewport.ScrollPercent()*100)
	if m.focus == FocusCaptureDetail {
		footerLines = append(footerLines, helpStyle.Render("  ↑/↓ scroll  ·  pgup/pgdn page  ·  home/end jump  ·  s search  ·  esc back")+lipgloss.NewStyle().Foreground(colorRose).Render(fmt.Sprintf("   [%s]", scrollPercent)))
	} else {
		footerLines = append(footerLines, lipgloss.NewStyle().Foreground(colorSubtle).Render(fmt.Sprintf("   [%s]", scrollPercent)))
	}
	footer := strings.Join(footerLines, "\n")

	parts := []string{header, contentBox}
	if footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
}

func (m Model) viewSearch(w, innerH int) string {
	var parts []string
	parts = append(parts, titleStyle.Render("Full-Text Search"))

	sessionText := "Session: " + truncateID(m.searchScope, 12)
	parts = append(parts, metaStyle.Render(truncateLine(sessionText, w-4)))
	parts = append(parts, metaStyle.Render(truncateLine(fmt.Sprintf("Engine: %s (global setting, press p to change)", m.searchMode), w-4)))

	parts = append(parts, "")
	parts = append(parts, panelStyle.Width(w-6).Render(m.searchInput.View()))
	parts = append(parts, "")
	if m.searchErr != "" {
		parts = append(parts, errorStyle.Render(truncateLine(m.searchErr, w-4)))
		parts = append(parts, "")
	}
	parts = append(parts, helpStyle.Render("  type query  ·  enter search  ·  esc cancel"))
	return strings.Join(parts, "\n")
}

func (m Model) viewSearchResults(w, innerH int) string {
	var header []string

	// Truncate to fit inside the pane (inner width is w - 4)
	titleText := fmt.Sprintf("Raw MCP Search Results for %q", m.searchQuery)
	header = append(header, titleStyle.Render(truncateLine(titleText, w-4)))

	sessionText := fmt.Sprintf("Session: %s", truncateID(m.searchScope, 12))
	header = append(header, metaStyle.Render(truncateLine(sessionText, w-4)))
	header = append(header, "")

	body := m.contentViewport.View()
	if strings.TrimSpace(body) == "" {
		body = dimStyle.Render("No content")
	}
	contentBox := contentStyle.Render(body)

	var footerLines []string
	scrollPercent := fmt.Sprintf("%3.f%%", m.contentViewport.ScrollPercent()*100)
	if m.focus == FocusSearchResults {
		footerLines = append(footerLines, helpStyle.Render("  ↑/↓ scroll  ·  pgup/pgdn page  ·  home/end jump  ·  s new search  ·  esc back")+lipgloss.NewStyle().Foreground(colorRose).Render(fmt.Sprintf("   [%s]", scrollPercent)))
	} else {
		footerLines = append(footerLines, lipgloss.NewStyle().Foreground(colorSubtle).Render(fmt.Sprintf("   [%s]", scrollPercent)))
	}
	footer := strings.Join(footerLines, "\n")

	parts := []string{strings.Join(header, "\n"), contentBox}
	if footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
}

// renderHeader renders the app title and stats breadcrumb in a single line.
func (m Model) renderHeader() string {
	title := headerStyle.Render("Context Bridge")
	if m.stats.Sessions > 0 || m.stats.Captures > 0 {
		statsStr := fmt.Sprintf("%s  ·  %s  ·  %s",
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Sessions))+statLabelStyle.Render(" sessions"),
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Captures))+statLabelStyle.Render(" outputs"),
			statLabelStyle.Render(store.FormatBytes(int(m.stats.TotalBytes))),
		)
		return title + "  " + metaStyle.Render("·") + "  " + statsStr
	}
	return title
}

// renderStatusBar renders spinner, status, or error below the body content.
func (m Model) renderStatusBar() string {
	var parts []string

	// Global key hints
	parts = append(parts, helpStyle.Render("tab/1/2/3: switch tab • s: search • p: settings • ctrl+c: quit"))

	if m.loading {
		parts = append(parts, statusStyle.Render(m.spinner.View()+"  Loading…"))
	} else if m.errorMsg != "" {
		parts = append(parts, errorStyle.Render("✗  "+m.errorMsg))
	} else if m.statusMsg != "" {
		parts = append(parts, statusStyle.Render(m.statusMsg))
	}

	return strings.Join(parts, "  |  ")
}

// renderConfirmDialog renders a modal confirmation box.
func (m Model) renderConfirmDialog(maxWidth int) string {
	var inner strings.Builder
	inner.WriteString(confirmWarningStyle.Render("⚠  " + m.confirmMsg))
	inner.WriteString("\n")

	if len(m.confirmMeta) > 0 {
		inner.WriteString("\n")
		for _, meta := range m.confirmMeta {
			inner.WriteString(dimStyle.Render("  "+truncateLine(meta, maxWidth-8)) + "\n")
		}
	}

	inner.WriteString("\n")
	inner.WriteString(helpStyle.Render("[y] Confirm  [n/esc] Cancel"))

	// Optional: add a hard width to the box itself if it looks too wide, but padding does enough usually
	return confirmBoxStyle.Render(inner.String())
}

func (m Model) renderSettingsDialog(maxWidth int) string {
	if maxWidth < 40 {
		maxWidth = 40
	}

	options := []struct {
		mode  store.SearchMode
		label string
		desc  string
	}{
		{mode: store.SearchModeRegex, label: "Regex", desc: "Literal + regex pattern matching across captured outputs."},
		{mode: store.SearchModeFTS5, label: "FTS5", desc: "SQLite full-text token search across captured outputs."},
	}

	var inner strings.Builder
	inner.WriteString(titleStyle.Render("Settings"))
	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render(truncateLine("Global search engine for all TUI searches. This is not a per-search option.", maxWidth)))
	inner.WriteString("\n\n")

	for i, option := range options {
		cursor := "  "
		if i == m.settingsCursor {
			cursor = "▸ "
		}

		current := ""
		if option.mode == m.searchMode {
			current = " " + metaStyle.Render("(current)")
		}

		line := cursor + option.label + current
		if i == m.settingsCursor {
			inner.WriteString(selectedStyle.Render(line))
		} else {
			inner.WriteString(line)
		}
		inner.WriteString("\n")
		inner.WriteString(dimStyle.Render("   " + truncateLine(option.desc, maxWidth-3)))
		inner.WriteString("\n\n")
	}

	if m.settingsSaveError != "" {
		inner.WriteString(errorStyle.Render(truncateLine(m.settingsSaveError, maxWidth)))
		inner.WriteString("\n\n")
	}

	inner.WriteString(helpStyle.Render("[↑/↓] Choose  [enter] Save  [esc] Cancel"))
	return settingsBoxStyle.Width(maxWidth + 4).Render(inner.String())
}

const heroASCII = `
   ____ ___  _   _ _____ _______  _______   ____  ____  ___ ____   ____ _____ 
  / ___/ _ \| \ | |_   _| ____\ \/ /_   _| | __ )|  _ \|_ _|  _ \ / ___| ____|
 | |  | | | |  \| | | | |  _|  \  /  | |   |  _ \| |_) || || | | | |  _|  _|  
 | |__| |_| | |\  | | | | |___ /  \  | |   | |_) |  _ < | || |_| | |_| | |___ 
  \____\___/|_| \_| |_| |_____/_/\_\ |_|   |____/|_| \_\___|____/ \____|_____|`

// truncateLine collapses newlines and trims to width with an ellipsis.
func truncateLine(value string, width int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if width <= 0 || len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "…"
}

// truncateID shortens a session/call ID to a readable form: prefix…suffix.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	if maxLen < 8 {
		return id[:maxLen]
	}
	half := (maxLen - 1) / 2
	return id[:half] + "…" + id[len(id)-half:]
}

// formatSessionTime returns the human-readable time for a session summary.
func formatSessionTime(summary store.SessionSummary) string {
	if !summary.LastCapturedAt.IsZero() {
		return store.FormatRelativeTime(summary.LastCapturedAt)
	}
	return store.FormatRelativeTime(summary.CreatedAt)
}

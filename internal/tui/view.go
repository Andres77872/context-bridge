package tui

import (
	"fmt"
	"strings"

	"context-bridge/internal/store"
)

func (m Model) View() string {
	if !m.ready && m.width == 0 && m.height == 0 {
		return "Loading context-bridge TUI..."
	}

	body := m.viewBody()
	statusBar := m.renderStatusBar()
	parts := []string{body}
	if strings.TrimSpace(statusBar) != "" {
		parts = append(parts, statusBar)
	}
	content := strings.Join(parts, "\n")

	if m.confirmActive {
		content += "\n" + m.renderConfirmDialog()
	}

	return appStyle.Render(content)
}

func (m Model) viewBody() string {
	switch m.screen {
	case ScreenDashboard:
		return m.viewDashboard()
	case ScreenSession:
		return m.viewSession()
	case ScreenCapture:
		return m.viewCapture()
	case ScreenSearch:
		return m.viewSearch()
	case ScreenSearchResults:
		return m.viewSearchResults()
	default:
		return "Unknown screen"
	}
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

// renderBreadcrumb renders a contextual sub-title for child screens.
func (m Model) renderBreadcrumb(extra string) string {
	parts := []string{headerStyle.Render("Context Bridge")}
	if m.selectedSession != "" {
		parts = append(parts, metaStyle.Render(truncateID(m.selectedSession, 24)))
	}
	if extra != "" {
		parts = append(parts, metaStyle.Render(extra))
	}
	return strings.Join(parts, metaStyle.Render("  ›  "))
}

// renderStatusBar renders spinner, status, or error below the body content.
func (m Model) renderStatusBar() string {
	if m.loading {
		return statusStyle.Render(m.spinner.View() + "  Loading…")
	}
	if m.errorMsg != "" {
		return errorStyle.Render("✗  " + m.errorMsg)
	}
	if m.statusMsg != "" {
		return statusStyle.Render(m.statusMsg)
	}
	return ""
}

// renderConfirmDialog renders a modal confirmation box.
func (m Model) renderConfirmDialog() string {
	var inner strings.Builder
	inner.WriteString(confirmWarningStyle.Render("⚠  " + m.confirmMsg))
	inner.WriteString("\n\n")
	inner.WriteString(helpStyle.Render("[y] Confirm  [n/esc] Cancel"))
	return confirmBoxStyle.Render(inner.String())
}

func (m Model) viewDashboard() string {
	var lines []string
	lines = append(lines, m.renderHeader())

	// Stats card — rendered below the header only when we have data.
	if m.stats.Sessions > 0 || m.stats.Captures > 0 {
		card := fmt.Sprintf("  %s  ·  %s  ·  %s",
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Sessions))+" "+statLabelStyle.Render("sessions"),
			statNumberStyle.Render(fmt.Sprintf("%d", m.stats.Captures))+" "+statLabelStyle.Render("outputs"),
			statLabelStyle.Render(store.FormatBytes(int(m.stats.TotalBytes))),
		)
		lines = append(lines, statCardStyle.Render(card))
	}

	if len(m.dashboardSessions) == 0 {
		empty := dimStyle.Render("No sessions captured yet.\nRun context-bridge serve and start an OpenCode session.")
		lines = append(lines, empty)
		lines = append(lines, helpStyle.Render("  q quit"))
		return strings.Join(lines, "\n\n")
	}

	// Compute visible window.
	fixedRows := 6 // header, stats card, help, status, app padding
	visibleCount := m.height - fixedRows
	if visibleCount < 3 {
		visibleCount = 3
	}

	scroll := m.dashboardScroll
	total := len(m.dashboardSessions)
	end := scroll + visibleCount
	if end > total {
		end = total
	}

	var items []string
	for i := scroll; i < end; i++ {
		session := m.dashboardSessions[i]
		id := truncateID(session.ID, 26)
		ts := formatSessionTime(session)
		line := fmt.Sprintf("%-26s  %s outputs  %s", id, statNumberStyle.Render(fmt.Sprintf("%d", session.CaptureCount)), metaStyle.Render(ts))
		if session.DeletedAt != nil {
			line += "  " + dimStyle.Render("[deleted]")
		}
		if i == m.dashboardCursor {
			items = append(items, selectedStyle.Render("▸ "+line))
		} else {
			items = append(items, "  "+line)
		}
	}
	lines = append(lines, strings.Join(items, "\n"))

	// Scroll indicator.
	if total > visibleCount {
		indicator := scrollStyle.Render(fmt.Sprintf("  showing %d–%d of %d", scroll+1, end, total))
		if end < total {
			indicator += scrollStyle.Render("  ↓ more")
		}
		lines = append(lines, indicator)
	}

	lines = append(lines, helpStyle.Render("  j/k move  ·  enter open session  ·  / search selected  ·  q quit"))
	return strings.Join(lines, "\n\n")
}

func (m Model) viewSession() string {
	var lines []string
	lines = append(lines, m.renderBreadcrumb(fmt.Sprintf("%d outputs", len(m.sessionCaptures))))

	if len(m.sessionCaptures) == 0 {
		lines = append(lines, dimStyle.Render("No outputs recorded for this session."))
		lines = append(lines, helpStyle.Render("  esc back"))
		return strings.Join(lines, "\n\n")
	}

	// Filter bar.
	if m.filterActive {
		prompt := filterBarStyle.Render("/ ") + m.filterInput.View()
		lines = append(lines, prompt)
	}

	visible := m.filteredCaptures()
	fixedRows := 7 // header, help, status, optional filter bar, app padding
	if m.filterActive {
		fixedRows++
	}
	visibleCount := m.height - fixedRows
	if visibleCount < 3 {
		visibleCount = 3
	}

	scroll := m.sessionScroll
	total := len(visible)
	end := scroll + visibleCount
	if end > total {
		end = total
	}

	if total == 0 && m.filterActive {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("No outputs match %q.  Press esc to clear the filter.", m.filterQuery)))
		lines = append(lines, helpStyle.Render("  esc clear filter"))
		return strings.Join(lines, "\n\n")
	}

	var items []string
	for i := scroll; i < end; i++ {
		c := visible[i]
		badge := agentBadge(c.Agent)
		desc := truncateLine(c.Description, 60)
		meta := metaStyle.Render(fmt.Sprintf("%s  %s", store.FormatRelativeTime(c.CapturedAt), store.FormatBytes(c.Bytes)))
		line := fmt.Sprintf("#%-3d %s  %s  %s", c.Seq, badge, desc, meta)
		if i == m.sessionCursor {
			items = append(items, selectedStyle.Render("▸ "+line))
		} else {
			items = append(items, "  "+line)
		}
	}
	lines = append(lines, strings.Join(items, "\n"))

	// Scroll indicator.
	if total > visibleCount {
		indicator := scrollStyle.Render(fmt.Sprintf("  showing %d–%d of %d", scroll+1, end, total))
		if end < total {
			indicator += scrollStyle.Render("  ↓ more")
		}
		lines = append(lines, indicator)
	}

	// Preview of selected capture.
	if selected := m.selectedSessionCaptureFromFiltered(visible); selected != nil && strings.TrimSpace(selected.Preview) != "" {
		lines = append(lines, dimStyle.Render(truncateLine(selected.Preview, 120)))
	}

	// Contextual help.
	if m.filterActive {
		lines = append(lines, helpStyle.Render("  type to filter  ·  enter confirm  ·  esc clear filter"))
	} else {
		lines = append(lines, helpStyle.Render("  j/k move  ·  enter open  ·  f filter  ·  / search  ·  esc/q back"))
	}
	return strings.Join(lines, "\n\n")
}

func (m Model) viewCapture() string {
	if m.selectedCapture == nil {
		return panelStyle.Render("No capture loaded.")
	}

	var lines []string
	badge := agentBadge(m.selectedCapture.Agent)
	lines = append(lines, m.renderBreadcrumb(fmt.Sprintf("#%d  %s", m.selectedCapture.Seq, badge)))
	lines = append(lines, metaStyle.Render(fmt.Sprintf("%s  ·  %s  ·  %s",
		truncateID(m.selectedCapture.SessionID, 24),
		store.FormatBytes(m.selectedCapture.Bytes),
		m.selectedCapture.CapturedAt.Format("2006-01-02 15:04 UTC"),
	)))
	if desc := strings.TrimSpace(m.selectedCapture.Description); desc != "" {
		lines = append(lines, dimStyle.Render(desc))
	}

	body := m.contentViewport.View()
	if strings.TrimSpace(body) == "" {
		body = dimStyle.Render("No content")
	}
	lines = append(lines, contentStyle.Render(body))
	lines = append(lines, helpStyle.Render("  ↑/↓ scroll  ·  pgup/pgdn page  ·  home/end jump  ·  / search  ·  esc/q back"))
	return strings.Join(lines, "\n\n")
}

func (m Model) viewSearch() string {
	var parts []string
	parts = append(parts, m.renderBreadcrumb("Search"))
	parts = append(parts, metaStyle.Render("Scope: "+m.searchScope))
	parts = append(parts, panelStyle.Render(m.searchInput.View()))
	if m.searchErr != "" {
		parts = append(parts, errorStyle.Render(m.searchErr))
	}
	parts = append(parts, helpStyle.Render("  type query  ·  enter search  ·  esc cancel"))
	return strings.Join(parts, "\n\n")
}

func (m Model) viewSearchResults() string {
	var header []string
	header = append(header, m.renderBreadcrumb(fmt.Sprintf("Search: %q", m.searchQuery)))
	header = append(header, metaStyle.Render(fmt.Sprintf("Scope: %s", m.searchScope)))

	if len(m.searchResults) == 0 {
		header = append(header, dimStyle.Render(fmt.Sprintf("No matches for %q.\nTry a different term or esc to go back.", m.searchQuery)))
		header = append(header, helpStyle.Render("  esc back  ·  / edit search"))
		return strings.Join(header, "\n\n")
	}

	fixedRows := 8 // header, scope, snippet preview, help, status, app padding
	visibleCount := m.height - fixedRows
	if visibleCount < 3 {
		visibleCount = 3
	}

	total := len(m.searchResults)
	scroll := m.searchResultsScroll
	end := scroll + visibleCount
	if end > total {
		end = total
	}

	var items []string
	for i := scroll; i < end; i++ {
		r := m.searchResults[i]
		badge := agentBadge(r.Capture.Agent)
		desc := truncateLine(r.Capture.Description, 60)
		matches := dimStyle.Render(fmt.Sprintf("(%d match)", r.MatchCount))
		if r.MatchCount != 1 {
			matches = dimStyle.Render(fmt.Sprintf("(%d matches)", r.MatchCount))
		}
		line := fmt.Sprintf("#%-3d %s  %s  %s", r.Capture.Seq, badge, desc, matches)
		if i == m.searchCursor {
			items = append(items, selectedStyle.Render("▸ "+line))
		} else {
			items = append(items, "  "+line)
		}
	}
	header = append(header, strings.Join(items, "\n"))

	// Scroll indicator.
	if total > visibleCount {
		indicator := scrollStyle.Render(fmt.Sprintf("  showing %d–%d of %d", scroll+1, end, total))
		if end < total {
			indicator += scrollStyle.Render("  ↓ more")
		}
		header = append(header, indicator)
	}

	// Snippet of selected result.
	if selected := m.selectedSearchResult(); selected != nil && strings.TrimSpace(selected.Snippet) != "" {
		header = append(header, panelStyle.Render(truncateLine(selected.Snippet, 200)))
	}

	header = append(header, helpStyle.Render("  j/k move  ·  enter open  ·  / refine search  ·  esc back"))
	return strings.Join(header, "\n\n")
}

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

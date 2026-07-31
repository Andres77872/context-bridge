package tui

import (
	"context"
	"fmt"
	"strings"

	"context-bridge/internal/config"
	"context-bridge/internal/mcp"
	"context-bridge/internal/store"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/reflow/wordwrap"
)

const (
	dashboardLimit = 100
	// overviewWindowDays is the activity window summarised on the overview tab.
	overviewWindowDays = 14
)

type Tab int

const (
	TabOverview Tab = iota
	TabSessions
	TabSearch
)

// FocusPane represents the currently focused pane.
type FocusPane int

const (
	FocusSessions FocusPane = iota
	FocusCaptures
	FocusCaptureDetail
	FocusSearch
	FocusSearchResults
)

// RightPanel represents the view state of the right panel.
type RightPanel int

const (
	PanelCaptures RightPanel = iota
	PanelCaptureDetail
	PanelSearch
	PanelSearchResults
)

// confirmAction represents the action to perform after confirmation.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDeleteSession
	confirmDeleteCapture
)

// Model is the root Bubble Tea model for the TUI.
type Model struct {
	store      *store.Store
	searchMode store.SearchMode

	activeTab Tab

	focus           FocusPane
	rightPanel      RightPanel
	prevPanel       RightPanel
	searchOrigin    RightPanel
	searchOriginFoc FocusPane

	width  int
	height int

	// Loading and messaging state.
	loading   bool
	errorMsg  string
	statusMsg string
	spinner   spinner.Model

	// Stats loaded once at Init.
	stats store.StoreStats

	// Usage aggregates powering the overview tab.
	analytics       store.Analytics
	analyticsLoaded bool

	// Dashboard screen state.
	dashboardSessions []store.SessionSummary
	dashboardCursor   int
	dashboardScroll   int

	// Session screen state.
	selectedSession string
	sessionCaptures []store.CaptureRecord
	sessionCursor   int
	sessionScroll   int

	// Inline filter state (session screen).
	filterInput  textinput.Model
	filterActive bool
	filterQuery  string

	// Capture screen state.
	selectedCapture *store.CaptureRecord
	contentViewport viewport.Model
	// captureAgentView holds the exact MCP `read` payload for the selected
	// capture; captureRaw switches the pane to the stored document instead.
	captureAgentView string
	captureRaw       bool

	// Search screen state.
	searchInput textinput.Model
	searchScope string
	// searchAllSessions widens the next search to every live session instead
	// of the selected one.
	searchAllSessions   bool
	searchResults       []store.SearchResult
	searchRawOutput     string
	searchCursor        int
	searchResultsScroll int
	searchQuery         string
	searchErr           string

	// Confirm layer state (infrastructure for future destructive actions).
	confirmActive bool
	confirmMsg    string
	confirmMeta   []string // extra metadata lines for the confirm dialog
	confirmAction confirmAction

	// Settings modal state.
	settingsActive    bool
	settingsCursor    int
	settingsSaveError string
	configPath        string

	ready bool
}

// Async message types.

type dashboardLoadedMsg struct {
	sessions []store.SessionSummary
	err      error
}

type statsLoadedMsg struct {
	stats store.StoreStats
	err   error
}

type sessionLoadedMsg struct {
	sessionID string
	captures  []store.CaptureRecord
	err       error
}

type captureLoadedMsg struct {
	record    *store.CaptureRecord
	agentView string
	err       error
}

type searchLoadedMsg struct {
	sessionID     string
	rootID        string
	totalCaptures int
	query         string
	results       []store.SearchResult
	allSessions   bool
	err           error
}

type analyticsLoadedMsg struct {
	analytics store.Analytics
	err       error
}

// New initialises the TUI model. The search input starts unfocused; it is
// focused only when entering ScreenSearch.
func New(st *store.Store, searchMode store.SearchMode) Model {
	searchInput := textinput.New()
	searchInput.Placeholder = "Search this session"
	searchInput.Prompt = "/ "
	searchInput.CharLimit = 200
	// NOTE: intentionally NOT calling searchInput.Focus() here.
	// Focus is set when entering ScreenSearch; lack of this was a pre-existing bug.

	filterInput := textinput.New()
	filterInput.Placeholder = "filter outputs…"
	filterInput.Prompt = ""
	filterInput.CharLimit = 100

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	return Model{
		store:           st,
		searchMode:      searchMode,
		activeTab:       TabOverview,
		focus:           FocusSessions,
		rightPanel:      PanelCaptures,
		prevPanel:       PanelCaptures,
		searchOrigin:    PanelCaptures,
		searchOriginFoc: FocusCaptures,
		searchInput:     searchInput,
		filterInput:     filterInput,
		spinner:         sp,
		contentViewport: viewport.New(0, 0),
	}
}

// Run starts the TUI program with alt-screen mode.
func Run(st *store.Store, searchMode store.SearchMode, configPath string) error {
	model := New(st, searchMode)
	model.configPath = configPath
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := program.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(loadDashboardCmd(m.store), loadStatsCmd(m.store), loadAnalyticsCmd(m.store), m.spinner.Tick)
}

// Command constructors.

func loadDashboardCmd(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		sessions, err := st.ListRootSessions(dashboardLimit)
		return dashboardLoadedMsg{sessions: sessions, err: err}
	}
}

func loadStatsCmd(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		if st == nil {
			return statsLoadedMsg{}
		}
		stats, err := st.Stats()
		return statsLoadedMsg{stats: stats, err: err}
	}
}

func loadSessionCmd(st *store.Store, sessionID string) tea.Cmd {
	return func() tea.Msg {
		captures, err := st.ListCaptures(sessionID, "")
		return sessionLoadedMsg{sessionID: sessionID, captures: captures, err: err}
	}
}

func loadCaptureCmd(st *store.Store, sessionID string, seq int, searchMode store.SearchMode) tea.Cmd {
	return func() tea.Msg {
		record, err := st.GetCaptureBySeq(sessionID, seq)
		if err != nil {
			return captureLoadedMsg{err: err}
		}
		// Render the agent view up front so the pane never has to hit the
		// database while drawing.
		agentView, renderErr := mcp.RenderRead(context.Background(), st, sessionID, seq)
		if renderErr != nil {
			agentView = ""
		}
		return captureLoadedMsg{record: record, agentView: agentView}
	}
}

// loadAnalyticsCmd loads the usage aggregates shown on the overview tab. The
// window matches the sparkline the overview renders.
func loadAnalyticsCmd(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		if st == nil {
			return analyticsLoadedMsg{}
		}
		analytics, err := st.AnalyticsContext(context.Background(), overviewWindowDays)
		return analyticsLoadedMsg{analytics: analytics, err: err}
	}
}

// loadSearchCmd runs a search either inside one session or across every live
// session when allSessions is set.
func loadSearchCmd(st *store.Store, sessionID, query string, searchMode store.SearchMode, allSessions bool) tea.Cmd {
	return func() tea.Msg {
		scope := sessionID
		if allSessions {
			scope = ""
		}

		results, err := st.SearchContext(context.Background(), query, store.SearchOptions{
			SessionID:    scope,
			Mode:         searchMode,
			ContextLines: 3,
		})

		msg := searchLoadedMsg{
			sessionID:   sessionID,
			rootID:      sessionID,
			query:       query,
			results:     results,
			allSessions: allSessions,
			err:         err,
		}

		if allSessions {
			if stats, statsErr := st.Stats(); statsErr == nil {
				msg.totalCaptures = stats.Captures
			}
			return msg
		}

		if rootID, rootErr := st.ResolveRoot(sessionID); rootErr == nil {
			msg.rootID = rootID
		}
		captures, _ := st.ListCaptures(sessionID, "")
		msg.totalCaptures = len(captures)
		return msg
	}
}

// Cursor / selection helpers.

func (m Model) selectedDashboardSession() *store.SessionSummary {
	if len(m.dashboardSessions) == 0 || m.dashboardCursor < 0 || m.dashboardCursor >= len(m.dashboardSessions) {
		return nil
	}
	return &m.dashboardSessions[m.dashboardCursor]
}

func (m Model) selectedSessionCapture() *store.CaptureRecord {
	if len(m.sessionCaptures) == 0 || m.sessionCursor < 0 || m.sessionCursor >= len(m.sessionCaptures) {
		return nil
	}
	return &m.sessionCaptures[m.sessionCursor]
}

// selectedSessionCaptureFromFiltered returns the capture at m.sessionCursor
// within the given (possibly filtered) slice.
func (m Model) selectedSessionCaptureFromFiltered(visible []store.CaptureRecord) *store.CaptureRecord {
	if len(visible) == 0 || m.sessionCursor < 0 || m.sessionCursor >= len(visible) {
		return nil
	}
	c := visible[m.sessionCursor]
	return &c
}

// filteredCaptures returns the session captures filtered by filterQuery (client-side).
func (m Model) filteredCaptures() []store.CaptureRecord {
	if strings.TrimSpace(m.filterQuery) == "" {
		return m.sessionCaptures
	}
	q := strings.ToLower(strings.TrimSpace(m.filterQuery))
	var out []store.CaptureRecord
	for _, c := range m.sessionCaptures {
		seqStr := fmt.Sprintf("%d", c.Seq)
		if strings.Contains(seqStr, q) ||
			strings.Contains(strings.ToLower(c.Agent), q) ||
			strings.Contains(strings.ToLower(c.Description), q) {
			out = append(out, c)
		}
	}
	return out
}

// Filter helpers.

func (m *Model) activateFilter() {
	if m.confirmActive {
		return
	}
	m.filterActive = true
	m.filterQuery = ""
	m.filterInput.SetValue("")
	m.filterInput.Focus()
	m.sessionCursor = 0
	m.sessionScroll = 0
}

func (m *Model) deactivateFilter() {
	m.filterActive = false
	m.filterQuery = ""
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.sessionCursor = 0
	m.sessionScroll = 0
}

func (m *Model) activateSettings() {
	if m.confirmActive {
		return
	}
	m.settingsActive = true
	m.settingsSaveError = ""
	m.settingsCursor = m.settingsCursorForMode(m.searchMode)
}

func (m *Model) closeSettings() {
	m.settingsActive = false
	m.settingsSaveError = ""
}

func (m Model) settingsModeForCursor() store.SearchMode {
	if m.settingsCursor == 1 {
		return store.SearchModeFTS5
	}
	return store.SearchModeRegex
}

func (m Model) settingsCursorForMode(mode store.SearchMode) int {
	if mode == store.SearchModeFTS5 {
		return 1
	}
	return 0
}

func (m *Model) saveSettingsSelection() error {
	if m.configPath == "" {
		err := fmt.Errorf("settings cannot be saved because config path is unavailable")
		m.settingsSaveError = err.Error()
		return err
	}

	mode := m.settingsModeForCursor()
	err := config.UpdateConfig(m.configPath, false, func(cfg config.Config) config.Config {
		cfg.SearchMode = config.SearchMode(mode)
		return cfg
	})
	if err != nil {
		m.settingsSaveError = err.Error()
		return err
	}

	m.searchMode = mode
	m.closeSettings()
	m.setStatus(fmt.Sprintf("Global search engine set to %s", mode))
	return nil
}

// syncComponentSize recalculates viewport and input dimensions on window resize.
func (m *Model) syncComponentSize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.ready = true

	rightPaneWidth := m.width - 34 // 34 for left pane + app padding.
	if rightPaneWidth < 20 {
		rightPaneWidth = 20
	}

	// Inner width of right pane is rightPaneWidth - 4 (due to borders + padding).
	innerWidth := rightPaneWidth - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	// For the search input inside a panel, the panel adds 4.
	// So to fit in innerWidth, input itself must be innerWidth - 4.
	inputWidth := innerWidth - 4
	if inputWidth < 20 {
		inputWidth = 20
	}
	m.searchInput.Width = inputWidth

	// filterInput is prepended with "/ " (2 chars), so it can take innerWidth - 2.
	filterWidth := innerWidth - 2
	if filterWidth < 20 {
		filterWidth = 20
	}
	m.filterInput.Width = filterWidth

	// viewports are rendered inside contentStyle which has padding 1 (adds 2 to width).
	// So vpWidth must be innerWidth - 2 to fit inside the pane.
	vpWidth := innerWidth - 2
	if vpWidth < 20 {
		vpWidth = 20
	}
	vpHeight := m.height - 18 // header + meta + desc + help + status + padding + borders + tabs
	if vpHeight < 5 {
		vpHeight = 5
	}
	m.contentViewport.Width = vpWidth
	m.contentViewport.Height = vpHeight
	if m.rightPanel == PanelCaptureDetail && m.selectedCapture != nil {
		wrappedContent := wordwrap.String(m.captureContent(), vpWidth)
		m.contentViewport.SetContent(wrappedContent)
	} else if m.rightPanel == PanelSearchResults && m.searchRawOutput != "" {
		wrappedContent := wordwrap.String(m.searchRawOutput, vpWidth)
		m.contentViewport.SetContent(wrappedContent)
	}
}

// captureContent returns what the capture pane should display. The default is
// the agent view: the exact payload the MCP `read` tool hands to the model,
// trust boundary and truncation included, because inspecting what the agent
// actually received is the point of this tool. Raw mode drops to the stored
// document as it sits in SQLite.
func (m Model) captureContent() string {
	if m.selectedCapture == nil {
		return ""
	}
	if m.captureRaw || m.captureAgentView == "" {
		return m.selectedCapture.Content
	}
	return m.captureAgentView
}

// setError records an error message and clears the status message.
func (m *Model) setError(err error) {
	if err == nil {
		m.errorMsg = ""
		return
	}
	m.errorMsg = err.Error()
	m.statusMsg = ""
}

// setStatus records a status message and clears any error.
func (m *Model) setStatus(msg string) {
	m.statusMsg = msg
	if msg != "" {
		m.errorMsg = ""
	}
}

// Cursor movement helpers.

func clampCursor(cursor, size int) int {
	if size <= 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= size {
		return size - 1
	}
	return cursor
}

func moveCursor(cursor, delta, size int) int {
	return clampCursor(cursor+delta, size)
}

// adjustScroll updates the scroll offset so cursor remains within [scroll, scroll+visibleCount).
func adjustScroll(cursor, scroll, visibleCount int) int {
	if cursor < scroll {
		return cursor
	}
	if cursor >= scroll+visibleCount {
		return cursor - visibleCount + 1
	}
	return scroll
}

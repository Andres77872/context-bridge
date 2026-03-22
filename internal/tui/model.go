package tui

import (
	"strings"

	"context-bridge/internal/store"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

const dashboardLimit = 100

// Screen represents the currently active TUI screen.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenSession
	ScreenCapture
	ScreenSearch
	ScreenSearchResults
)

func (s Screen) String() string {
	switch s {
	case ScreenDashboard:
		return "Dashboard"
	case ScreenSession:
		return "Session"
	case ScreenCapture:
		return "Capture"
	case ScreenSearch:
		return "Search"
	case ScreenSearchResults:
		return "Search Results"
	default:
		return "Unknown"
	}
}

// confirmAction represents the action to perform after confirmation.
type confirmAction int

const confirmNone confirmAction = iota

// Model is the root Bubble Tea model for the TUI.
type Model struct {
	store *store.Store

	screen       Screen
	prevScreen   Screen
	searchOrigin Screen

	width  int
	height int

	// Loading and messaging state.
	loading   bool
	errorMsg  string
	statusMsg string
	spinner   spinner.Model

	// Stats loaded once at Init.
	stats store.StoreStats

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

	// Search screen state.
	searchInput         textinput.Model
	searchScope         string
	searchResults       []store.SearchResult
	searchCursor        int
	searchResultsScroll int
	searchQuery         string
	searchErr           string

	// Confirm layer state (infrastructure for future destructive actions).
	confirmActive bool
	confirmMsg    string
	confirmAction confirmAction

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
	record *store.CaptureRecord
	err    error
}

type searchLoadedMsg struct {
	sessionID string
	query     string
	results   []store.SearchResult
	err       error
}

// New initialises the TUI model. The search input starts unfocused; it is
// focused only when entering ScreenSearch.
func New(st *store.Store) Model {
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
		screen:          ScreenDashboard,
		prevScreen:      ScreenDashboard,
		searchOrigin:    ScreenDashboard,
		searchInput:     searchInput,
		filterInput:     filterInput,
		spinner:         sp,
		contentViewport: viewport.New(0, 0),
	}
}

// Run starts the TUI program with alt-screen mode.
func Run(st *store.Store) error {
	program := tea.NewProgram(New(st), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(loadDashboardCmd(m.store), loadStatsCmd(m.store), m.spinner.Tick)
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

func loadCaptureCmd(st *store.Store, sessionID string, seq int) tea.Cmd {
	return func() tea.Msg {
		record, err := st.GetCaptureBySeq(sessionID, seq)
		return captureLoadedMsg{record: record, err: err}
	}
}

func loadSearchCmd(st *store.Store, sessionID, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := st.Search(sessionID, query, 3)
		return searchLoadedMsg{sessionID: sessionID, query: query, results: results, err: err}
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

func (m Model) selectedSearchResult() *store.SearchResult {
	if len(m.searchResults) == 0 || m.searchCursor < 0 || m.searchCursor >= len(m.searchResults) {
		return nil
	}
	return &m.searchResults[m.searchCursor]
}

// filteredCaptures returns the session captures filtered by filterQuery (client-side).
func (m Model) filteredCaptures() []store.CaptureRecord {
	if !m.filterActive || strings.TrimSpace(m.filterQuery) == "" {
		return m.sessionCaptures
	}
	q := strings.ToLower(m.filterQuery)
	var out []store.CaptureRecord
	for _, c := range m.sessionCaptures {
		if strings.Contains(strings.ToLower(c.Agent+" "+c.Description), q) {
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

// syncComponentSize recalculates viewport and input dimensions on window resize.
func (m *Model) syncComponentSize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.ready = true

	inputWidth := m.width - 10
	if inputWidth < 20 {
		inputWidth = 20
	}
	m.searchInput.Width = inputWidth
	m.filterInput.Width = inputWidth

	vpWidth := m.width - 8 // account for contentStyle border + appStyle padding
	if vpWidth < 20 {
		vpWidth = 20
	}
	vpHeight := m.height - 12 // header + meta + desc + help + status + padding
	if vpHeight < 5 {
		vpHeight = 5
	}
	m.contentViewport.Width = vpWidth
	m.contentViewport.Height = vpHeight
	if m.selectedCapture != nil {
		m.contentViewport.SetContent(m.selectedCapture.Content)
	}
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

package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func seedAnalyticsCapture(t *testing.T, st *Store, sessionID, callID, agent, description, content string, capturedAt time.Time) {
	t.Helper()
	if _, err := st.AddCapture(CaptureInput{
		ParentSessionID: sessionID,
		CallID:          callID,
		Agent:           agent,
		Description:     description,
		Content:         content,
		CapturedAt:      capturedAt,
	}); err != nil {
		t.Fatalf("seed capture %s: %v", callID, err)
	}
}

func TestAnalyticsAggregatesUsage(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	seedAnalyticsCapture(t, st, "ses-a", "call-a1", "grep", "find auth", "auth handler body", now.Add(-2*time.Hour))
	seedAnalyticsCapture(t, st, "ses-a", "call-a2", "grep", "find login", "login handler body", now.Add(-3*time.Hour))
	seedAnalyticsCapture(t, st, "ses-a", "call-a3", "explore", "map routes", "route table body", now.Add(-30*time.Hour))
	seedAnalyticsCapture(t, st, "ses-b", "call-b1", "executor", "run tests", "test output body", now.Add(-4*24*time.Hour))

	analytics, err := st.AnalyticsContext(context.Background(), 30)
	if err != nil {
		t.Fatalf("AnalyticsContext: %v", err)
	}

	if analytics.Sessions != 2 {
		t.Fatalf("expected 2 root sessions, got %d", analytics.Sessions)
	}
	if analytics.ActiveSessions != 2 {
		t.Fatalf("expected 2 active sessions, got %d", analytics.ActiveSessions)
	}
	if analytics.Captures != 4 {
		t.Fatalf("expected 4 captures, got %d", analytics.Captures)
	}
	if analytics.Bytes <= 0 {
		t.Fatalf("expected positive byte total, got %d", analytics.Bytes)
	}
	if analytics.Captures24h != 2 {
		t.Fatalf("expected 2 captures in the last 24h, got %d", analytics.Captures24h)
	}
	if analytics.Captures7d != 4 {
		t.Fatalf("expected 4 captures in the last 7d, got %d", analytics.Captures7d)
	}
	if analytics.AvgCaptureBytes <= 0 {
		t.Fatalf("expected positive average capture size, got %d", analytics.AvgCaptureBytes)
	}
	if analytics.MedianCaptureBytes <= 0 || analytics.P95CaptureBytes <= 0 {
		t.Fatalf("expected byte percentiles to be populated, got median=%d p95=%d", analytics.MedianCaptureBytes, analytics.P95CaptureBytes)
	}
	if analytics.RetentionDays != RetentionWindowDays {
		t.Fatalf("expected retention %d, got %d", RetentionWindowDays, analytics.RetentionDays)
	}

	if len(analytics.Agents) != 3 {
		t.Fatalf("expected 3 agents, got %d (%+v)", len(analytics.Agents), analytics.Agents)
	}
	if analytics.Agents[0].Agent != "grep" || analytics.Agents[0].Captures != 2 {
		t.Fatalf("expected grep to lead with 2 captures, got %+v", analytics.Agents[0])
	}
	if analytics.Agents[0].Sessions != 1 {
		t.Fatalf("expected grep to span 1 session, got %d", analytics.Agents[0].Sessions)
	}
	if analytics.Agents[0].LastCapturedAt.IsZero() {
		t.Fatal("expected agent last capture time to be populated")
	}

	if len(analytics.Daily) != 30 {
		t.Fatalf("expected 30 dense daily buckets, got %d", len(analytics.Daily))
	}
	var dailyTotal int
	for i, bucket := range analytics.Daily {
		dailyTotal += bucket.Captures
		if i > 0 && !bucket.Day.After(analytics.Daily[i-1].Day) {
			t.Fatalf("expected daily buckets in ascending order at index %d", i)
		}
	}
	if dailyTotal != 4 {
		t.Fatalf("expected daily buckets to cover 4 captures, got %d", dailyTotal)
	}

	if len(analytics.Hourly) != 24 {
		t.Fatalf("expected 24 hourly buckets, got %d", len(analytics.Hourly))
	}
	var hourlyTotal int
	for _, bucket := range analytics.Hourly {
		hourlyTotal += bucket.Captures
	}
	if hourlyTotal != 4 {
		t.Fatalf("expected hourly buckets to cover 4 captures, got %d", hourlyTotal)
	}

	if len(analytics.Heatmap) != 7*24 {
		t.Fatalf("expected a dense 7x24 heatmap, got %d cells", len(analytics.Heatmap))
	}
	var heatTotal int
	for _, cell := range analytics.Heatmap {
		heatTotal += cell.Captures
	}
	if heatTotal != 4 {
		t.Fatalf("expected heatmap to cover 4 captures, got %d", heatTotal)
	}

	if len(analytics.TopSessions) != 2 {
		t.Fatalf("expected 2 top sessions, got %d", len(analytics.TopSessions))
	}
	if analytics.TopSessions[0].ID != "ses-a" || analytics.TopSessions[0].Captures != 3 {
		t.Fatalf("expected ses-a to lead with 3 captures, got %+v", analytics.TopSessions[0])
	}
	if analytics.TopSessions[0].Agents != 2 {
		t.Fatalf("expected ses-a to span 2 agents, got %d", analytics.TopSessions[0].Agents)
	}

	var bucketTotal int
	for _, bucket := range analytics.SizeBuckets {
		bucketTotal += bucket.Captures
	}
	if bucketTotal != 4 {
		t.Fatalf("expected size buckets to cover 4 captures, got %d", bucketTotal)
	}

	if analytics.LargestCapture == nil {
		t.Fatal("expected a largest capture reference")
	}
	if analytics.LargestCapture.Bytes <= 0 || analytics.LargestCapture.SessionID == "" {
		t.Fatalf("expected a populated largest capture, got %+v", analytics.LargestCapture)
	}
	if analytics.DiskBytes <= 0 {
		t.Fatalf("expected positive disk usage, got %d", analytics.DiskBytes)
	}
	if analytics.FirstCapturedAt.IsZero() || analytics.LastCapturedAt.IsZero() {
		t.Fatal("expected first and last capture timestamps")
	}
}

func TestAnalyticsOnEmptyStore(t *testing.T) {
	st := openTestStore(t)

	analytics, err := st.AnalyticsContext(context.Background(), 0)
	if err != nil {
		t.Fatalf("AnalyticsContext: %v", err)
	}
	if analytics.WindowDays != RetentionWindowDays {
		t.Fatalf("expected default window %d, got %d", RetentionWindowDays, analytics.WindowDays)
	}
	if analytics.Captures != 0 || analytics.Bytes != 0 {
		t.Fatalf("expected zero totals, got %+v", analytics)
	}
	if analytics.LargestCapture != nil {
		t.Fatalf("expected no largest capture, got %+v", analytics.LargestCapture)
	}
	if len(analytics.Daily) != RetentionWindowDays {
		t.Fatalf("expected dense daily buckets even when empty, got %d", len(analytics.Daily))
	}
	if analytics.Agents == nil || analytics.TopSessions == nil {
		t.Fatal("expected non-nil empty slices so JSON encodes as []")
	}
}

func TestAnalyticsWindowClampsToRetention(t *testing.T) {
	st := openTestStore(t)

	analytics, err := st.AnalyticsContext(context.Background(), 900)
	if err != nil {
		t.Fatalf("AnalyticsContext: %v", err)
	}
	if analytics.WindowDays != RetentionWindowDays {
		t.Fatalf("expected window clamped to %d, got %d", RetentionWindowDays, analytics.WindowDays)
	}

	analytics, err = st.AnalyticsContext(context.Background(), 7)
	if err != nil {
		t.Fatalf("AnalyticsContext: %v", err)
	}
	if analytics.WindowDays != 7 || len(analytics.Daily) != 7 {
		t.Fatalf("expected a 7 day window, got %d days and %d buckets", analytics.WindowDays, len(analytics.Daily))
	}
}

func TestAnalyticsExcludesDeletedSessions(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-keep", "call-keep", "grep", "keep", "keep body", now)
	seedAnalyticsCapture(t, st, "ses-drop", "call-drop", "grep", "drop", "drop body", now)

	if err := st.MarkSessionDeleted("ses-drop"); err != nil {
		t.Fatalf("MarkSessionDeleted: %v", err)
	}

	analytics, err := st.AnalyticsContext(context.Background(), 30)
	if err != nil {
		t.Fatalf("AnalyticsContext: %v", err)
	}
	if analytics.Captures != 1 {
		t.Fatalf("expected deleted session captures excluded, got %d", analytics.Captures)
	}
	if analytics.Sessions != 1 {
		t.Fatalf("expected 1 live session, got %d", analytics.Sessions)
	}
	if analytics.DeletedSessions != 1 {
		t.Fatalf("expected 1 deleted session, got %d", analytics.DeletedSessions)
	}
}

func TestSessionDetailReportsUsage(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	if err := st.EnsureSession("ses-detail", ""); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := st.EnsureSession("ses-child", "ses-detail"); err != nil {
		t.Fatalf("EnsureSession child: %v", err)
	}
	seedAnalyticsCapture(t, st, "ses-detail", "call-d1", "grep", "one", "first body", now.Add(-2*time.Hour))
	seedAnalyticsCapture(t, st, "ses-detail", "call-d2", "explore", "two", "second body that is longer", now.Add(-1*time.Hour))

	detail, err := st.SessionDetailContext(context.Background(), "ses-detail")
	if err != nil {
		t.Fatalf("SessionDetailContext: %v", err)
	}
	if detail.Summary.CaptureCount != 2 {
		t.Fatalf("expected 2 captures, got %d", detail.Summary.CaptureCount)
	}
	if detail.Bytes <= 0 || detail.LargestBytes <= 0 {
		t.Fatalf("expected byte totals, got %+v", detail)
	}
	if len(detail.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(detail.Agents))
	}
	if detail.ChildSessions != 1 {
		t.Fatalf("expected 1 child session, got %d", detail.ChildSessions)
	}
	if detail.FirstCapturedAt.IsZero() || detail.LastCapturedAt.IsZero() {
		t.Fatal("expected capture time range")
	}
	if detail.Summary.AgentCount != 2 {
		t.Fatalf("expected agent count 2, got %d", detail.Summary.AgentCount)
	}

	// Child sessions resolve to their root.
	childDetail, err := st.SessionDetailContext(context.Background(), "ses-child")
	if err != nil {
		t.Fatalf("SessionDetailContext(child): %v", err)
	}
	if childDetail.Summary.ID != "ses-detail" {
		t.Fatalf("expected child to resolve to root, got %s", childDetail.Summary.ID)
	}
}

func TestSessionDetailReturnsErrorForUnknownSession(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.SessionDetailContext(context.Background(), "ses-missing"); err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}

func TestListRootSessionsContextFiltersAndSorts(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-alpha", "call-1", "grep", "alpha", "alpha body", now.Add(-3*time.Hour))
	seedAnalyticsCapture(t, st, "ses-beta", "call-2", "explore", "beta", "beta body with more content", now.Add(-2*time.Hour))
	seedAnalyticsCapture(t, st, "ses-beta", "call-3", "explore", "beta two", "beta body two", now.Add(-1*time.Hour))

	all, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListRootSessionsContext: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(all))
	}
	if all[0].ID != "ses-beta" {
		t.Fatalf("expected most recent session first, got %s", all[0].ID)
	}
	if all[0].Bytes <= 0 || all[0].AgentCount != 1 {
		t.Fatalf("expected usage columns to be populated, got %+v", all[0])
	}
	if all[0].FirstCapturedAt.IsZero() {
		t.Fatal("expected first capture timestamp")
	}

	byCaptures, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10, Sort: SessionSortCaptures})
	if err != nil {
		t.Fatalf("sort by captures: %v", err)
	}
	if byCaptures[0].ID != "ses-beta" || byCaptures[0].CaptureCount != 2 {
		t.Fatalf("expected ses-beta first by captures, got %+v", byCaptures[0])
	}

	filtered, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10, Query: "alpha"})
	if err != nil {
		t.Fatalf("filter by id: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "ses-alpha" {
		t.Fatalf("expected only ses-alpha, got %+v", filtered)
	}

	byAgent, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10, Agent: "explore"})
	if err != nil {
		t.Fatalf("filter by agent: %v", err)
	}
	if len(byAgent) != 1 || byAgent[0].ID != "ses-beta" {
		t.Fatalf("expected only ses-beta for agent explore, got %+v", byAgent)
	}

	if _, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Sort: "nonsense"}); err == nil {
		t.Fatal("expected an error for an unsupported sort key")
	}
	if _, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Agent: "bad agent!"}); err == nil {
		t.Fatal("expected an error for an invalid agent filter")
	}
}

func TestListRootSessionsContextIncludeDeleted(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-live", "call-live", "grep", "live", "live body", now)
	seedAnalyticsCapture(t, st, "ses-gone", "call-gone", "grep", "gone", "gone body", now)
	if err := st.MarkSessionDeleted("ses-gone"); err != nil {
		t.Fatalf("MarkSessionDeleted: %v", err)
	}

	live, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("expected deleted sessions hidden by default, got %d", len(live))
	}

	withDeleted, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("list including deleted: %v", err)
	}
	if len(withDeleted) != 2 {
		t.Fatalf("expected 2 sessions when including deleted, got %d", len(withDeleted))
	}
}

func TestListRootSessionsQueryFilterTreatsWildcardsLiterally(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-plain", "call-plain", "grep", "plain", "plain body", now)

	results, err := st.ListRootSessionsContext(context.Background(), SessionListOptions{Limit: 10, Query: "%"})
	if err != nil {
		t.Fatalf("wildcard query: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected LIKE wildcards to be escaped, got %d sessions", len(results))
	}
}

func TestSearchContextAcrossAllSessions(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-one", "call-1", "grep", "first", "shared marker in session one", now.Add(-2*time.Hour))
	seedAnalyticsCapture(t, st, "ses-two", "call-2", "explore", "second", "shared marker in session two", now.Add(-1*time.Hour))
	seedAnalyticsCapture(t, st, "ses-two", "call-3", "executor", "third", "unrelated content", now)

	for _, mode := range []SearchMode{SearchModeRegex, SearchModeFTS5} {
		t.Run(string(mode), func(t *testing.T) {
			results, err := st.SearchContext(context.Background(), "marker", SearchOptions{Mode: mode, MaxResults: 10, MaxMatches: 100, MaxCandidates: 100})
			if err != nil {
				t.Fatalf("SearchContext: %v", err)
			}
			if len(results) != 2 {
				t.Fatalf("expected 2 cross-session results, got %d", len(results))
			}
			sessions := map[string]bool{}
			for _, result := range results {
				sessions[result.Capture.SessionID] = true
			}
			if !sessions["ses-one"] || !sessions["ses-two"] {
				t.Fatalf("expected both sessions represented, got %v", sessions)
			}

			scoped, err := st.SearchContext(context.Background(), "marker", SearchOptions{SessionID: "ses-one", Mode: mode, MaxResults: 10})
			if err != nil {
				t.Fatalf("scoped SearchContext: %v", err)
			}
			if len(scoped) != 1 || scoped[0].Capture.SessionID != "ses-one" {
				t.Fatalf("expected a single scoped result, got %+v", scoped)
			}

			byAgent, err := st.SearchContext(context.Background(), "marker", SearchOptions{Agent: "explore", Mode: mode, MaxResults: 10})
			if err != nil {
				t.Fatalf("agent filtered SearchContext: %v", err)
			}
			if len(byAgent) != 1 || byAgent[0].Capture.Agent != "explore" {
				t.Fatalf("expected only explore results, got %+v", byAgent)
			}
		})
	}
}

func TestSearchContextGlobalSkipsDeletedSessions(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-live", "call-live", "grep", "live", "needle here", now)
	seedAnalyticsCapture(t, st, "ses-dead", "call-dead", "grep", "dead", "needle here too", now)
	if err := st.MarkSessionDeleted("ses-dead"); err != nil {
		t.Fatalf("MarkSessionDeleted: %v", err)
	}

	results, err := st.SearchContext(context.Background(), "needle", SearchOptions{Mode: SearchModeRegex, MaxResults: 10})
	if err != nil {
		t.Fatalf("SearchContext: %v", err)
	}
	if len(results) != 1 || results[0].Capture.SessionID != "ses-live" {
		t.Fatalf("expected only the live session, got %+v", results)
	}
}

func TestSearchContextRejectsInvalidAgentFilter(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.SearchContext(context.Background(), "needle", SearchOptions{Agent: "bad agent"}); err == nil {
		t.Fatal("expected an error for an invalid agent filter")
	}
}

func TestSearchContextRespectsMaxResultsGlobally(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		seedAnalyticsCapture(t, st, fmt.Sprintf("ses-%d", i), fmt.Sprintf("call-%d", i), "grep", "row", "needle row", now.Add(-time.Duration(i)*time.Minute))
	}

	results, err := st.SearchContext(context.Background(), "needle", SearchOptions{Mode: SearchModeRegex, MaxResults: 3, MaxCandidates: 100})
	if err != nil {
		t.Fatalf("SearchContext: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestListAgentsContextReturnsSortedAgents(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-a", "call-1", "grep", "a", "body", now)
	seedAnalyticsCapture(t, st, "ses-a", "call-2", "explore", "b", "body", now)
	seedAnalyticsCapture(t, st, "ses-a", "call-3", "explore", "c", "body", now)

	agents, err := st.ListAgentsContext(context.Background())
	if err != nil {
		t.Fatalf("ListAgentsContext: %v", err)
	}
	if len(agents) != 2 || agents[0] != "explore" || agents[1] != "grep" {
		t.Fatalf("expected sorted [explore grep], got %v", agents)
	}
}

func TestStorePathAndDiskUsage(t *testing.T) {
	st := openTestStore(t)
	if st.Path() == "" {
		t.Fatal("expected the store to remember its path")
	}
	usage, err := st.DiskUsage()
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if usage <= 0 {
		t.Fatalf("expected positive disk usage, got %d", usage)
	}
}

func TestListCapturesPageFiltersAndPages(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()
	seedAnalyticsCapture(t, st, "ses-page", "call-1", "grep", "alpha description", "alpha body", now.Add(-4*time.Minute))
	seedAnalyticsCapture(t, st, "ses-page", "call-2", "explore", "beta description", "beta body", now.Add(-3*time.Minute))
	seedAnalyticsCapture(t, st, "ses-page", "call-3", "explore", "gamma description", "gamma body", now.Add(-2*time.Minute))

	page, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Limit: 2})
	if err != nil {
		t.Fatalf("ListCapturesPageContext: %v", err)
	}
	if page.Total != 3 || page.Filtered != 3 {
		t.Fatalf("expected totals 3/3, got %d/%d", page.Total, page.Filtered)
	}
	if len(page.Captures) != 2 {
		t.Fatalf("expected 2 captures on the page, got %d", len(page.Captures))
	}
	if page.Captures[0].Seq != 1 {
		t.Fatalf("expected ascending order by default, got seq %d", page.Captures[0].Seq)
	}
	if len(page.Agents) != 2 {
		t.Fatalf("expected 2 distinct agents, got %v", page.Agents)
	}

	newest, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Limit: 1, Newest: true})
	if err != nil {
		t.Fatalf("newest page: %v", err)
	}
	if newest.Captures[0].Seq != 3 {
		t.Fatalf("expected newest first, got seq %d", newest.Captures[0].Seq)
	}

	offset, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("offset page: %v", err)
	}
	if len(offset.Captures) != 1 || offset.Captures[0].Seq != 3 {
		t.Fatalf("expected the tail page, got %+v", offset.Captures)
	}

	byAgent, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Agent: "explore"})
	if err != nil {
		t.Fatalf("agent filter: %v", err)
	}
	if byAgent.Filtered != 2 || byAgent.Total != 3 {
		t.Fatalf("expected filtered 2 of 3, got %d of %d", byAgent.Filtered, byAgent.Total)
	}

	byText, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Query: "gamma"})
	if err != nil {
		t.Fatalf("text filter: %v", err)
	}
	if byText.Filtered != 1 || byText.Captures[0].Description != "gamma description" {
		t.Fatalf("expected the gamma capture, got %+v", byText.Captures)
	}

	bySeq, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Query: "2"})
	if err != nil {
		t.Fatalf("seq filter: %v", err)
	}
	if bySeq.Filtered != 1 || bySeq.Captures[0].Seq != 2 {
		t.Fatalf("expected seq 2 to match the numeric filter, got %+v", bySeq.Captures)
	}

	if _, err := st.ListCapturesPageContext(context.Background(), CaptureListOptions{SessionID: "ses-page", Limit: -1}); err == nil {
		t.Fatal("expected an error for a negative limit")
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/store"

	mcpapi "github.com/mark3labs/mcp-go/mcp"
)

func TestContextBridgeToolListsOutputsAndSupportsAgentFilter(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-1",
		callID:         "call-1",
		agent:          "grep",
		description:    "Map codebase",
		content:        "grep body",
	})
	seedCapture(t, st, "ses-root", 2, time.Date(2026, 3, 22, 8, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-2",
		callID:         "call-2",
		agent:          "explore",
		description:    "Verify architecture",
		content:        "explore body",
	})

	srv := New(st, "test", store.SearchModeRegex)
	resp := callTool(t, srv, "list", map[string]any{
		"session_id": "ses-child-2",
		"agent":      "grep",
	})

	if resp.IsError {
		t.Fatalf("expected success, got error text %q", resp.Text)
	}
	for _, want := range []string{
		"## Session Context — 1 subagent outputs",
		"Root session: `ses-root`",
		"| 1 | grep | Map codebase |",
		"**[#1] grep** — Map codebase",
	} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("expected text to contain %q, got:\n%s", want, resp.Text)
		}
	}
	if strings.Contains(resp.Text, "Verify architecture") {
		t.Fatalf("agent filter should exclude explore output, got:\n%s", resp.Text)
	}
}

func TestContextBridgeReadToolReturnsFullOutput(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-2",
		agent:          "executor",
		description:    "Implement tests",
		content:        "full output body",
	})

	srv := New(st, "test", store.SearchModeRegex)
	resp := callTool(t, srv, "read", map[string]any{
		"session_id": "ses-child",
		"output":     1,
	})

	if resp.IsError {
		t.Fatalf("expected success, got error text %q", resp.Text)
	}
	for _, want := range []string{
		"## Output #1: [executor] Implement tests",
		"**Time**: 2026-03-22T09:00:00Z",
		"full output body",
	} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("expected text to contain %q, got:\n%s", want, resp.Text)
		}
	}
}

func TestContextBridgeSearchToolReturnsMatches(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "explore",
		description:    "Search auth bug",
		content:        "before line\nauth bug appears here\nafter line",
	})

	srv := New(st, "test", store.SearchModeRegex)
	resp := callTool(t, srv, "search", map[string]any{
		"session_id":    "ses-child",
		"query":         "auth bug",
		"context_lines": 1,
	})

	if resp.IsError {
		t.Fatalf("expected success, got error text %q", resp.Text)
	}
	for _, want := range []string{
		"## Search: \"auth bug\"",
		"2 match(es) across 1 outputs.",
		"Use `read` with `session_id=\"ses-root\"` and the output # to read full content.",
		"### #1 [explore] Search auth bug",
		">>>",
	} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("expected text to contain %q, got:\n%s", want, resp.Text)
		}
	}
}

func TestContextBridgeSearchFTS5Mode(t *testing.T) {
	st := openTestStore(t)
	seedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "explore",
		description:    "Find issue",
		content:        "before line\nfts5word appears\nafter line",
	})

	srv := New(st, "test", store.SearchModeFTS5)
	resp := callTool(t, srv, "search", map[string]any{
		"session_id":    "ses-child",
		"query":         "fts5word",
		"context_lines": 1,
	})

	if resp.IsError {
		t.Fatalf("expected success, got error text %q", resp.Text)
	}
	for _, want := range []string{
		"## Search: \"fts5word\"",
		"match(es)",
		"### #1 [explore] Find issue",
	} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("expected text to contain %q, got:\n%s", want, resp.Text)
		}
	}
}

func TestSearchDescriptionReflectsMode(t *testing.T) {
	tests := []struct {
		mode     store.SearchMode
		contains string
	}{
		{store.SearchModeRegex, "regex"},
		{store.SearchModeFTS5, "FTS5"},
	}

	for _, tc := range tests {
		desc := searchDescription(tc.mode)
		if !strings.Contains(desc, tc.contains) {
			t.Fatalf("mode %q description should contain %q, got %q", tc.mode, tc.contains, desc)
		}
	}
}

func TestServerInstructionsReflectsMode(t *testing.T) {
	tests := []struct {
		mode        store.SearchMode
		wantContain string
		wantExclude string
	}{
		{
			mode:        store.SearchModeRegex,
			wantContain: "regex",
			wantExclude: "FTS5",
		},
		{
			mode:        store.SearchModeFTS5,
			wantContain: "FTS5",
			wantExclude: "regex",
		},
	}

	for _, tc := range tests {
		instr := serverInstructions(tc.mode)
		if !strings.Contains(instr, tc.wantContain) {
			t.Errorf("mode %q: instructions should contain %q, got:\n%s", tc.mode, tc.wantContain, instr)
		}
		if strings.Contains(instr, tc.wantExclude) && tc.wantExclude != "" {
			t.Errorf("mode %q: instructions should NOT contain %q, got:\n%s", tc.mode, tc.wantExclude, instr)
		}
	}
}

func TestSearchQueryHintReflectsMode(t *testing.T) {
	tests := []struct {
		mode        store.SearchMode
		wantContain string
		wantExclude string
	}{
		{
			mode:        store.SearchModeRegex,
			wantContain: "regex",
			wantExclude: "FTS5",
		},
		{
			mode:        store.SearchModeFTS5,
			wantContain: "FTS5",
			wantExclude: "regex",
		},
	}

	for _, tc := range tests {
		hint := searchQueryHint(tc.mode)
		if !strings.Contains(hint, tc.wantContain) {
			t.Errorf("mode %q: hint should contain %q, got:\n%s", tc.mode, tc.wantContain, hint)
		}
		if strings.Contains(hint, tc.wantExclude) && tc.wantExclude != "" {
			t.Errorf("mode %q: hint should NOT contain %q, got:\n%s", tc.mode, tc.wantExclude, hint)
		}
	}
}

func TestMCPToolDescriptionsAreModeSpecific(t *testing.T) {
	st := openTestStore(t)

	regexSrv := New(st, "test", store.SearchModeRegex)
	fts5Srv := New(st, "test", store.SearchModeFTS5)

	regexToolsResp := callListTools(t, regexSrv)
	fts5ToolsResp := callListTools(t, fts5Srv)

	regexSearchDesc := findToolDescription(regexToolsResp, "search")
	fts5SearchDesc := findToolDescription(fts5ToolsResp, "search")

	if regexSearchDesc == "" {
		t.Fatal("search tool description empty for regex mode")
	}
	if fts5SearchDesc == "" {
		t.Fatal("search tool description empty for FTS5 mode")
	}

	if !strings.Contains(regexSearchDesc, "regex") {
		t.Errorf("regex mode search tool description should contain 'regex', got: %q", regexSearchDesc)
	}
	if strings.Contains(regexSearchDesc, "FTS5") {
		t.Errorf("regex mode search tool description should NOT contain 'FTS5', got: %q", regexSearchDesc)
	}

	if !strings.Contains(fts5SearchDesc, "FTS5") {
		t.Errorf("FTS5 mode search tool description should contain 'FTS5', got: %q", fts5SearchDesc)
	}
	if strings.Contains(fts5SearchDesc, "regex") {
		t.Errorf("FTS5 mode search tool description should NOT contain 'regex', got: %q", fts5SearchDesc)
	}
}

func TestMCPSearchToolHasNoEngineField(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, "test", store.SearchModeRegex)

	toolsResp := callListTools(t, srv)
	props := findToolSchema(toolsResp, "search")

	if props == nil {
		t.Fatal("search tool properties not found in tools/list response")
	}

	for paramName := range props {
		if paramName == "engine" || paramName == "mode" {
			t.Errorf("search tool schema should NOT have caller-visible engine selector field %q", paramName)
		}
	}
}

type toolsListResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	} `json:"result"`
}

func callListTools(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcpapi.JSONRPCMessage
}) toolsListResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": mcpapi.JSONRPC_VERSION,
		"id":      1,
		"method":  "tools/list",
	})
	if err != nil {
		t.Fatalf("marshal tools/list request: %v", err)
	}

	message := srv.HandleMessage(context.Background(), payload)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal tools/list response: %v", err)
	}

	var resp toolsListResponse
	if err := json.Unmarshal(encoded, &resp); err != nil {
		t.Fatalf("decode tools/list response: %v\npayload: %s", err, string(encoded))
	}
	return resp
}

func findToolDescription(resp toolsListResponse, name string) string {
	for _, tool := range resp.Result.Tools {
		if tool.Name == name {
			return tool.Description
		}
	}
	return ""
}

func findToolSchema(resp toolsListResponse, name string) map[string]any {
	for _, tool := range resp.Result.Tools {
		if tool.Name == name {
			if props, ok := tool.InputSchema["properties"].(map[string]any); ok {
				return props
			}
		}
	}
	return nil
}

type toolResponse struct {
	Text    string
	IsError bool
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type seededCapture struct {
	childSessionID string
	callID         string
	agent          string
	description    string
	content        string
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mcp.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedCapture(t *testing.T, st *store.Store, sessionID string, seq int, capturedAt time.Time, capture seededCapture) {
	t.Helper()
	record, err := st.AddCapture(store.CaptureInput{
		ParentSessionID: sessionID,
		ChildSessionID:  capture.childSessionID,
		CallID:          capture.callID,
		Agent:           capture.agent,
		Description:     capture.description,
		Content:         capture.content,
		CapturedAt:      capturedAt,
	})
	if err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if record.Seq != seq {
		t.Fatalf("expected seq %d, got %d", seq, record.Seq)
	}
}

func callTool(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcpapi.JSONRPCMessage
}, name string, args map[string]any) toolResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": mcpapi.JSONRPC_VERSION,
		"id":      1,
		"method":  string(mcpapi.MethodToolsCall),
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatalf("marshal rpc request: %v", err)
	}

	message := srv.HandleMessage(context.Background(), payload)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal rpc response: %v", err)
	}

	var resp rpcResponse
	if err := json.Unmarshal(encoded, &resp); err != nil {
		t.Fatalf("decode rpc response: %v\npayload: %s", err, string(encoded))
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", *resp.Error)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("expected tool content, got: %s", string(encoded))
	}
	return toolResponse{Text: resp.Result.Content[0].Text, IsError: resp.Result.IsError}
}

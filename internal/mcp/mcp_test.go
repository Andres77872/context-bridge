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
	seedImportedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 8, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-1",
		callID:         "call-1",
		agent:          "grep",
		description:    "Map codebase",
		content:        "grep body",
	})
	seedImportedCapture(t, st, "ses-root", 2, time.Date(2026, 3, 22, 8, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child-2",
		callID:         "call-2",
		agent:          "explore",
		description:    "Verify architecture",
		content:        "explore body",
	})

	srv := New(st, "test")
	resp := callTool(t, srv, "context_bridge", map[string]any{
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
	seedImportedCapture(t, st, "ses-root", 2, time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-2",
		agent:          "executor",
		description:    "Implement tests",
		content:        "full output body",
	})

	srv := New(st, "test")
	resp := callTool(t, srv, "context_bridge_read", map[string]any{
		"session_id": "ses-child",
		"output":     2,
	})

	if resp.IsError {
		t.Fatalf("expected success, got error text %q", resp.Text)
	}
	for _, want := range []string{
		"## Output #2: [executor] Implement tests",
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
	seedImportedCapture(t, st, "ses-root", 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: "ses-child",
		callID:         "call-1",
		agent:          "explore",
		description:    "Search auth bug",
		content:        "before line\nauth bug appears here\nafter line",
	})

	srv := New(st, "test")
	resp := callTool(t, srv, "context_bridge_search", map[string]any{
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
		"Use `context_bridge_read` with `session_id=\"ses-root\"` and the output # to read full content.",
		"### #1 [explore] Search auth bug",
		">>>",
	} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("expected text to contain %q, got:\n%s", want, resp.Text)
		}
	}
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

func seedImportedCapture(t *testing.T, st *store.Store, sessionID string, seq int, capturedAt time.Time, capture seededCapture) {
	t.Helper()
	_, err := st.ImportCapture(sessionID, store.CaptureInput{
		ParentSessionID: sessionID,
		ChildSessionID:  capture.childSessionID,
		CallID:          capture.callID,
		Agent:           capture.agent,
		Description:     capture.description,
		Content:         capture.content,
		CapturedAt:      capturedAt,
	}, seq, "", capture.description, len(capture.content), false)
	if err != nil {
		t.Fatalf("seed imported capture: %v", err)
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

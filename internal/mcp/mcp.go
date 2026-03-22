package mcp

import (
	"context"
	"fmt"
	"strings"

	"context-bridge/internal/store"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverInstructions = `Context Bridge exposes same-session research history captured from OpenCode subagents.
Use list to list prior outputs, search to search them, and read to read one by output number.`

const timeFormat = "2006-01-02T15:04:05Z07:00"

func New(st *store.Store, version string) *server.MCPServer {
	srv := server.NewMCPServer(
		"context-bridge",
		version,
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)
	registerTools(srv, st)
	return srv
}

func Serve(st *store.Store, version string) error {
	return server.ServeStdio(New(st, version))
}

func registerTools(srv *server.MCPServer, st *store.Store) {
	listTool := mcp.NewTool("list",
		mcp.WithDescription("View the current session's subagent output history with sequence numbers, times, sizes, and previews."),
		mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
		mcp.WithString("agent", mcp.Description("Optional agent filter, for example grep or explore.")),
	)

	readTool := mcp.NewTool("read",
		mcp.WithDescription("Read the full content of one captured output by its output number."),
		mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
		mcp.WithNumber("output", mcp.Required(), mcp.Description("Output number from the hint or list output.")),
	)

	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Search across all captured outputs for the current session context using Regex."),
		mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Regex pattern or text query to search for across stored outputs.")),
		mcp.WithNumber("context_lines", mcp.Description("Optional lines of context around each match. Defaults to 3.")),
	)

	srv.AddTool(listTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID := strings.TrimSpace(req.GetString("session_id", ""))
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required for MCP usage"), nil
		}

		captures, err := st.ListCaptures(sessionID, strings.TrimSpace(req.GetString("agent", "")))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(captures) == 0 {
			return mcp.NewToolResultText("No subagent outputs recorded for this session."), nil
		}

		rootID, err := st.ResolveRoot(sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var rows []string
		for _, capture := range captures {
			rows = append(rows, fmt.Sprintf("| %d | %s | %s | %s | %s |", capture.Seq, capture.Agent, escapeTable(capture.Description), store.FormatRelativeTime(capture.CapturedAt), store.FormatBytes(capture.Bytes)))
		}

		var previews []string
		for _, capture := range captures {
			previewLines := strings.Split(capture.Preview, "\n")
			for i, line := range previewLines {
				previewLines[i] = "> " + line
			}
			previews = append(previews, fmt.Sprintf("**[#%d] %s** — %s\n%s", capture.Seq, capture.Agent, capture.Description, strings.Join(previewLines, "\n")))
		}

		text := strings.Join([]string{
			fmt.Sprintf("## Session Context — %d subagent outputs", len(captures)),
			fmt.Sprintf("Root session: `%s`", rootID),
			"",
			"| # | Agent | Task | Time | Size |",
			"|---|-------|------|------|------|",
			strings.Join(rows, "\n"),
			"",
			"### Previews",
			"",
			strings.Join(previews, "\n\n"),
			"",
			fmt.Sprintf("Use `read` with `session_id=%q` and `output=<number>` to read one output.", rootID),
		}, "\n")

		return mcp.NewToolResultText(text), nil
	})

	srv.AddTool(readTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID := strings.TrimSpace(req.GetString("session_id", ""))
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required for MCP usage"), nil
		}

		output, err := req.RequireInt("output")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		record, err := st.GetCaptureBySeq(sessionID, output)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		text := strings.Join([]string{
			fmt.Sprintf("## Output #%d: [%s] %s", record.Seq, record.Agent, record.Description),
			fmt.Sprintf("**Time**: %s | **Size**: %s", record.CapturedAt.Format(timeFormat), store.FormatBytes(record.Bytes)),
			"",
			"---",
			"",
			record.Content,
		}, "\n")

		return mcp.NewToolResultText(text), nil
	})

	srv.AddTool(searchTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID := strings.TrimSpace(req.GetString("session_id", ""))
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required for MCP usage"), nil
		}

		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		results, err := st.Search(sessionID, query, req.GetInt("context_lines", 3))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(results) == 0 {
			captures, listErr := st.ListCaptures(sessionID, "")
			if listErr != nil {
				return mcp.NewToolResultError(listErr.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("No matches for %q across %d outputs.", query, len(captures))), nil
		}

		rootID, err := st.ResolveRoot(sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var groups []string
		totalMatches := 0
		for _, result := range results {
			totalMatches += result.MatchCount
			groups = append(groups, strings.Join([]string{
				fmt.Sprintf("### #%d [%s] %s", result.Capture.Seq, result.Capture.Agent, result.Capture.Description),
				fmt.Sprintf("%d match(es)", result.MatchCount),
				"",
				result.Snippet,
			}, "\n"))
		}

		text := strings.Join([]string{
			fmt.Sprintf("## Search: %q", query),
			"",
			fmt.Sprintf("%d match(es) across %d outputs.", totalMatches, len(results)),
			fmt.Sprintf("Use `read` with `session_id=%q` and the output # to read full content.", rootID),
			"",
			strings.Join(groups, "\n\n"),
		}, "\n")

		return mcp.NewToolResultText(text), nil
	})
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

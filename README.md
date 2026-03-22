# context-bridge

A Go binary that captures subagent outputs from OpenCode sessions and exposes them via MCP tools and a terminal UI.

## Why context-bridge?

When working with OpenCode, subagents (grep, explore, executor, etc.) produce outputs that get lost after the session ends. context-bridge persists these outputs so you can:

- **Recover prior research** — avoid re-running expensive grep/explore calls
- **Reference past findings** — agents can read previous outputs via MCP tools
- **Browse offline** — TUI lets you inspect captured data without OpenCode running

## How it works

```
OpenCode session
       │
       ▼
  thin TS plugin ──────► HTTP ──────► context-bridge serve (Go binary)
       │                                      │
       │                                      ▼
       │                              SQLite + FTS5 store
       │                                      │
       ▼                                      ▼
  MCP tools ◄──────────────────────────── context-bridge mcp
       │
       ▼
  Agent reads prior outputs
```

The plugin is a thin HTTP bridge (114 lines, no business logic). The Go binary owns all data persistence and query logic.

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Go | 1.26+ | Build the binary |
| OpenCode | latest | The IDE agent |
| Bun | latest | Runtime for the TypeScript plugin |

Verify:

```bash
go version      # go1.26.x or later
bun --version   # any recent version
which opencode  # or wherever OpenCode is installed
```

## Installation

### 1. Build and install the binary

```bash
cd /path/to/context-bridge
go install ./cmd/context-bridge
```

Verify:

```bash
context-bridge version
# dev (or version number)
```

### 2. Copy the plugin

```bash
mkdir -p ~/.config/opencode/plugins
cp plugin/opencode/context-bridge.ts ~/.config/opencode/plugins/
```

### 3. Configure OpenCode

Add to `~/.config/opencode/opencode.json`:

```jsonc
{
  "mcp": {
    "context-bridge": {
      "enabled": true,
      "type": "local",
      "command": ["context-bridge", "mcp"]
    }
  },
  "permission": {
    "context_bridge": "allow",
    "context_bridge_read": "allow",
    "context_bridge_search": "allow"
  }
}
```

Restart OpenCode to load the MCP server.

### 4. Import existing data (optional)

If you have legacy session data from OpenCode's tool-output:

```bash
context-bridge migrate
# imported N capture(s) from ~/.local/share/opencode/tool-output/sessions
```

### 5. Verify everything works

```bash
# Binary works
context-bridge version

# TUI opens
context-bridge tui

# MCP server starts (press Ctrl+D to exit stdio mode)
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}' | context-bridge mcp
```

## Commands

### `context-bridge serve`

Starts the HTTP server that the TypeScript plugin talks to.

```bash
context-bridge serve
context-bridge serve --addr 127.0.0.1:7438
```

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `CONTEXT_BRIDGE_ADDR` | `127.0.0.1:7438` | HTTP listen address |
| `CONTEXT_BRIDGE_PORT` | `7438` | Port (if ADDR not set) |
| `CONTEXT_BRIDGE_DB` | `~/.local/share/context-bridge/store.db` | SQLite database path |

**Note:** You rarely run this manually. The plugin auto-spawns it when needed.

### `context-bridge mcp`

Starts the MCP stdio server for agent-facing tools. OpenCode manages this automatically via the `mcp` config block.

```bash
context-bridge mcp
```

### `context-bridge tui`

Opens an interactive terminal browser for captured outputs.

```bash
context-bridge tui
```

### `context-bridge migrate`

Imports legacy data from OpenCode's tool-output directory.

```bash
context-bridge migrate
context-bridge migrate --from /path/to/sessions
```

### `context-bridge version`

Prints the binary version.

```bash
context-bridge version
```

## MCP Tools

### `context_bridge`

Lists all captured outputs for a session with sequence numbers, timestamps, sizes, and previews.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes* | OpenCode session ID |
| `agent` | string | no | Filter by agent type (e.g., `grep`, `explore`) |

*Required for MCP usage; the plugin auto-injects this.

**Example:**

```json
{
  "session_id": "ses_abc123",
  "agent": "grep"
}
```

**Output:**

```markdown
## Session Context — 5 subagent outputs
Root session: `ses_abc123`

| # | Agent | Task | Time | Size |
|---|-------|------|------|------|
| 1 | grep | Investigate auth module | 2m ago | 12KB |
| 2 | explore | Check plugin boundary | 5m ago | 8KB |
...

### Previews

**[#1] grep** — Investigate auth module
> ## auth module investigation
> Found 3 files implementing authentication...

Use `context_bridge_read` with `session_id="ses_abc123"` and `output=<number>` to read one output.
```

### `context_bridge_read`

Reads the full content of a specific output by its sequence number.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | OpenCode session ID |
| `output` | number | yes | Output number from the list |

**Example:**

```json
{
  "session_id": "ses_abc123",
  "output": 1
}
```

**Output:**

```markdown
## Output #1: [grep] Investigate auth module
**Time**: 2026-03-22T14:30:00Z | **Size**: 12KB

---

## auth module investigation

### Files Found
...
```

### `context_bridge_search`

Full-text search across all captured outputs for a session.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | OpenCode session ID |
| `query` | string | yes | Search query (FTS5 syntax supported) |
| `context_lines` | number | no | Lines of context around matches (default: 3) |

**Example:**

```json
{
  "session_id": "ses_abc123",
  "query": "authentication JWT",
  "context_lines": 5
}
```

**Output:**

```markdown
## Search: "authentication JWT"

3 match(es) across 2 outputs.
Use `context_bridge_read` with `session_id="ses_abc123"` and the output # to read full content.

### #1 [grep] Investigate auth module
1 match(es)

...authentication logic uses JWT tokens...
     ^^match^^
...verify the JWT signature before...
```

## TUI Browser

Launch with `context-bridge tui`. A read-only terminal interface for browsing captured outputs.

### Screens

| Screen | Description |
|--------|-------------|
| **Dashboard** | List of sessions with stats |
| **Session** | List of outputs for one session |
| **Capture** | Full output content with scroll |
| **Search** | Full-text search input |
| **SearchResults** | Search matches with snippets |

### Key Bindings

| Key | Dashboard | Session | Capture | Search | Results |
|-----|-----------|---------|---------|--------|---------|
| `j` / `↓` | next session | next output | — | — | next result |
| `k` / `↑` | prev session | prev output | — | — | prev result |
| `enter` | open session | open output | — | submit query | open result |
| `/` | open search | open search | open search | — | refine search |
| `f` | — | filter mode | — | — | — |
| `esc` | — | back to dashboard | back | cancel | back |
| `q` | quit | back to dashboard | back | cancel | back |
| `ctrl+c` | quit | quit | quit | quit | quit |
| `pgup`/`pgdn` | — | — | page scroll | — | — |
| `home`/`end` | — | — | top/bottom | — | — |

### Filter Mode (Session screen)

Press `f` to activate inline filtering. Type to filter the output list client-side. Press `esc` or `enter` to exit filter mode.

### Scroll Indicators

When content exceeds the visible window, a footer shows: `showing 1–10 of 45`.

## Plugin Behavior

The plugin at `~/.config/opencode/plugins/context-bridge.ts` is intentionally thin:

**What it does:**
- Forwards OpenCode lifecycle hooks to the Go HTTP server
- Auto-spawns `context-bridge serve` if the server isn't running
- Returns silently (graceful degradation) if the backend is unavailable

**What it does NOT do:**
- No database access
- No search logic
- No data transformation
- No state management

### Hooks

| Hook | Purpose |
|------|---------|
| `session.created` | Register new session in store |
| `session.deleted` | Mark session as deleted (soft delete) |
| `tool.execute.after` | Capture Task tool outputs from subagents |
| `experimental.chat.system.transform` | Inject hint about prior outputs into system prompt |

### Auto-spawn behavior

When the plugin initializes or receives a hook, it:

1. Checks `http://127.0.0.1:7438/health` with 400ms timeout
2. If unreachable, spawns `context-bridge serve` via `Bun.spawn()`
3. Waits 600ms for startup
4. Proceeds with the HTTP call

If spawn fails or the server remains unreachable, the plugin continues silently — no error bubbles up to OpenCode.

### Environment variables (plugin)

| Variable | Default | Description |
|----------|---------|-------------|
| `CONTEXT_BRIDGE_PORT` | `7438` | HTTP port |
| `CONTEXT_BRIDGE_ADDR` | `127.0.0.1:7438` | Full address (overrides PORT) |
| `CONTEXT_BRIDGE_BIN` | `context-bridge` | Path to binary |

## Troubleshooting

### "No sessions captured yet"

The TUI shows this when the database is empty. Make sure:

1. OpenCode is running with the plugin configured
2. You've run tasks that invoke subagents (grep, explore, executor)
3. The plugin can reach the HTTP server

Check the server:

```bash
curl http://127.0.0.1:7438/health
# should return "OK"
```

### Plugin not loading

1. Verify the file exists: `ls ~/.config/opencode/plugins/context-bridge.ts`
2. Check OpenCode logs for plugin errors
3. Ensure Bun is installed: `bun --version`

### MCP server not starting

1. Verify binary is on PATH: `which context-bridge`
2. Test manually: `echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}' | context-bridge mcp`
3. Check OpenCode's MCP logs

### Database permission errors

The database lives at `~/.local/share/context-bridge/store.db`. Ensure the directory exists and is writable:

```bash
mkdir -p ~/.local/share/context-bridge
ls -la ~/.local/share/context-bridge/
```

### Port already in use

If port 7438 is taken, set a different port:

```bash
export CONTEXT_BRIDGE_PORT=7439
# Or in opencode.json, set CONTEXT_BRIDGE_PORT via plugin env
```

### Search returns no results

The search uses FTS5. Try:
- Simpler queries without special characters
- OR syntax: `term1 OR term2`
- Prefix matching: `auth*`

## Architecture

For deep technical details, see [DESIGN.md](./DESIGN.md).

Key components:

| Component | File | Responsibility |
|-----------|------|----------------|
| Store | `internal/store/store.go` | SQLite schema, queries, FTS |
| MCP | `internal/mcp/mcp.go` | Tool definitions, stdio server |
| TUI | `internal/tui/*.go` | Bubble Tea browser |
| HTTP | `internal/server/server.go` | Plugin-to-binary bridge |
| Plugin | `plugin/opencode/context-bridge.ts` | OpenCode hooks, auto-spawn |

## Limitations

- **No cross-session search** — Search is scoped to one session tree
- **No purge/retention** — Data is retained forever (no TTL)
- **No release packaging** — Build from source; no goreleaser yet
- **No destructive actions in TUI** — Delete buttons exist but aren't wired

## Related Projects

- **[Engram](https://github.com/anomalyco/engram)** — Persistent memory system with `mem_*` tools, topics, and cross-session recall
- **OpenCode** — The IDE agent framework this integrates with
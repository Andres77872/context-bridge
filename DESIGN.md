# context-bridge — Technical Design

**Status**: Design  
**Author**: Architecture session  
**Date**: 2026-03-22

---

## 1. Purpose & Scope

`context-bridge` is a **single-binary Go MCP service** that captures subagent task outputs from OpenCode sessions, persists them in a local SQLite store, and exposes them back to agents via three MCP tools.

The system replaces all business logic currently embedded in the TypeScript plugin at `~/.config/opencode/plugins/context-bridge.ts`. A thin TypeScript adapter remains to forward OpenCode lifecycle hooks to MCP — it contains no business logic.

### Non-goals
- No automatic OpenCode configuration or self-registration
- No general long-term memory semantics (that is Engram's job)
- No cloud sync or multi-device storage
- No copying of Engram's product-specific memory ontology (`topic_key`, `mem_*` tools, privacy tags, passive learning, etc.)

---

## 2. Domain Model

The domain is centered on **sessions**, **captures**, and **search**. Everything else is infrastructure around these three concepts.

```
Session
  id           TEXT PK          -- OpenCode session ID (ses_xxx)
  parent_id    TEXT FK           -- NULL for root sessions
  created_at   DATETIME
  deleted_at   DATETIME NULL     -- soft delete, not hard delete

Capture
  id           INTEGER PK AUTOINCREMENT
  session_id   TEXT FK → Session.id   -- root/parent session that owns this capture
  seq          INTEGER               -- per-session monotonic 1..N
  child_session_id TEXT NULL          -- OpenCode child session ID (if known)
  call_id      TEXT                  -- OpenCode tool call ID
  agent        TEXT                  -- subagent type (grep, explore, executor, ...)
  description  TEXT                  -- task description passed to subagent
  content      TEXT                  -- full raw output markdown
  preview      TEXT                  -- first N non-blank lines (pre-extracted)
  bytes        INTEGER
  captured_at  DATETIME

-- FTS5 virtual table for full-text search over content + description
CREATE VIRTUAL TABLE captures_fts USING fts5(
  description,
  content,
  content='captures',
  content_rowid='id'
);
```

### Key domain rules
1. A **root session** is any `Session` with `parent_id IS NULL`.
2. A **capture** always belongs to the root session, not the child session that produced it.
3. `seq` is assigned atomically per root session and is stable for the lifetime of the capture.
4. Soft deletes: when a session is deleted, mark `deleted_at`; retain data until explicit purge or TTL policy (configurable, default: forever).
5. Preview is extracted at ingestion time (first 10 non-blank lines, max 80 chars/line) — no re-scanning at query time.
6. `captures_fts` is kept in sync via SQLite triggers on INSERT and DELETE.

---

## 3. Package Layout

```
context-bridge/
├── cmd/
│   └── context-bridge/
│       └── main.go              # CLI dispatcher
├── internal/
│   ├── store/
│   │   ├── store.go             # DB open, schema migration, all domain operations
│   │   └── store_test.go
│   ├── mcp/
│   │   ├── mcp.go               # NewServer(), tool registration, ServeStdio
│   │   └── handlers.go          # one func per tool, delegates to store
│   ├── tui/
│   │   ├── model.go             # single tea.Model with Screen enum
│   │   ├── update.go            # Update() router by Screen
│   │   └── view.go              # View() router by Screen
├── plugin/
│   └── opencode/
│       └── context-bridge.ts    # thin OpenCode adapter (forward hooks → MCP)
├── go.mod
├── go.sum
├── DESIGN.md                    # this file
└── .goreleaser.yaml             # single-binary release
```

### Package responsibility boundaries

| Package | Owns | Does NOT own |
|---|---|---|
| `internal/store` | SQLite schema, all domain queries, FTS, seq counters, soft-delete policy | MCP protocol, HTTP, UI, hook business logic |
| `internal/mcp` | Tool registration, arg parsing, MCP protocol, hint rendering | DB schema, session graph logic |
| `internal/tui` | Terminal UI, screen states, key routing | Any DB write; reads store only |
| `cmd/context-bridge` | CLI flag parsing, mode dispatch, store init | Any business logic |
| `plugin/opencode/context-bridge.ts` | OpenCode hook binding, MCP transport, system prompt injection | Session graph, persistence, search |

---

## 4. Store API (Core Interface)

```go
// internal/store/store.go

type Store struct { db *sql.DB }

func Open(dbPath string) (*Store, error)
func (s *Store) Close() error

// Session management
func (s *Store) EnsureSession(id, parentID string) error
func (s *Store) ResolveRoot(childID string) (string, error)  // walks parent chain
func (s *Store) MarkSessionDeleted(id string) error

// Capture ingestion
type CaptureInput struct {
    ParentSessionID  string
    ChildSessionID   string
    CallID           string
    Agent            string
    Description      string
    Content          string
    CapturedAt       time.Time
}
type CaptureRecord struct {
    ID             int64
    SessionID      string
    Seq            int
    ChildSessionID string
    CallID         string
    Agent          string
    Description    string
    Preview        string
    Bytes          int
    CapturedAt     time.Time
}
func (s *Store) AddCapture(input CaptureInput) (*CaptureRecord, error)

// Query
func (s *Store) ListCaptures(rootSessionID string) ([]CaptureRecord, error)
func (s *Store) GetCaptureBySeq(rootSessionID string, seq int) (*CaptureRecord, error)
func (s *Store) GetCaptureContent(id int64) (string, error)

// Search
type SearchResult struct {
    CaptureRecord
    Snippet string  // highlighted context lines
}
func (s *Store) Search(rootSessionID string, query string, contextLines int) ([]SearchResult, error)

// Hint
func (s *Store) RenderHint(rootSessionID string) (string, error)

```

### Seq counter strategy
Use a single SQLite transaction:
```sql
SELECT COALESCE(MAX(seq), 0) + 1 FROM captures WHERE session_id = ? FOR UPDATE
```
(SQLite write-locks the DB on write; this is safe under WAL with a single writer.)

### FTS5 trigger pair
```sql
CREATE TRIGGER captures_ai AFTER INSERT ON captures BEGIN
  INSERT INTO captures_fts(rowid, description, content) VALUES (new.id, new.description, new.content);
END;
CREATE TRIGGER captures_ad AFTER DELETE ON captures BEGIN
  INSERT INTO captures_fts(captures_fts, rowid, description, content) VALUES('delete', old.id, old.description, old.content);
END;
```

### Hint rendering (in store, not MCP)
```
## Prior Research Available — READ BEFORE WORKING

There are N prior subagent outputs from this session:
- [#1] [grep] Short task description (2026-03-22, 18.4 KB)
  /abs/path/to/output-or-use-seq-for-lookup
- [#2] [explore] Another task description ...

**REQUIRED**: Use read with the output # to read full content.
Use search to find specific information across all outputs.
```

The hint includes `#seq` numbers so agents can call `read` by number without consulting the list tool first.

---

## 5. MCP Tool Surface

The Go MCP server exposes exactly three tools, preserving current agent-facing names.

### Tool: `list`

**Purpose**: List all captured outputs for the current session context.

```go
type ListArgs struct {
    SessionID string `json:"session_id" jsonschema:"required,description=OpenCode session ID (child or root)"`
    Agent     string `json:"agent,omitempty" jsonschema_description:"Filter by agent type (optional)"`
}
```

**Behavior**:
1. Resolve root session from `session_id` (handles child sessions transparently)
2. Call `store.ListCaptures(rootSessionID)`
3. Return markdown table:
   ```
   | # | Agent   | Task               | Time     | Size   |
   |---|---------|---------------------|----------|--------|
   | 1 | grep    | Deep analysis...    | 5m ago   | 18 KB  |
   ```

---

### Tool: `read`

**Purpose**: Read the full content of one output by its sequence number.

```go
type ReadArgs struct {
    SessionID string `json:"session_id" jsonschema:"required"`
    Seq       int    `json:"seq"        jsonschema:"required,description=Output number from the list or hint"`
}
```

**Behavior**:
1. Resolve root session
2. Call `store.GetCaptureBySeq(rootSessionID, seq)`
3. Return `capture.content` (full markdown)

---

### Tool: `search`

**Purpose**: Full-text search across all outputs in the current session context.

```go
type SearchArgs struct {
    SessionID    string `json:"session_id"    jsonschema:"required"`
    Query        string `json:"query"         jsonschema:"required"`
    ContextLines int    `json:"context_lines,omitempty" jsonschema_description:"Lines of context around matches (default 3)"`
}
```

**Behavior**:
1. Resolve root session
2. Call `store.Search(rootSessionID, query, contextLines)`
3. Return grouped match snippets with capture header (seq, agent, task)

**Search implementation**:
- Regular expression matching over capture content
- Fallback to exact literal match if regex compile fails
- Post-filter: extract matching line ranges with `contextLines` padding
- Max 5 match groups per capture in output to control token cost

---

### HTTP API (Internal)

**Purpose**: Called exclusively by the thin TS plugin to manage captures and get the pre-rendered hint string for a session. The OpenCode plugin uses HTTP `fetch` to talk to the `context-bridge serve` process.

**Endpoints**:
- `POST /events`: Handles `session.created` and `session.deleted`
- `POST /capture`: Ingests a new subagent output
- `GET /hint?session_id=...`: Returns plain text hint block or empty string if no captures exist

These endpoints are NOT exposed to agents.

---

### MCP server bootstrap

```go
// internal/mcp/mcp.go
func NewServer(s *store.Store) *server.MCPServer {
    srv := server.NewMCPServer(
        "context-bridge",
        "0.1.0",
        server.WithToolCapabilities(true),
        server.WithInstructions(serverInstructions),
    )
    registerTools(srv, s)
    return srv
}

func Serve(s *store.Store) error {
    srv := NewServer(s)
    return server.ServeStdio(srv)
}
```

---

## 6. CLI / Binary Interface

```
context-bridge mcp          # Start MCP stdio server (primary mode; used by OpenCode)
context-bridge tui          # Start TUI browser
context-bridge version      # Print version
```

**No `setup` subcommand.** No automatic OpenCode configuration. The user manually:
1. Builds or installs the binary
2. Adds MCP block to `opencode.json`
3. Copies the thin TS plugin to `~/.config/opencode/plugins/`

The binary reads its DB path from:
1. `CONTEXT_BRIDGE_DB` environment variable
2. Default: `~/.local/share/context-bridge/store.db`

---

## 7. Thin TypeScript Plugin

Location: `plugin/opencode/context-bridge.ts`  
Installed manually to: `~/.config/opencode/plugins/context-bridge.ts`

### Responsibilities (only these)

```typescript
const CB_URL = `http://${process.env.CONTEXT_BRIDGE_ADDR ?? "127.0.0.1:7438"}`

export const ContextBridge: Plugin = async (ctx) => {
  return {
    // 1. Register session/parent relationship
    event: async (event) => {
      await fetch(`${CB_URL}/events`, {
        method: "POST",
        body: JSON.stringify(event)
      }).catch(() => {}) // degrade silently
    },

    // 2. Forward Task outputs to HTTP server for capture
    "tool.execute.after": async (input, output) => {
      if (!isTaskTool(input.name)) return
      const content = extractOutputText(output)
      if (!content || content.length < 100) return
      
      await fetch(`${CB_URL}/capture`, {
        method: "POST",
        body: JSON.stringify({
          parent_session_id: input.sessionID,
          child_session_id:  output.metadata?.sessionId ?? null,
          call_id:           input.callID,
          agent:             input.args?.subagent_type ?? input.args?.subagentType ?? "unknown",
          description:       input.args?.description ?? "",
          content,
          captured_at:       new Date().toISOString(),
        })
      }).catch(() => {})
    },

    // 3. Inject hint into child session system prompts
    "experimental.chat.system.transform": async (sessionID, system) => {
      try {
        const res = await fetch(`${CB_URL}/hint?session_id=${sessionID}`)
        if (!res.ok) return system
        const hint = await res.text()
        if (!hint) return system
        return system + "\n\n" + hint
      } catch {
        return system
      }
    },
  }
}
```

### What the plugin does NOT do
- No manifest reading/writing
- No filesystem operations
- No session counter tracking
- No in-memory children/sessions maps
- No search logic
- No preview extraction

### Communication Transport
The plugin communicates with the Go binary via HTTP for lifecycle events and prompt injection. It manages auto-spawning the `context-bridge serve` process if the server is unreachable. OpenCode's MCP client handles the subprocess lifecycle for agent-facing tools via `context-bridge mcp`.

---

## 8. TUI Structure

The TUI is a single `tea.Model` with a `Screen` enum following Engram's structural pattern but with context-bridge-specific screens.

### Screens

```go
type Screen int
const (
    ScreenDashboard   Screen = iota  // recent sessions, capture counts
    ScreenSession                     // list of captures for selected session
    ScreenCapture                     // full content view for single capture
    ScreenSearch                      // search input
    ScreenSearchResults               // search results list
)
```

### Model shape

```go
type Model struct {
    store         *store.Store
    Screen        Screen
    PrevScreen    Screen
    Width, Height int
    Cursor        int
    Scroll        int

    // Dashboard
    RecentSessions []store.SessionSummary

    // Session view
    SelectedSession string
    Captures        []store.CaptureRecord

    // Capture view
    SelectedCapture *store.CaptureRecord
    CaptureContent  string

    // Search
    SearchInput     textinput.Model
    SearchResults   []store.SearchResult

    ErrorMsg   string
    StatusMsg  string
}
```

### Key bindings (universal)
| Key | Action |
|---|---|
| `q` / `ctrl+c` | Quit or go back |
| `enter` | Select / drill into |
| `esc` | Go to PrevScreen |
| `/` | Open search (from any list screen) |
| `j` / `k` / `↓` / `↑` | Cursor navigation |
| `PgDn` / `PgUp` | Scroll in capture view |

### TUI does NOT expose any write operations
The TUI is read-only. It is a browsing tool for existing captured data. No capture creation or deletion from TUI in v1.

---

## 9. SQLite Schema (Full)

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    deleted_at  DATETIME NULL
);

CREATE TABLE IF NOT EXISTS captures (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id       TEXT NOT NULL REFERENCES sessions(id),
    seq              INTEGER NOT NULL,
    child_session_id TEXT NULL,
    call_id          TEXT NOT NULL,
    agent            TEXT NOT NULL DEFAULT 'unknown',
    description      TEXT NOT NULL DEFAULT '',
    content          TEXT NOT NULL,
    preview          TEXT NOT NULL DEFAULT '',
    bytes            INTEGER NOT NULL DEFAULT 0,
    captured_at      DATETIME NOT NULL,
    UNIQUE(session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_captures_session ON captures(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_captures_agent   ON captures(session_id, agent);

CREATE VIRTUAL TABLE IF NOT EXISTS captures_fts USING fts5(
    description,
    content,
    content='captures',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS captures_ai AFTER INSERT ON captures BEGIN
    INSERT INTO captures_fts(rowid, description, content)
    VALUES (new.id, new.description, new.content);
END;

CREATE TRIGGER IF NOT EXISTS captures_ad AFTER DELETE ON captures BEGIN
    INSERT INTO captures_fts(captures_fts, rowid, description, content)
    VALUES ('delete', old.id, old.description, old.content);
END;

CREATE TRIGGER IF NOT EXISTS captures_au AFTER UPDATE ON captures BEGIN
    INSERT INTO captures_fts(captures_fts, rowid, description, content)
    VALUES ('delete', old.id, old.description, old.content);
    INSERT INTO captures_fts(rowid, description, content)
    VALUES (new.id, new.description, new.content);
END;
```

### Schema migration
Use a simple integer version in `schema_version`. On `Open()`, read current version and apply forward-only migration SQL steps. No external migration library needed.

---

## 10. opencode.json Configuration (Manual)

The user must manually add this to `~/.config/opencode/opencode.json`:

```json
{
  "mcp": {
    "context-bridge": {
      "type": "local",
      "command": "context-bridge",
      "args": ["mcp"]
    }
  },
  "permission": {
    "list":        { "allow": true },
    "read":   { "allow": true },
    "search": { "allow": true }
  }
}
```

The internal tools (`capture_output`, `ensure_session`, `mark_session_deleted`, `context_bridge_hint`) are NOT listed in permissions — they should only be accessible to the plugin process via its own MCP client connection.

---

## 11. External Dependencies

| Package | Version | Purpose |
|---|---|---|
| `github.com/mark3labs/mcp-go` | `v0.44.0` | MCP stdio server, tool registration |
| `modernc.org/sqlite` | `v1.45.0` | Pure-Go SQLite driver (no CGO) |
| `github.com/charmbracelet/bubbletea` | `v1.x` | TUI MVU framework |
| `github.com/charmbracelet/bubbles` | `latest` | `textinput`, `viewport` components |
| `github.com/charmbracelet/lipgloss` | `latest` | Terminal styling |

**HTTP Server**: The standard library `net/http` is used for the `serve` command which receives OpenCode lifecycle hooks and plugin captures. This avoids the overhead of MCP subprocess management for fire-and-forget captures. OpenCode talks to the MCP stdio server only for the 3 agent-facing tools.

---

## 12. Phased Implementation Plan

### Phase 1 — Store + MCP (core loop works)
**Goal**: The Go binary can receive captures from the plugin and return them to agents.

Tasks:
1. Update `go.mod` with correct module path and Go version, add dependencies
2. Implement `internal/store/store.go`: schema, `Open`, migrations, `EnsureSession`, `AddCapture`, `ListCaptures`, `GetCaptureBySeq`, `GetCaptureContent`, `RenderHint`
3. Implement `internal/mcp/mcp.go` + `handlers.go`: all 7 tools (3 agent-facing + 4 internal)
4. Implement `cmd/context-bridge/main.go`: `mcp` subcommand
5. Write `internal/store/store_test.go` with table-driven tests for seq assignment, FTS search, hint rendering

Acceptance: `context-bridge mcp` starts, OpenCode can call all tools, a test session with captures round-trips correctly.

---

### Phase 2 — Thin TS Plugin
**Goal**: Replace the current fat plugin with the thin adapter.

Tasks:
1. Write `plugin/opencode/context-bridge.ts` (thin adapter, ~100 lines)
2. Verify all 3 OpenCode hooks work against Phase 1 MCP server
3. Verify hint injection works in a live OpenCode session
4. Update `AGENTS.md` / `ORCHESTRATOR.md` prompt contracts to reference `#seq` numbers in hint

Acceptance: Existing sessions continue to produce and recover subagent outputs with same UX. Agents use `read` with `#` numbers from hint.

---

### Phase 3 — TUI
**Goal**: Browseable terminal interface.

Tasks:
1. Implement `internal/tui/model.go` (Model struct, screens, `New()`, `Init()`)
2. Implement `internal/tui/update.go` (key routing, store queries as tea.Cmd)
3. Implement `internal/tui/view.go` (per-screen renderers, lipgloss styling)
4. Wire `context-bridge tui` CLI subcommand

Acceptance: `context-bridge tui` shows sessions, allows drilling into captures, shows full content, supports `/` search.

---

### Phase 4 — Polish & Release
**Goal**: Production-ready binary.

Tasks:
1. `.goreleaser.yaml` for cross-platform single binary (darwin/linux/arm64/amd64)
2. `context-bridge version` subcommand with ldflags version stamping
3. `README.md` with manual install instructions (build → copy binary → edit `opencode.json` → copy plugin)
4. Integration test: end-to-end capture → search → read flow

---

## 13. Risks & Tradeoffs

### R1: MCP subprocess management
**Risk**: OpenCode's MCP client must keep `context-bridge mcp` alive. If it restarts, in-flight captures could be lost.  
**Mitigation**: MCP stdio servers are stateless per call — the store is the durable state. Any restart is safe; only in-flight calls during restart are lost (acceptable).

### R2: Concurrent writes (multiple sessions)
**Risk**: Multiple active OpenCode sessions writing captures simultaneously could cause SQLite locking.  
**Mitigation**: WAL mode + `busy_timeout = 5000ms`. SQLite handles multiple readers/one writer. The MCP server handles one call at a time over stdio; concurrent MCP server instances would need a separate binary instance per session (fine — each session gets its own MCP subprocess).

**Decision**: One `context-bridge mcp` process per OpenCode session. SQLite WAL handles concurrent access from multiple such processes on the same DB file.



### R4: Old plugin still active during transition
**Risk**: Running both old and new plugins simultaneously causes double-capture.  
**Mitigation**: Phase 2 requires explicitly replacing (not appending) the plugin file. The old plugin and new plugin cannot coexist because both listen to `tool.execute.after`. Make this explicit in the migration instructions.

### R5: Internal tools accessible to agents
**Risk**: If `capture_output` / `ensure_session` etc. are listed in `opencode.json` permissions, agents could call them directly and corrupt the store.  
**Mitigation**: Never list internal tools in `permission` block. They are registered on the MCP server but not advertised in agent permissions. An agent LLM won't spontaneously call undocumented tools.

### R6: Session deletion policy
**Current behavior**: Hard-delete session directory on `session.deleted`.  
**New behavior**: Soft-delete with `deleted_at` timestamp. Data is retained.  
**Tradeoff**: Slightly higher storage growth. Benefit: no data loss on accidental session deletion; enables "deleted session" history browsing in TUI. Add a `context-bridge purge --before=<date>` CLI command for manual cleanup.

### R7: Search quality regression
**Current**: Linear scan with case-insensitive substring match (simple, predictable).  
**New**: Regex search across captures (more powerful, supports grep-like functionality).  
**Mitigation**: We automatically prefix regex with `(?i)` if valid, to preserve case-insensitive match behavior by default, and fall back to quoted literal matches if regex fails to compile.

### R8: preview extraction at query time vs ingestion time
**Decision**: Extract at ingestion. Preview is stored in DB.  
**Tradeoff**: Slightly larger DB, but `ListCaptures` and `RenderHint` are fast — no file reads, no re-scanning.

---

## 14. Hint Contract Change (Breaking)

The current hint format shows **file paths**, which agents cannot use with `read` (which takes a seq number). This is a known contract bug.

**New hint format**:
```
## Prior Research Available — READ BEFORE WORKING

There are 3 prior subagent outputs from this session:
- [#1] [grep] Deep investigation of engram project
- [#2] [grep] Investigate context-bridge plugin  
- [#3] [explore] Find all Button component variants

**REQUIRED**: Before starting, check prior research relevant to your task.
- Use `read` with the # number to read full output
- Use `search` with keywords to find specific info
- Use `list` to see the full list with sizes and timestamps
Do NOT redo research that already exists.
```

`AGENTS.md` and `ORCHESTRATOR.md` must be updated to reflect `#seq` lookup semantics at the same time as Phase 2 deployment.

---

## Summary

| Concern | Decision |
|---|---|
| Persistence | SQLite with FTS5, WAL, `modernc.org/sqlite` (pure Go) |
| MCP transport | stdio via `github.com/mark3labs/mcp-go` (for agent tools only) |
| Plugin transport | HTTP server (`net/http`) via `context-bridge serve` |
| Plugin | Thin TS adapter, ~100 lines, no business logic |
| Session cleanup | Soft-delete with `deleted_at`; manual purge CLI |
| Search | FTS5 primary, LIKE fallback |
| TUI | Single `tea.Model`, 5 screens, read-only |
| Auto-config | None — fully manual setup |
| Tool names | `list`, `read`, `search` |
| Hint format | Changed: `#seq` numbers instead of file paths (breaking, intentional) |
| Install | Build binary → copy to PATH → edit `opencode.json` → copy plugin |

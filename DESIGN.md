# context-bridge — Technical Design

**Status**: Implemented  
**Author**: Architecture session  
**Date**: 2026-03-22 (updated 2026-04-04)

---

## 1. Purpose & Scope

`context-bridge` is a **single-binary Go MCP service** that captures subagent task outputs from OpenCode sessions, persists them in a local SQLite store, and exposes them back to agents via three MCP tools.

The system replaces all business logic previously embedded in a TypeScript plugin. A thin TypeScript adapter remains to forward OpenCode lifecycle hooks to HTTP — it contains no business logic.

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
  created_at   TEXT NOT NULL     -- ISO timestamp
  deleted_at   TEXT NULL         -- soft delete

Capture
  id               INTEGER PK AUTOINCREMENT
  session_id       TEXT FK → Session.id   -- root session that owns this capture
  seq              INTEGER               -- per-session monotonic 1..N
  child_session_id TEXT NULL              -- OpenCode child session ID (if known)
  call_id          TEXT NOT NULL          -- OpenCode tool call ID
  agent            TEXT NOT NULL          -- subagent type (grep, explore, executor, ...)
  description      TEXT NOT NULL          -- task description passed to subagent
  content          TEXT NOT NULL          -- full raw output markdown
  preview          TEXT NOT NULL          -- first N non-blank lines (pre-extracted)
  bytes            INTEGER NOT NULL       -- content byte count
  source_path      TEXT NULL              -- optional file path for context
  captured_at      TEXT NOT NULL          -- when capture was ingested
  created_at       TEXT NOT NULL          -- DB row creation timestamp
  UNIQUE(session_id, seq)
  UNIQUE(session_id, call_id)

FTS5 virtual table for full-text search:
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
4. Soft deletes: when a session is deleted, mark `deleted_at`; retain data until explicit purge.
5. Preview is extracted at ingestion time (first 10 non-blank lines, max 80 chars/line) — no re-scanning at query time.
6. `captures_fts` is kept in sync via SQLite triggers on INSERT, UPDATE, and DELETE.
7. `call_id` uniqueness prevents duplicate captures from retry/replay scenarios.

---

## 3. Package Layout

```
context-bridge/
├── cmd/
│   └── context-bridge/
│       └── main.go              # CLI dispatcher (serve, mcp, tui, web, uninstall, version)
├── internal/
│   ├── store/
│   │   └── store.go             # DB open, schema migration, all domain operations
│   │   └── store_test.go
│   ├── mcp/
│   │   └── mcp.go               # NewServer(), 3 tool registrations, ServeStdio
│   ├── server/
│   │   └── server.go            # HTTP server for plugin hooks (/health, /events, /capture, /hint)
│   ├── config/
│   │   └── config.go            # Config file loading, search mode selection
│   ├── tui/
│   │   ├── model.go             # Tab/Panel architecture with focus management
│   │   ├── update.go            # Key routing, state transitions
│   │   ├── view.go              # Per-tab/panel renderers
│   │   ├── styles.go            # Lipgloss styling definitions
│   │   └── plugin.go            # Plugin install helper for TUI
│   ├── web/
│   │   ├── web.go               # Web dashboard + REST API
│   │   └── static/              # Embedded HTML/CSS/JS assets
│   ├── uninstall/
│   │   ├── uninstall.go         # Uninstall engine (artifact detection, removal)
│   │   └── prompt.go            # Interactive confirmation prompts
│   └── opencode/
│       └── integration.go       # OpenCode config management (plugin install, MCP registration)
├── plugin/
│   ├── plugin.go                # //go:embed opencode/context-bridge.ts
│   └── opencode/
│       └── context-bridge.ts    # Thin HTTP adapter (~114 lines)
├── docs/                        # Deep-review documentation
├── script/
│   ├── install.sh               # Hosted install script
│   └── uninstall.sh             # Hosted uninstall script
├── go.mod, go.sum
├── .goreleaser.yaml             # Cross-platform single binary release
├── DESIGN.md                    # This file
└── README.md                    # User documentation
```

### Package responsibility boundaries

| Package | Owns | Does NOT own |
|---|---|---|
| `internal/store` | SQLite schema, all domain queries, FTS, seq counters, soft-delete policy, dual search modes | MCP protocol, HTTP, UI, hook business logic |
| `internal/mcp` | Tool registration (3 tools), arg parsing, MCP protocol, hint rendering | DB schema, session graph logic |
| `internal/server` | HTTP endpoints for plugin hooks (`/health`, `/events`, `/capture`, `/hint`) | MCP protocol, agent-facing tools |
| `internal/config` | Config file loading, search mode selection, path resolution | Any persistence, MCP behavior |
| `internal/tui` | Terminal UI, tab/panel architecture, key routing, deletion flows | Any DB write path (uses store API) |
| `internal/web` | Web dashboard, REST API, browser auto-open | MCP protocol, CLI surface |
| `internal/uninstall` | Artifact detection, removal logic, interactive prompts | Any persistence |
| `internal/opencode` | Plugin installation, MCP registration, JSONC config parsing | HTTP server, search logic |
| `cmd/context-bridge` | CLI flag parsing, mode dispatch, store init | Any business logic |
| `plugin/opencode/context-bridge.ts` | OpenCode hook binding, HTTP transport, auto-spawn | Session graph, persistence, search |

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
func (s *Store) ListRootSessions(limit int) ([]SessionSummary, error)  // NEW

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
    Content        string      // included in record (no separate GetCaptureContent)
    Bytes          int
    SourcePath     string      // NEW
    CapturedAt     time.Time
    DeletedAt      *time.Time  // NEW (from session join)
}
func (s *Store) AddCapture(input CaptureInput) (*CaptureRecord, error)

// Query
func (s *Store) ListCaptures(sessionID, agent string) ([]CaptureRecord, error)  // agent filter added
func (s *Store) GetCaptureBySeq(sessionID string, seq int) (*CaptureRecord, error)

// Delete (NEW)
func (s *Store) DeleteCapture(sessionID string, seq int) error

// Search
type SearchResult struct {
    Capture    CaptureRecord
    Snippet    string      // highlighted context lines with `>>>` prefix
    MatchCount int         // number of matches in this capture
}
type SearchMode string  // "regex" | "fts5"
func (s *Store) Search(sessionID, query string, contextLines int) ([]SearchResult, error)
func (s *Store) SearchWithMode(sessionID, query string, contextLines int, mode SearchMode) ([]SearchResult, error)  // NEW
func (s *Store) searchFTS5(sessionID, query string, contextLines int) ([]SearchResult, error)  // internal FTS5 impl

// Stats (NEW)
type SessionSummary struct {
    ID             string
    CreatedAt      time.Time
    LastCapturedAt time.Time
    CaptureCount   int
    DeletedAt      *time.Time
}
type StoreStats struct {
    Sessions   int
    Captures   int
    TotalBytes int64
}
func (s *Store) Stats() (StoreStats, error)

// Hint
func (s *Store) RenderHint(rootSessionID string) (string, error)
```

### Seq counter strategy
Use a single SQLite transaction:
```sql
SELECT COALESCE(MAX(seq), 0) + 1 FROM captures WHERE session_id = ?
```
SQLite write-locks the DB on write; safe under WAL with a single writer.

### Dual search modes
- **Regex mode** (default): Go regex with `(?i)` prefix, rejects invalid patterns with explicit errors (no fallback)
- **FTS5 mode**: SQLite FTS5 full-text search with literal-safe query sanitization (`BuildLiteralFTS5Match()`), BM25 ranking via hidden `rank` column, native highlighting via FTS5 `highlight()` function (no raw MATCH syntax exposed)

### Hint rendering (in store, not MCP)
```
## Prior Research Available — READ BEFORE WORKING

There are N prior subagent outputs from this session:
- [#1] [grep] Short task description (timestamp, size)
- [#2] [explore] Another task description ...

**REQUIRED**: Use read with the output # to read full content.
Use search to find specific information across all outputs.
Do NOT redo research that already exists.
```

The hint includes `#seq` numbers so agents can call `read` by number without consulting `list` first.

---

## 5. MCP Tool Surface

The Go MCP server exposes **exactly three tools** (agent-facing only). No internal tools are exposed via MCP.

### Tool: `list`

**Purpose**: List all captured outputs for the current session context.

```go
mcp.NewTool("list",
    mcp.WithDescription("View the current session's subagent output history with sequence numbers, times, sizes, and previews."),
    mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
    mcp.WithString("agent", mcp.Description("Optional agent filter, for example grep or explore.")),
)
```

**Behavior**:
1. Resolve root session from `session_id` (handles child sessions transparently)
2. Call `store.ListCaptures(rootSessionID, agentFilter)`
3. Return markdown table with `#`, agent, task preview, time, size

---

### Tool: `read`

**Purpose**: Read the full content of one output by its sequence number.

```go
mcp.NewTool("read",
    mcp.WithDescription("Read the full content of one captured output by its output number."),
    mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
    mcp.WithNumber("output", mcp.Required(), mcp.Description("Output number from the hint or list output.")),  // "output" not "seq"
)
```

**Behavior**:
1. Resolve root session
2. Call `store.GetCaptureBySeq(rootSessionID, output)`
3. Return `capture.content` (full markdown)

---

### Tool: `search`

**Purpose**: Full-text search across all outputs in the current session context.

```go
mcp.NewTool("search",
    mcp.WithDescription(searchDescription(searchMode)),  // mode-dependent
    mcp.WithString("session_id", mcp.Description("Root or child OpenCode session ID.")),
    mcp.WithString("query", mcp.Required(), mcp.Description(searchQueryHint(searchMode))),
    mcp.WithNumber("context_lines", mcp.Description("Optional lines of context around each match. Defaults to 3.")),
)
```

**Behavior**:
1. Resolve root session
2. Call `store.SearchWithMode(rootSessionID, query, contextLines, searchMode)`
3. Return grouped match snippets with capture header (seq, agent, task)

**Search mode descriptions** (configurable):
- **Regex mode**: "Case-insensitive Go regex pattern or literal text. Examples: auth.*, error.*Handler, (?i)jwt, token. Invalid patterns return explicit errors."
- **FTS5 mode**: "Keywords separated by spaces. Each keyword must appear in the content. Punctuation preserved as-is. Results ranked by BM25 internally."

---

## 5.5 HTTP API (Plugin Internal)

**Purpose**: Called exclusively by the thin TS plugin to manage captures and get the pre-rendered hint. The plugin uses HTTP `fetch` to talk to the `context-bridge serve` process.

**Endpoints**:
| Endpoint | Method | Purpose |
|---|---|---|
| `/health` | GET | Health check → `{"ok": true}` |
| `/events` | POST | Handle `session.created` and `session.deleted` events |
| `/capture` | POST | Ingest a new subagent output |
| `/hint` | GET | Return hint block as JSON `{"text": "..."}` or empty if no captures |

These endpoints are NOT exposed to agents — only to the plugin process.

---

## 5.6 Web Dashboard API

**Purpose**: REST API for the web dashboard (`context-bridge web`).

**Endpoints**:
| Endpoint | Method | Purpose |
|---|---|---|
| `/api/stats` | GET | Aggregate stats (sessions, captures, bytes) |
| `/api/config` | GET | Current config file contents |
| `/api/config` | PUT | Update config (search mode, etc.) |
| `/api/sessions` | GET | List root sessions with summaries |
| `/api/sessions/{id}` | DELETE | Soft-delete a session |
| `/api/sessions/{id}/captures` | GET | List captures for a session |
| `/api/sessions/{id}/captures/{seq}` | GET | Get single capture detail |
| `/api/sessions/{id}/captures/{seq}` | DELETE | Delete a capture |
| `/api/sessions/{id}/search` | GET | Search within a session |

---

## 6. CLI / Binary Interface

```
context-bridge serve      # HTTP server for the thin OpenCode plugin
                          # Flags: --addr (default: 127.0.0.1:7438)

context-bridge mcp        # MCP stdio server (primary agent-facing mode)

context-bridge tui        # Terminal browser with tabs, filtering, deletion

context-bridge web        # Web dashboard (default: 127.0.0.1:7440)
                          # Flags: --addr, --open (default: true)

context-bridge uninstall  # Remove project-owned artifacts
                          # Flags: --dry-run, --mode (full|preserve-data), --yes

context-bridge version    # Print version
```

**No `setup` subcommand.** Installation is handled via `script/install.sh` or manual steps.

The binary reads its DB path from:
1. `CONTEXT_BRIDGE_DB` environment variable
2. Default: `~/.local/share/context-bridge/store.db`

Config file path:
1. `CONTEXT_BRIDGE_CONFIG` environment variable
2. Default: `~/.config/context-bridge/config.json`

---

## 7. Thin TypeScript Plugin

Location: `plugin/opencode/context-bridge.ts`  
Installed to: `~/.config/opencode/plugins/context-bridge.ts`

### Plugin code (actual implementation)

```typescript
import type { Plugin } from "@opencode-ai/plugin";

const BRIDGE_PORT = parseInt(process.env.CONTEXT_BRIDGE_PORT ?? "7438", 10);
const BRIDGE_ADDR = process.env.CONTEXT_BRIDGE_ADDR ?? `127.0.0.1:${BRIDGE_PORT}`;
const BRIDGE_URL = `http://${BRIDGE_ADDR}`;
const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";

async function ensureBridge(): Promise<boolean> {
  if (await isBridgeRunning()) return true;  // 400ms timeout health check
  Bun.spawn([BRIDGE_BIN, "serve"], { stdout: "ignore", stderr: "ignore" });
  await new Promise(r => setTimeout(r, 600));  // wait for server startup
  return isBridgeRunning();
}

export const ContextBridge: Plugin = async () => {
  await ensureBridge();
  return {
    // 1. Register session events
    event: async ({ event }) => {
      if (event.type !== "session.created" && event.type !== "session.deleted") return;
      if (!(await ensureBridge())) return;
      await bridgeFetch("/events", { method: "POST", body: JSON.stringify(event) });
    },

    // 2. Forward Task outputs to HTTP server
    "tool.execute.after": async (input, output) => {
      if (input.tool !== "Task" && input.tool !== "task") return;
      const agent = input.args?.subagent_type ?? input.args?.subagentType;
      const content = extractOutputText(output);
      if (!agent || !content || content.length < 100 || !input.sessionID) return;
      if (!(await ensureBridge())) return;

      await bridgeFetch("/capture", {
        method: "POST",
        body: JSON.stringify({
          parent_session_id: input.sessionID,
          child_session_id: output?.metadata?.sessionId ?? "",
          call_id: input.callID,
          agent,
          description: input.args?.description ?? "",
          content,
          captured_at: new Date().toISOString(),
        }),
      });
    },

    // 3. Inject hint into system prompts
    "experimental.chat.system.transform": async (input, output) => {
      if (!input.sessionID) return;
      if (!(await ensureBridge())) return;

      const res = await bridgeFetch(`/hint?session_id=${encodeURIComponent(input.sessionID)}`);
      const hint = res?.text;
      if (!hint) return;

      if (output.system.length > 0) {
        output.system[output.system.length - 1] += `\n\n${hint}`;
      } else {
        output.system.push(hint);
      }
    },
  };
};
```

### What the plugin does NOT do
- No manifest reading/writing
- No filesystem operations beyond spawning the binary
- No session counter tracking
- No in-memory children/sessions maps
- No search logic
- No preview extraction
- No business logic whatsoever

### Environment variables
| Variable | Default | Purpose |
|---|---|---|
| `CONTEXT_BRIDGE_PORT` | `7438` | HTTP server port |
| `CONTEXT_BRIDGE_ADDR` | `127.0.0.1:${PORT}` | Full HTTP address |
| `CONTEXT_BRIDGE_BIN` | `context-bridge` (via `Bun.which`) | Binary path for auto-spawn |

---

## 8. TUI Structure

The TUI uses a **tab + panel architecture** with focus management, not the original screen-enum model.

### Tabs

```go
type Tab int
const (
    TabOverview Tab = iota  // dashboard with stats and recent sessions
    TabSessions             // session list with captures panel
    TabSearch               // full search interface
)
```

### Focus/Panel enums

```go
type FocusPane int
const (
    FocusSessions       // session list cursor
    FocusCaptures       // capture list cursor
    FocusCaptureDetail  // viewport for full content
    FocusSearch         // search input field
    FocusSearchResults  // search results cursor
)

type RightPanel int
const (
    PanelCaptures        // capture list view
    PanelCaptureDetail   // full content viewport
    PanelSearch          // search input
    PanelSearchResults   // search results list
)
```

### Model shape

```go
type Model struct {
    store      *store.Store
    searchMode store.SearchMode
    configPath string

    activeTab    Tab
    focus        FocusPane
    rightPanel   RightPanel

    // Loading and messaging
    loading   bool
    errorMsg  string
    statusMsg string
    spinner   spinner.Model

    // Stats (loaded once at Init)
    stats store.StoreStats

    // Dashboard state
    dashboardSessions []store.SessionSummary
    dashboardCursor   int
    dashboardScroll   int

    // Session screen state
    selectedSession string
    sessionCaptures []store.CaptureRecord
    sessionCursor   int
    sessionScroll   int

    // Inline filter (session screen)
    filterInput  textinput.Model
    filterActive bool
    filterQuery  string

    // Capture screen state
    selectedCapture *store.CaptureRecord
    contentViewport viewport.Model

    // Search screen state
    searchInput         textinput.Model
    searchScope         string
    searchResults       []store.SearchResult
    searchRawOutput     string
    searchCursor        int
    searchResultsScroll int

    // Confirmation dialog
    confirmActive bool
    confirmMsg    string
    confirmAction confirmAction  // deleteSession | deleteCapture

    // Settings modal
    settingsActive    bool
    settingsCursor    int
    settingsSaveError string

    ready bool
}
```

### Key bindings
| Key | Action |
|---|---|
| `tab` / `1` / `2` / `3` | Switch tabs |
| `q` / `ctrl+c` | Quit |
| `enter` | Select / drill into |
| `esc` | Go back / close modal |
| `/` | Open inline filter (session screen) |
| `s` | Open search dialog |
| `x` | Delete selected item (with confirmation) |
| `p` | Open settings |
| `i` | Install plugin from TUI |
| `j` / `k` / `↓` / `↑` | Cursor navigation |
| `PgDn` / `PgUp` | Scroll in capture detail |

### TUI now supports write operations
- Delete sessions and captures via `x` key
- Confirmation dialogs for destructive actions
- Settings modal for config changes (search mode)
- Plugin installation flow via `i` key

---

## 9. SQLite Schema (Full)

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at  TEXT NULL
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
    source_path      TEXT NULL,
    captured_at      TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(session_id, seq),
    UNIQUE(session_id, call_id)
);

CREATE INDEX IF NOT EXISTS idx_captures_session ON captures(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_captures_agent   ON captures(session_id, agent);
CREATE INDEX IF NOT EXISTS idx_sessions_parent  ON sessions(parent_id);

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

### Schema notes
- `source_path` and `created_at` columns added to captures table
- `call_id` uniqueness prevents duplicate captures
- `idx_sessions_parent` enables efficient parent chain traversal
- Triggers cover INSERT, DELETE, and UPDATE for FTS sync

---

## 10. opencode.json Configuration

The user must add this to `~/.config/opencode/opencode.json`:

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
    "list":   "allow",
    "read":   "allow",
    "search": "allow"
  }
}
```

**Key differences from original design**:
- `command` is an array, not `command` + `args` split
- Permissions are `"allow"` strings, not `{ "allow": true }` objects
- `enabled: true` flag is required

---

## 11. External Dependencies

| Package | Version | Purpose |
|---|---|---|
| `github.com/mark3labs/mcp-go` | `v0.45.0` | MCP stdio server, tool registration |
| `modernc.org/sqlite` | `v1.47.0` | Pure-Go SQLite driver (no CGO) |
| `github.com/charmbracelet/bubbletea` | `v1.3.10` | TUI MVU framework |
| `github.com/charmbracelet/bubbles` | `v1.0.0` | `textinput`, `viewport`, `spinner` components |
| `github.com/charmbracelet/lipgloss` | `v1.1.0` | Terminal styling |
| `github.com/muesli/reflow` | `v0.3.0` | Word wrapping for viewport |

**HTTP Server**: Standard library `net/http` for the `serve` command (plugin hooks) and `web` command (dashboard).

---

## 12. Implementation Status

All original phases are **COMPLETE**. Additional features were built beyond the original plan.

### Phase 1 — Store + MCP ✅
- Store implemented with schema, FTS5, seq counters
- MCP tools: `list`, `read`, `search` (3 only)
- Dual search modes (regex + FTS5)
- Stats, session summaries, delete operations

### Phase 2 — Thin TS Plugin ✅
- Plugin is 114 lines, no business logic
- Auto-spawn logic with health check
- All hooks working (`event`, `tool.execute.after`, `system.transform`)
- Hint injection into system prompts

### Phase 3 — TUI ✅ (different architecture)
- Tab/Panel architecture instead of Screen enum
- Inline filtering, settings modal, confirmation dialogs
- Delete operations (sessions and captures)
- Plugin installation flow

### Phase 4 — Polish & Release ✅
- `.goreleaser.yaml` for cross-platform binaries
- `version` command with ldflags stamping
- README.md with install/uninstall instructions

### Phase 5 — Web Dashboard ✅ (new)
- `web` command with browser auto-open
- REST API for stats, sessions, captures, search
- Config update via web API
- Embedded static assets

### Phase 6 — Uninstall System ✅ (new)
- `uninstall` command with `--dry-run`, `--mode`, `--yes`
- Interactive and non-interactive modes
- Artifact detection (binary, plugin, MCP entry, config, data)
- Two modes: `full` (remove all), `preserve-data` (keep DB)

### Phase 7 — Config Package ✅ (new)
- Config file at `~/.config/context-bridge/config.json`
- Environment overrides: `CONTEXT_BRIDGE_CONFIG`, `CONTEXT_BRIDGE_DB`
- Search mode selection (regex vs fts5)
- Live config updates via web API

### Phase 8 — OpenCode Integration Package ✅ (new)
- `internal/opencode/integration.go`
- Plugin installation with binary path patching
- MCP registration management
- JSONC parsing for config files

---

## 13. Risks & Tradeoffs

### R1: MCP subprocess management
**Risk**: OpenCode's MCP client must keep `context-bridge mcp` alive.  
**Mitigation**: MCP stdio servers are stateless per call — the store is durable. Any restart is safe; only in-flight calls during restart are lost (acceptable).

### R2: Concurrent writes (multiple sessions)
**Risk**: Multiple OpenCode sessions writing simultaneously.  
**Mitigation**: WAL mode + `busy_timeout = 5000ms`. SQLite handles multiple readers/one writer. The MCP server handles one call at a time over stdio.

### R3: Plugin auto-spawn race
**Risk**: Multiple plugin hooks spawning the server simultaneously.  
**Mitigation**: `spawnInFlight` promise guards against duplicate spawns. 600ms wait after spawn ensures server readiness.

### R4: Session deletion policy
**Decision**: Soft-delete with `deleted_at` timestamp. Data is retained.  
**Tradeoff**: Slightly higher storage growth. Benefit: no data loss on accidental deletion; enables history browsing.

### R5: Search quality regression
**Decision**: Regex mode (default) + FTS5 mode (optional).  
**Mitigation**: Regex mode preserves case-insensitive substring behavior. FTS5 mode provides more powerful full-text search with snippet highlighting.

### R6: Preview extraction timing
**Decision**: Extract at ingestion. Preview is stored in DB.  
**Tradeoff**: Slightly larger DB, but `ListCaptures` and `RenderHint` are fast — no re-scanning.

### R7: call_id uniqueness
**Decision**: Duplicate captures with same `(session_id, call_id)` are deduplicated on insert.  
**Tradeoff**: Prevents data bloat from retry/replay scenarios. Same call_id always maps to same seq.

---

## 14. Hint Contract

The hint format uses `#seq` numbers for direct lookup:

```
## Prior Research Available — READ BEFORE WORKING

There are N prior subagent outputs from this session:
- [#1] [grep] Short task description (timestamp, size)
- [#2] [explore] Another task description ...

**REQUIRED**: Before starting, check prior research relevant to your task.
- Use read with the output # to read full content.
- Use search to find specific information across all outputs.
- Use list to see the full list with sizes and timestamps.
Do NOT redo research that already exists.
```

---

## Summary

| Concern | Decision |
|---|---|
| Persistence | SQLite with FTS5, WAL, `modernc.org/sqlite` (pure Go) |
| MCP transport | stdio via `github.com/mark3labs/mcp-go` (3 agent-facing tools) |
| Plugin transport | HTTP via `context-bridge serve` (not MCP) |
| Plugin | Thin TS adapter, 114 lines, auto-spawn, no business logic |
| Search | Dual mode: regex (default) + FTS5 (configurable) |
| TUI | Tab/Panel architecture with focus management, supports deletes |
| Web | Dashboard with REST API, browser auto-open |
| Uninstall | Full artifact cleanup with interactive/non-interactive modes |
| Config | JSON file with env overrides, live updates via web API |
| Auto-config | None — manual setup or install script |
| Tool names | `list`, `read`, `search` (parameter: `output` not `seq`) |
| Hint format | `#seq` numbers for direct lookup |
| Install | `script/install.sh` or manual: binary → MCP config → plugin |
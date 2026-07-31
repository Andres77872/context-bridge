# context-bridge — Technical Design

**Status**: Implemented; known transport and lifecycle risks are documented below  
**Author**: Architecture session  
**Date**: 2026-03-22 (updated 2026-07-20)

---

## 1. Purpose & Scope

`context-bridge` is a **single-binary Go MCP service** that captures subagent task outputs from OpenCode sessions, persists them in a local SQLite store, and exposes them back to agents via three MCP tools.

The system keeps persistence, search, and query logic in Go. A bounded TypeScript adapter owns OpenCode runtime registration, same-session binding, lineage repair, filtering, limits, and HTTP-over-Unix-socket transport.

Companion documents cover individual subsystems in more depth: the
[persistence model](./docs/persistence-model.md), the
[search subsystem](./docs/search-subsystem.md), and the
[MCP search tool contract](./docs/search/mcp-tool-contract.md). They are
enforced against the code by `internal/docsverification`.

### Non-goals
- No persistent OpenCode config-file mutation; MCP registration is runtime-only
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
  next_seq     INTEGER NOT NULL  -- durable capture-number allocator for this root
  created_at   TEXT NOT NULL     -- ISO timestamp
  ended_at     TEXT NULL         -- OpenCode emitted session.deleted
  deleted_at   TEXT NULL         -- explicit user soft-delete in TUI/web

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
3. `seq` is allocated atomically from the root's durable `next_seq` counter and is stable for as long as the capture stays under that root. Numbers are never reused after a delete; reparenting to a newly discovered authoritative root renumbers the capture once.
4. OpenCode's `session.deleted` lifecycle event marks `ended_at` and retains the session as queryable. Explicit TUI/web deletion marks `deleted_at` and hides it from normal queries.
5. Preview is extracted at ingestion time (first 10 non-blank lines, max 80 chars/line) — no re-scanning at query time.
6. `captures_fts` is kept in sync via SQLite triggers on INSERT, UPDATE, and DELETE.
7. `call_id` uniqueness prevents duplicate captures from retry/replay scenarios.

---

## 3. Package Layout

```
context-bridge/
├── cmd/
│   └── context-bridge/
│       └── main.go              # CLI dispatcher (serve, stop, mcp, tui, web,
│                                #   integration, update, uninstall, version)
├── internal/
│   ├── store/
│   │   ├── store.go             # DB open, schema migration, all domain operations
│   │   ├── analytics.go         # Usage aggregates, session detail, filtered listings
│   │   └── store_test.go
│   ├── search/
│   │   └── fts5.go              # BuildLiteralFTS5Match — literal-safe FTS5 queries
│   ├── mcp/
│   │   ├── mcp.go               # NewServer(), 3 tool registrations, result bounds, ServeStdio
│   │   └── render.go            # The exact tool payloads; shared with the web and TUI surfaces
│   ├── server/
│   │   └── server.go            # HTTP server for plugin hooks and graceful shutdown
│   ├── config/
│   │   └── config.go            # Config file loading, search mode selection
│   ├── tui/
│   │   ├── model.go             # Tab/Panel architecture with focus management
│   │   ├── update.go            # Key routing, state transitions
│   │   ├── view.go              # Per-tab/panel renderers
│   │   ├── styles.go            # Lipgloss styling definitions
│   │   └── plugin.go            # Plugin install helper for TUI
│   ├── web/
│   │   ├── web.go               # Server, routing, token/origin guard, security headers
│   │   ├── api.go               # REST handlers, response shapes, error mapping
│   │   └── static/              # Embedded, dependency-free dashboard
│   │       ├── index.html       # Shell markup + inline SVG icon sprite
│   │       ├── app.css          # Design tokens, light/dark themes, components
│   │       └── app.js           # Router, API client, views, charts, shortcuts
│   ├── uninstall/
│   │   ├── uninstall.go         # Uninstall engine (artifact detection, removal)
│   │   └── prompt.go            # Interactive confirmation prompts
│   ├── update/
│   │   └── update.go            # Verified self-update from GitHub Releases
│   ├── docsverification/
│   │   └── docsverification_test.go  # Test-only: asserts docs match runtime
│   └── opencode/
│       ├── integration.go       # Owned OpenCode adapter lifecycle; never edits OpenCode config
│       └── testdata/            # Legacy adapter fixture for ownership adoption
├── plugin/
│   ├── plugin.go                # //go:embed opencode/context-bridge.ts
│   ├── plugin_test.go           # Asserts the embedded adapter's runtime contract
│   ├── package.json             # devDeps + `typecheck` script (CI gate)
│   ├── tsconfig.json            # strict, noEmit typecheck config
│   └── opencode/
│       └── context-bridge.ts    # Bounded OpenCode integration adapter
├── docs/
│   ├── persistence-model.md     # Sequence allocation, bounds, redaction
│   ├── search-subsystem.md      # Mode selection and search bounds
│   └── search/                  # Status, MCP tool contract, vector-search placeholder
├── script/
│   ├── install.sh               # Hosted install script
│   ├── uninstall.sh             # Hosted uninstall script
│   └── uninstall_test.go        # Hosted uninstall headless-path coverage
├── go.mod, go.sum
├── .github/workflows/release.yml # Validation-gated release automation
├── .goreleaser.yaml             # Linux/macOS amd64/arm64 binary release
├── DESIGN.md                    # This file
├── LICENSE
└── README.md                    # User documentation
```

### Package responsibility boundaries

| Package | Owns | Does NOT own |
|---|---|---|
| `internal/store` | SQLite schema, all domain queries, FTS, seq counters, retention/pruning, soft-delete policy, dual search modes | MCP protocol, HTTP, UI, hook business logic |
| `internal/search` | Literal-safe FTS5 match construction (`BuildLiteralFTS5Match`) | SQL execution, ranking, result shaping |
| `internal/mcp` | Tool registration (3 tools), arg parsing, MCP protocol, result rendering, agent-facing bounds and trust boundary | DB schema, session graph logic, hint rendering |
| `internal/server` | HTTP endpoints for plugin hooks (`/health`, `/events`, `/capture`, `/hint`, `/shutdown`) | MCP protocol, agent-facing tools |
| `internal/config` | Config file loading, search mode selection, config/data/socket path resolution | Any persistence, MCP behavior |
| `internal/tui` | Terminal UI, tab/panel architecture, key routing, deletion flows | Any DB write path (uses store API) |
| `internal/web` | Token-protected loopback dashboard, REST API, analytics/search response shaping, self-contained frontend assets, optional browser launch | MCP protocol, CLI surface, aggregate SQL (delegates to `internal/store`) |
| `internal/uninstall` | Artifact detection, removal logic, interactive prompts | Any persistence |
| `internal/update` | Release resolution, checksum verification, atomic binary replacement, integration re-run | Ownership policy (delegates to `internal/opencode`), persistence |
| `internal/opencode` | Adapter ownership manifest, atomic install/update, safe removal | OpenCode config files, HTTP server, search logic |
| `internal/docsverification` | Test-only assertions that README/DESIGN/`docs/` match runtime behavior | Any production code path |
| `cmd/context-bridge` | CLI flag parsing, mode dispatch, store init | Any business logic |
| `plugin/opencode/context-bridge.ts` | OpenCode hook binding, HTTP transport, auto-spawn | Session graph, persistence, search |

---

## 4. Store API (Core Interface)

```go
// internal/store/store.go

type Store struct { db *sql.DB }

const MaxCaptureContentBytes = 256 << 10   // persisted content cap

func Open(dbPath string) (*Store, error)
func OpenManaged(dbPath string) (*Store, error)  // Context-Bridge-owned dir: tightens perms
func (s *Store) Close() error

// Session management
func (s *Store) EnsureSession(id, parentID string) error
func (s *Store) ResolveRoot(sessionID string) (string, error)  // walks parent chain
func (s *Store) ResolveRootContext(ctx context.Context, sessionID string) (string, error)
func (s *Store) MarkSessionDeleted(id string) error
func (s *Store) MarkSessionEnded(id string) error
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
    EndedAt        *time.Time  // OpenCode lifecycle end (from session join)
}
func (s *Store) AddCapture(input CaptureInput) (*CaptureRecord, error)

// Query — the *Context variants take a cancellable context and explicit bounds,
// and are what the MCP and web surfaces call. The non-Context forms are
// unbounded conveniences used by the TUI and tests.
func (s *Store) ListCaptures(sessionID, agent string) ([]CaptureRecord, error)  // agent filter added
func (s *Store) ListCapturesContext(ctx context.Context, sessionID, agent string, limit int) ([]CaptureRecord, error)
func (s *Store) GetCaptureBySeq(sessionID string, seq int) (*CaptureRecord, error)
func (s *Store) GetCaptureBySeqContext(ctx context.Context, sessionID string, seq int) (*CaptureRecord, error)
func (s *Store) CountCapturesContext(ctx context.Context, sessionID, agent string) (int, error)
func (s *Store) RootSessionExistsContext(ctx context.Context, sessionID string) (bool, error)

// Delete (NEW)
func (s *Store) DeleteCapture(sessionID string, seq int) error

// Redaction — applied on persist; exported so callers can preview the result
func RedactSensitiveContent(value string) string

// Search
type SearchResult struct {
    Capture    CaptureRecord
    Snippet    string      // highlighted context lines with `>>>` prefix
    MatchCount int         // number of matches in this capture
}
type SearchMode string  // "regex" | "fts5"
func (s *Store) Search(sessionID, query string, contextLines int) ([]SearchResult, error)
func (s *Store) SearchWithMode(sessionID, query string, contextLines int, mode SearchMode) ([]SearchResult, error)  // NEW
func (s *Store) SearchWithModeContext(ctx context.Context, sessionID, query string, contextLines int,
    mode SearchMode, maxResults, maxMatches, maxCandidates int) ([]SearchResult, error)
func (s *Store) searchRegexContext(...) ([]SearchResult, error)  // internal regex impl
func (s *Store) searchFTS5Context(...) ([]SearchResult, error)   // internal FTS5 impl

// Stats (NEW)
type SessionSummary struct {
    ID             string
    CreatedAt      time.Time
    LastCapturedAt time.Time
    CaptureCount   int
    DeletedAt      *time.Time
    EndedAt        *time.Time
}
type StoreStats struct {
    Sessions   int
    Captures   int
    TotalBytes int64
}
func (s *Store) Stats() (StoreStats, error)

// Hint
func (s *Store) RenderHint(rootSessionID string, mode SearchMode) (string, error)
```

### Seq counter strategy
Sequence numbers come from a **durable counter on the root session**, not from a scan of existing rows. Within the capture transaction:
```sql
SELECT next_seq FROM sessions WHERE id = ?;      -- allocate
UPDATE sessions SET next_seq = ? WHERE id = ?;   -- advance
```
SQLite write-locks the DB on write; safe under WAL with a single writer.

This is deliberately not `MAX(seq) + 1`. A durable counter means **deleting a capture never frees its number for reuse**, so an output number an agent saw earlier can never later refer to different content. The trade-off is that numbering has gaps after deletions, which is the intended behavior.

Late parent discovery can move captures from a provisional root into the authoritative root in a single transaction. During that migration, `call_id` values are deduplicated and the moved captures are assigned fresh sequence numbers from the authoritative root's counter — so `seq` is stable for the lifetime of a capture *within a root*, but a reparented capture is renumbered exactly once, when it changes roots.

### Retention and storage bounds
The store is self-limiting; there is no external cleanup job and no unbounded growth path.

| Constant | Value | Bound |
|---|---|---|
| `captureRetention` | 30 days | Age after which a capture is pruned |
| `MaxCaptureContentBytes` | 256 KiB | Persisted content per capture |
| `maxCapturesPerSession` | 1,000 | Captures per root session |
| `maxCapturesGlobal` | 10,000 | Captures across all sessions |
| `maxCaptureBytesGlobal` | 256 MiB | Logical content across all sessions |

`pruneRetention` runs at `Open`, and `pruneRetentionTx` runs inside every `AddCapture` transaction, so writes pay for their own cleanup. Over-cap deletion is oldest-first.

Read paths do not depend on pruning having run: every query also filters on `julianday(created_at) >= julianday('now','-30 days')` and on `deleted_at IS NULL`, so an expired or dashboard-deleted row is invisible even before it is physically removed. Logical deletion does not shrink the SQLite or WAL file immediately.

Two content caps apply in sequence and are frequently confused: the OpenCode adapter refuses to transmit more than 1 MiB of tool output, and the store then truncates what it persists to `MaxCaptureContentBytes` (256 KiB).

### Dual search modes
- **Regex mode** (default): Go regex with `(?i)` prefix, rejects invalid patterns with explicit errors (no fallback)
- **FTS5 mode**: SQLite FTS5 full-text search with literal-safe query sanitization (`BuildLiteralFTS5Match()`), BM25 ranking via hidden `rank` column, native highlighting via FTS5 `highlight()` function (no raw MATCH syntax exposed)

### Hint rendering (in store, not MCP)
`RenderHint(sessionID, mode)` resolves the root, lists its captures, and returns the empty string when there are none. See [§14 Hint Contract](#14-hint-contract) for the rendered form and its guarantees.

---

## 5. MCP Tool Surface

The Go MCP server exposes **exactly three tools** (agent-facing only). No internal tools are exposed via MCP.

### Tool: `list`

**Purpose**: List all captured outputs for the current session context.

```go
mcp.NewTool("list",
    mcp.WithDescription("View up to the 100 most recent subagent outputs for the current session tree ..."),
    mcp.WithString("session_id", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(256),
        mcp.Description("Root or child OpenCode session ID. The source OpenCode adapter overwrites this with the current runtime session.")),
    mcp.WithString("agent", mcp.MaxLength(128),
        mcp.Description("Optional agent filter, for example grep or explore.")),
)
```

**Behavior**:
1. Resolve root session from `session_id` (handles child sessions transparently)
2. Call `store.ListCapturesContext(ctx, sessionID, agentFilter, maxListCaptures)` plus `CountCapturesContext` for the total
3. Return markdown table with `#`, agent, task preview, time, size, headed by `showing N of TOTAL`

---

### Tool: `read`

**Purpose**: Read the full content of one output by its sequence number.

```go
mcp.NewTool("read",
    mcp.WithDescription("Read a byte-bounded prefix of one captured output by its output number. Results larger than 128 KiB are explicitly truncated."),
    mcp.WithString("session_id", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(256), ...),
    mcp.WithNumber("output", mcp.Required(), mcp.Min(1), mcp.MultipleOf(1),
        mcp.Description("Positive output number from the hint or list output.")),  // "output" not "seq"
)
```

**Behavior**:
1. Resolve root session
2. Call `store.GetCaptureBySeqContext(ctx, sessionID, output)`
3. Return `capture.content`, truncated to the shared payload cap if needed

---

### Tool: `search`

**Purpose**: Full-text search across all outputs in the current session context.

```go
mcp.NewTool("search",
    mcp.WithDescription(searchDescription(searchMode)),  // mode-dependent
    mcp.WithString("session_id", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(256), ...),
    mcp.WithString("query", mcp.Required(), mcp.MinLength(1), mcp.MaxLength(1024),
        mcp.Description(searchQueryHint(searchMode))),  // mode-dependent
    mcp.WithNumber("context_lines", mcp.DefaultNumber(3), mcp.Min(0), mcp.Max(20), mcp.MultipleOf(1),
        mcp.Description("Optional lines of context around each match. 0 returns only matched lines; maximum 20.")),
)
```

There is deliberately no `engine` parameter: the mode is process-global from `config.json`, so a caller cannot pick an engine per request.

**Behavior**:
1. Resolve root session
2. Call `store.SearchWithModeContext(ctx, sessionID, query, contextLines, searchMode, maxSearchResults, maxSearchMatches, maxSearchCandidates)`
3. Return grouped match snippets with capture header (seq, agent, task)

**Search mode descriptions** (selected by config, never both at once):
- **Regex mode**: "Search up to the 250 most recent captured outputs using case-insensitive Go regex, returning at most 50 outputs and 1000 matched lines. Invalid regex patterns return explicit errors."
- **FTS5 mode**: "Search retained outputs using literal-term full-text search, returning at most 50 outputs and 1000 matched lines. Each whitespace-delimited term must match; FTS5 operators are not accepted."

---

### Result bounds and trust boundary

Bounds are enforced by `internal/mcp`, independently of the store's local UI limits:

| Constant | Value | Bound |
|---|---|---|
| `maxToolResultBytes` | 128 KiB | Total payload of any tool result |
| `maxListCaptures` | 100 | Outputs returned by `list` |
| `maxSearchResults` | 50 | Result groups returned by `search` |
| `maxSearchMatches` | 1,000 | Matched lines returned by `search` |
| `maxSearchCandidates` | 250 | Outputs scanned by regex-mode `search` |
| `maxSessionIDLength` / `maxSearchQueryLength` / `maxAgentFilterLength` | 256 / 1024 / 128 | Argument lengths |

`boundedToolResult(prefix, value, suffix)` wraps every result:

```
<prefix — states that the payload is untrusted>

<untrusted-context-bridge-data>
...captured content...
</untrusted-context-bridge-data>

<suffix — follow-up guidance, outside the boundary>
```

Captured content is scrubbed of the boundary markers themselves (replaced with `[REMOVED TRUST BOUNDARY MARKER]`) so stored text cannot forge an early close, and the payload is coerced to valid UTF-8. On overflow the value is truncated on a rune boundary and `[Context Bridge result truncated at the tool-output limit.]` is appended inside the boundary. This is a prompt-injection mitigation, not a guarantee.

---

## 5.5 HTTP API (Plugin Internal)

**Purpose**: Intended for the TS adapter to manage captures and get the pre-rendered hint. The adapter uses Bun's HTTP `fetch` over a Unix-domain socket to talk to the `context-bridge serve` process.

**Endpoints**:
| Endpoint | Method | Purpose |
|---|---|---|
| `/health` | GET | Identity check → `{"ok":true,"service":"context-bridge","protocol":1,"version":"..."}` |
| `/events` | POST | Handle `session.created` lineage and mark `session.deleted` as lifecycle-ended |
| `/capture` | POST | Ingest a new subagent output |
| `/hint` | GET | Return hint block as JSON `{"text": "..."}` or empty if no captures |
| `/shutdown` | POST | Unix-socket mode only: acknowledge and request graceful server shutdown |

The default listener is `<UserConfigDir>/context-bridge/bridge.sock`. Its parent directory is created or tightened to mode `0700`, and the socket is mode `0600`. Under normal Unix discretionary access control this constrains access to the owning account, but it is not application-layer authentication and does not constrain privileged processes. The adapter requires this socket transport and does not consume TCP address/port settings. Explicit socket paths must be absolute in the adapter, `serve`, `stop`, and uninstall. `serve` replaces a pre-existing socket only after a dial proves it absent or refusing connections; timeouts and other indeterminate errors fail closed. During graceful shutdown the store closes before the owned socket is unlinked, and uninstall treats disappearance of that same socket path—not merely connection refusal—as the completion signal.

`serve --addr` remains an explicit loopback-only compatibility fallback. It has no authentication token; any local process or user that can connect can call mutation endpoints, and another local user can pre-bind the address. The health response (`service` + `protocol`) is compatibility detection, not authentication for TCP. Uninstall checks an explicitly configured TCP endpoint after any Unix-socket shutdown, refuses redirects, and fails closed on indeterminate probe errors. Mutation endpoints require `application/json`, reject browser `Origin`, and enforce body limits in both modes. `/shutdown` is intentionally absent in TCP mode.

---

## 5.6 Web Dashboard API

**Purpose**: REST API for the web dashboard (`context-bridge web`).

**Endpoints**:
| Endpoint | Method | Purpose |
|---|---|---|
| `/api/meta` | GET | Build version, running search mode, retention window, database path and size, request limits |
| `/api/stats` | GET | Aggregate stats (sessions, captures, bytes) — the cheap endpoint the dashboard polls |
| `/api/analytics` | GET | Usage aggregates for a window (`?days=`, clamped to retention) |
| `/api/agents` | GET | Distinct agents seen in the retention window |
| `/api/config` | GET | Current config file contents |
| `/api/config` | PUT | Update config (search mode, etc.) |
| `/api/search` | GET | Search across every live session, or one (`?session=`), with `?agent=`, `?context=`, `?limit=` |
| `/api/sessions` | GET | List root sessions with usage totals (`?q=`, `?agent=`, `?sort=`, `?include_deleted=`, `?limit=`) |
| `/api/sessions/{id}` | GET | Session detail: byte totals, per-agent breakdown, time span, sub-session count |
| `/api/sessions/{id}` | DELETE | Soft-delete a session |
| `/api/sessions/{id}/export` | GET | Download the session and its captures as one JSON document |
| `/api/sessions/{id}/captures` | GET | Filtered, paged capture listing (`?agent=`, `?q=`, `?order=`, `?limit=`, `?offset=`) |
| `/api/sessions/{id}/captures/{seq}` | GET | Get single capture detail |
| `/api/sessions/{id}/captures/{seq}/raw` | GET | Download the capture body as `text/plain` |
| `/api/sessions/{id}/captures/{seq}` | DELETE | Delete a capture |
| `/api/sessions/{id}/search` | GET | Session-scoped alias for `/api/search` |
| `/api/sessions/{id}/mcp/list` | GET | The exact payload the MCP `list` tool returns |
| `/api/sessions/{id}/mcp/read/{seq}` | GET | The exact payload the MCP `read` tool returns |
| `/api/sessions/{id}/mcp/search` | GET | The exact payload the MCP `search` tool returns |

**Agent view.** The `mcp/*` endpoints exist because the dashboard's purpose is inspecting what the model received, not presenting a prettier version of it. `internal/mcp/render.go` holds `RenderList`, `RenderRead`, and `RenderSearch`; the MCP tool handlers are thin wrappers around them, and the dashboard and TUI call the same functions. `TestRenderFunctionsMatchToolOutputByteForByte` drives the tools over JSON-RPC and compares the result with the render functions, so a surface can never drift into showing something the agent never saw. The response carries the payload byte size, the tool-output limit, and whether truncation fired, because a truncated payload is exactly the kind of thing an operator is looking for.

`/api/search` and `/api/sessions/{id}/search` share one implementation; the path form only pins the session. Both return an envelope (`results`, `total_matches`, `mode`, `scope`, `elapsed_ms`, `truncated`) so the client can distinguish "no matches" from "limit reached". Capture listings return `{items, total, filtered, limit, offset, agents, order}`, which is what lets the dashboard show "showing X of Y" and populate its agent filter without a second request.

The search engine stays a global setting: no endpoint accepts a per-request mode. Query mistakes (invalid regex, malformed FTS5) return `400` rather than `500`, so the dashboard can show them inline instead of as an outage.

**Analytics** are computed in SQL against the same scope as every other read path — live root sessions inside the retention window — so deleting a session immediately changes the totals. The aggregate includes per-agent usage, dense daily buckets (days with no captures are present with zero counts), a weekday×hour heatmap, byte percentiles (median/p95), a size histogram, the busiest sessions, and the database file size on disk.

**Frontend**: `internal/web/static/` ships `index.html`, `app.css`, and `app.js` with no build step and no CDN. The page renders identically offline; charts are hand-built inline SVG. That is enforced by the CSP: `default-src 'none'` with `script-src 'self'` (no inline script). `style-src` additionally allows inline attributes because chart marks carry their colour in a `style` attribute, which cannot execute. All dashboard actions dispatch through `data-action` attributes and event delegation, so no session id is ever concatenated into executable markup.

At startup the dashboard generates a random 256-bit token and prints `http://<loopback>/?token=<token>`. A valid bootstrap request sets an `HttpOnly`, `SameSite=Strict` session cookie and redirects to `/`, removing the token from the current URL. All other routes require that cookie. The outer handler also requires a loopback `Host` and, when `Origin` is present, an exact same-origin loopback value; this rejects DNS-rebinding Host values and cross-origin browser requests. Requests without `Origin` remain valid only with the token cookie. Responses use `no-store`, `no-referrer`, `nosniff`, frame denial, and same-origin resource headers.

Browser launch is opt-in (`--open`, default false) because the launcher receives the tokenized URL as a process argument. The printed URL and any launcher arguments remain bearer-secret exposure points for the lifetime of that server token.

---

## 6. CLI / Binary Interface

```
context-bridge serve      # HTTP server for the OpenCode adapter
                          # Default: private Unix socket
                          # Flags: --socket, or explicit loopback TCP --addr

context-bridge stop       # Gracefully stop the Unix-socket server
                          # Flag: --socket

context-bridge mcp        # MCP stdio server (primary agent-facing mode)

context-bridge tui        # Terminal browser with tabs, filtering, deletion

context-bridge web        # Web dashboard (default: 127.0.0.1:7440)
                          # Flags: --addr, --open (default: false)

context-bridge integration install|status|uninstall
                          # Ownership-aware OpenCode adapter lifecycle
                          # install flag: --owned-binary (hosted installer only)

context-bridge update     # Verified in-place self-update from GitHub Releases
                          # Flags: --version <tag> (default latest), --check

context-bridge uninstall  # Remove project-owned artifacts
                          # Flags: --dry-run, --mode (full|preserve-data), --yes

context-bridge version    # Print version
```

**No `setup` subcommand.** Installation is handled via `script/install.sh` or `context-bridge integration install`.

The binary reads its DB path from:
1. `CONTEXT_BRIDGE_DB` environment variable
2. Default: `~/.local/share/context-bridge/store.db`

Config file path:
1. `CONTEXT_BRIDGE_CONFIG` environment variable
2. Default: `~/.config/context-bridge/config.json`

Plugin socket path:
1. `CONTEXT_BRIDGE_SOCKET` environment variable
2. Default: `<UserConfigDir>/context-bridge/bridge.sock`

Whitespace-only path variables are treated as unset. Every effective config, database, socket, Linux XDG config base, and XDG data base path is trimmed, required to be absolute, and lexically canonicalized before use. Resolution errors abort the operation. In particular, a missing user home is an error; the database never falls back to a cwd-relative file. Hosted uninstall forwards `--dry-run` without opening `/dev/tty`, while destructive non-interactive runs still require an explicit mode and `--yes`.

---

## 7. Bounded TypeScript Adapter

Location: `plugin/opencode/context-bridge.ts`  
Installed to: `~/.config/opencode/plugins/context-bridge.ts`

### Module shape: one file, two plugin runtimes

OpenCode loads plugins through two systems that glob the same `{plugin,plugins}/*.{ts,js}` directories. The adapter default-exports a shape both accept:

```ts
export default { id: "context-bridge", setup, server }
```

| Loader | Reads | Status |
|---|---|---|
| V1 — `packages/opencode/src/plugin` via `readV1Plugin` | `id` + `server()` returning `Hooks` | Authoritative today |
| V2 — `packages/core/src/config/plugin/external.ts` via the `PluginModule` schema | `id` + `setup(context)` | Adopted, inert until its domains land |

The published V2 context (`@opencode-ai/plugin/v2/promise`) offers `agent`, `aisdk`, `catalog`, `command`, `integration`, `reference`, and `skill`. Capture requires the `tool` and `event` domains, which `packages/plugin/src/v2/effect/PLAN.md` lists as agreed design (`ctx.tool.hook("execute.after")`, `ctx.event.subscribe(...)`) but which the runtime does not implement. There is no V2 equivalent for MCP registration or the system-prompt transform at all.

`setup` therefore feature-detects those domains. It installs the capture hook and the session-event subscription the moment they appear, and returns without side effects when they are missing. **Ownership is single-writer**: `setup` sets `v2OwnsCapture` only after `ctx.tool.hook("execute.after")` registers successfully, and both V1 capture paths (`event`, `tool.execute.after`) return early when that flag is set. A runtime exposing a partial tool domain therefore never silences V1.

Capture, lineage sync, hint retrieval, and bounding live in one runtime-agnostic core shared by both paths, so the two runtimes cannot drift in behaviour.

### Runtime contract

- `config`: inject a local MCP entry only when absent; respect explicit disable; preserve equivalent entries; warn and leave conflicts untouched.
- `tool.execute.before`: mutate Context Bridge tool arguments in-place and force `session_id` to the active OpenCode session.
- `event`: persist session create/delete lineage events.
- `tool.execute.after`: skip background placeholders, ignore only empty foreground outputs, repair child lineage, cap agent/description/content at 128 B/512 B/1 MiB, and capture it.
- `experimental.chat.system.transform`: append a sanitized hint capped at 64 KiB on every inference; OpenCode reconstructs the system prompt, so the adapter must not suppress later transforms.
- Backend probe: require `service=context-bridge` and `protocol=1` over the Unix socket; spawn only when unreachable; apply per-request timeouts and a 4-second hook-pipeline deadline.

### What the plugin does NOT do
- No manifest reading/writing
- No project-source or OpenCode config-file writes; executable resolution/realpath reads are the only direct filesystem access
- No session counter tracking
- No durable session graph; runtime state is limited to spawn coordination, warnings, and binding state
- No search logic
- No preview extraction
- No OpenCode config-file parsing or mutation

### Environment variables
| Variable | Default | Purpose |
|---|---|---|
| `CONTEXT_BRIDGE_SOCKET` | `<UserConfigDir>/context-bridge/bridge.sock` | Absolute Unix socket path |
| `CONTEXT_BRIDGE_BIN` | `context-bridge` (via `Bun.which`) | Binary path for auto-spawn |

The adapter mirrors Go's user-config defaults: `${XDG_CONFIG_HOME:-$HOME/.config}` on Linux (non-empty XDG paths are trimmed, canonicalized, and must be absolute) and `$HOME/Library/Application Support` on macOS. An explicit socket path follows the same fail-closed canonicalization contract.

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
    next_seq    INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at  TEXT NULL,
    ended_at    TEXT NULL
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

The DDL above is the current shape. `ended_at` is declared on `sessions` only; there is no per-capture end timestamp. `CaptureRecord.EndedAt` and `SessionSummary.EndedAt` are both populated from `sessions.ended_at` via a join, so an "ended" capture means its owning session ended.

### Migrations

`currentSchemaVersion = 4`. A database newer than the running binary is a hard error, not a silent downgrade.

| Version | Change |
|---|---|
| 1 | Base schema above (including `next_seq`), indexes, FTS5 table, and the three sync triggers |
| 2 | `ALTER TABLE sessions ADD COLUMN ended_at TEXT NULL` |
| 3 | `redactLegacyCaptures` — re-runs redaction and truncation over rows persisted before those rules existed |
| 4 | Adds `sessions.next_seq` to pre-existing databases and backfills it to `MAX(1, MAX(captures.seq) + 1)` per session |

Migrations 2 and 4 are column additions guarded by `hasColumn`, so they are safe to re-run against a database that already has the column.

### Schema notes
- `next_seq` is the durable per-root sequence allocator; see [Seq counter strategy](#seq-counter-strategy)
- `source_path` and `created_at` columns added to captures table
- `call_id` uniqueness prevents duplicate captures
- `idx_sessions_parent` enables efficient parent chain traversal
- Triggers cover INSERT, DELETE, and UPDATE for FTS sync
- `captures_fts` is an external-content FTS5 table (`content='captures'`), so the triggers are what keep it consistent

---

## 10. OpenCode Runtime Integration

The adapter mutates OpenCode's in-memory config during startup; no integration command reads or writes `opencode.json` or `opencode.jsonc`.

Runtime rules:

1. Missing `mcp.context-bridge`: inject `{type:"local", command:[BRIDGE_BIN,"mcp"], enabled:true}` in memory.
2. Explicitly disabled entry: respect it.
3. Equivalent local entry: preserve it and enable session binding.
4. Conflicting entry: preserve it, disable binding to that entry, and warn without serializing its potentially sensitive contents.

OpenCode exposes the MCP tools as `context-bridge_list`, `context-bridge_read`, and `context-bridge_search`.

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

**HTTP Server**: Standard library `net/http` for the `serve` command (HTTP over a Unix socket by default, explicit loopback TCP fallback) and `web` command (loopback TCP dashboard).

---

## 12. Implementation Status

All original phases are **COMPLETE**. Additional features were built beyond the original plan.

### Phase 1 — Store + MCP ✅
- Store implemented with schema, FTS5, seq counters
- MCP tools: `list`, `read`, `search` (3 only)
- Dual search modes (regex + FTS5)
- Stats, session summaries, delete operations

### Phase 2 — Bounded TS Adapter ✅
- Runtime-only MCP registration with conflict/disable handling
- Forced same-session MCP binding through `tool.execute.before`
- Permissioned Unix-socket transport with identity/protocol health checks
- Bounded request timeouts and a 4-second end-to-end hook-pipeline deadline
- Foreground-only Task capture with lineage repair and a 1 MiB cap
- Bounded, sanitized hint injection

### Phase 3 — TUI ✅ (different architecture)
- Tab/Panel architecture instead of Screen enum
- Inline filtering, settings modal, confirmation dialogs
- Delete operations (sessions and captures)
- Plugin installation flow

### Phase 4 — Polish & Release ✅
- `.goreleaser.yaml` for Linux/macOS amd64/arm64 binaries
- `version` command with ldflags stamping
- README.md with install/uninstall instructions
- Release workflow gates tag creation on formatting, vet, tests, shell syntax, GoReleaser config validation, and a snapshot build

### Phase 5 — Web Dashboard ✅ (new)
- `web` command with printed per-process access URL and opt-in browser launch
- REST API for stats, sessions, captures, search
- Config update via web API
- Embedded static assets
- Random 256-bit token bootstrap, strict session cookie, loopback Host validation, and same-origin enforcement

### Phase 6 — Uninstall System ✅ (new)
- `uninstall` command with `--dry-run`, `--mode`, `--yes`
- Interactive and non-interactive modes
- Preserve-data is the interactive default
- Hash/manifest-verified binary and adapter removal, plus strict recognized-legacy adapter cleanup; no MCP config mutation
- Full mode removes the OS-resolved default config and default/XDG-resolved DB/WAL/SHM files and never recursively removes directories
- Direct `CONTEXT_BRIDGE_CONFIG` and `CONTEXT_BRIDGE_DB` overrides are preserved for manual review
- Compatible Unix-socket daemons receive graceful shutdown before removal; explicit TCP daemons remain externally managed

### Phase 7 — Config Package ✅ (new)
- Config file at `~/.config/context-bridge/config.json`
- Runtime path overrides: `CONTEXT_BRIDGE_CONFIG`, `CONTEXT_BRIDGE_DB`, `CONTEXT_BRIDGE_SOCKET`; uninstall preserves direct override targets as artifacts
- Search mode selection (regex vs fts5)
- Live config updates via web API

### Phase 8 — OpenCode Integration Package ✅ (new)
- `internal/opencode/integration.go`
- Atomic adapter installation with binary path patching
- Private ownership manifest with adapter SHA-256 and optional hosted-installer-owned binary path/SHA-256
- Strict legacy adoption and foreign/modified/symlink refusal
- `integration install|status|uninstall`; no JSON/JSONC config parsing

### Phase 9 — Bounded Persistence ✅ (new)
- 30-day capture retention plus per-root and global count/byte caps
- Pruning at `Open` and inside every `AddCapture` transaction, oldest-first
- Read paths filter expired and soft-deleted rows independently of pruning
- Durable `next_seq` allocator (schema v4) so deleted output numbers are never reused
- Late-parent capture migration into the authoritative root with `call_id` dedup
- Credential and private-block redaction on persist, with a v3 backfill over legacy rows

### Phase 10 — Agent-Facing Result Bounds ✅ (new)
- `internal/search` extracted for literal-safe FTS5 match construction
- Per-tool argument constraints (`Required`, min/max length, integer bounds)
- 128 KiB payload cap with an explicit truncation marker
- `<untrusted-context-bridge-data>` trust boundary with marker neutralization

### Phase 11 — Self-Update ✅ (new)
- `internal/update`: `context-bridge update [--version <tag>] [--check]`
- Checksum verified before any replacement; atomic publish beside the current executable
- Refuses symlinked or non-regular executable paths; linux/darwin × amd64/arm64 only
- Re-runs `integration install` from the new binary, preserving ownership state, and restores the previous binary on failure

### Phase 12 — Documentation Verification ✅ (new)
- `internal/docsverification` asserts README, DESIGN, and `docs/**` match runtime behavior
- Guards against reintroducing stale regex-fallback language and advertising unsupported FTS5 operators
- Release workflow gates tags on `go test ./...`, so documentation drift fails CI

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
**Mitigation**: `spawnInFlight` guards duplicate spawns and polls strict service/protocol readiness every 100ms for up to 3 seconds.
**Residual**: The spawned process is unreferenced and can outlive OpenCode. `context-bridge stop` and uninstall request graceful shutdown for the configured Unix socket, but there is no PID/service-manager ownership. Explicit TCP processes must be stopped externally.

### R4: Session deletion policy
**Decision**: OpenCode lifecycle deletion marks `ended_at` and retains query access; explicit TUI/web deletion marks `deleted_at` and hides the session.
**Tradeoff**: Lifecycle-ended data grows until the user explicitly deletes it. The separation prevents OpenCode cleanup events from silently erasing research.

### R5: Search quality regression
**Decision**: Regex mode (default) + FTS5 mode (optional).  
**Mitigation**: Regex mode preserves case-insensitive substring behavior. FTS5 mode provides more powerful full-text search with snippet highlighting.

### R6: Preview extraction timing
**Decision**: Extract at ingestion. Preview is stored in DB.  
**Tradeoff**: Slightly larger DB, but `ListCaptures` and `RenderHint` are fast — no re-scanning.

### R7: call_id uniqueness
**Decision**: Duplicate captures with same `(session_id, call_id)` are deduplicated on insert.  
**Tradeoff**: Prevents data bloat from retry/replay scenarios. Same call_id always maps to same seq.

### R8: Local transport access boundaries
**Risk**: The optional TCP plugin API is loopback-only but unauthenticated; loopback does not establish process identity or same-UID ownership, and a local user can pre-bind the address. Browser dashboards additionally face cross-origin and DNS-rebinding threats.
**Current mitigation**: The OpenCode adapter uses a Unix socket in a `0700` directory with socket mode `0600`. The dashboard uses a random 256-bit per-process token, an `HttpOnly`/`SameSite=Strict` cookie, loopback Host validation, and exact Origin matching when present. Plugin mutation endpoints enforce JSON/body limits and reject browser Origin headers.
**Residual**: The Unix socket has no application-level credential exchange and privileged processes remain in scope. Explicit TCP `serve --addr` remains unauthenticated. The dashboard access URL is printed, opt-in browser launch can expose its bearer token briefly in process arguments, and browser cookies are host-scoped rather than port-scoped.

### R9: Capture-hook latency budget
**Risk**: Lineage synchronization can perform many sequential SDK and HTTP calls.
**Mitigation**: Every relevant hook establishes one 4-second deadline and passes it through startup, SDK lineage reads, session linking, capture, and hint retrieval; individual bridge requests remain capped at 2 seconds.
**Residual**: Deadline exhaustion intentionally drops that capture or hint so OpenCode work can continue; the deadline bounds hook waiting, not delivery reliability.

---

## 14. Hint Contract

The hint format uses `#seq` numbers for direct lookup:

```
## Prior Context Bridge Outputs

Context Bridge has N persisted subagent outputs in this session tree.
Only opaque output numbers and validated agent identifiers are shown here.
Showing the latest 20 outputs; M earlier outputs are omitted from this hint.
- output #1; agent=grep
- output #2; agent=explore

If a prior output is relevant and these tools are available:
- `context-bridge_read`: read one numbered output in the current session tree
- `context-bridge_search`: search persisted outputs in the current session tree
- `context-bridge_list`: list persisted outputs in the current session tree
The OpenCode adapter enforces the current session ID before each Context Bridge MCP call.

### Regex Search Tips
...mode-specific guidance appended by `appendModeSpecificGuidance`...
```

The count line always reports the full total, while the bullet list shows at most `maxHintCaptures` (20) of the most recent entries; the "Showing the latest…" line appears only when entries were omitted. A trailing tips block is appended for the configured mode — `### Regex Search Tips` or `### FTS5 Search Tips` — so the hint never describes a mode the server is not running.

Free-form descriptions, output content, and session IDs never enter the hint; agent identifiers that fail validation become `unknown`. A session with no captures yields an empty hint, and the adapter appends nothing. Context Bridge has no dependency on Engram.

---

## Summary

| Concern | Decision |
|---|---|
| Persistence | SQLite with FTS5, WAL, `modernc.org/sqlite` (pure Go) |
| MCP transport | stdio via `github.com/mark3labs/mcp-go` (3 agent-facing tools) |
| Plugin transport | HTTP over a `0700`/`0600` Unix socket via `context-bridge serve` (not MCP); explicit loopback TCP is optional and unauthenticated |
| Plugin | Bounded TS adapter: runtime MCP registration, session binding, capture transport |
| Search | Dual mode: regex (default) + FTS5 (configurable) |
| TUI | Tab/Panel architecture with focus management, supports deletes |
| Web | Token-protected loopback dashboard with REST API; prints access URL and disables browser auto-open by default |
| Uninstall | Manifest/hash-checked cleanup; preserve-data default; graceful socket shutdown; direct path overrides preserved; no OpenCode config mutation |
| Config | JSON file with env overrides, live updates via web API |
| Auto-config | Runtime-only MCP injection by the adapter; config files untouched |
| Tool names | Raw MCP: `list`, `read`, `search`; OpenCode IDs: `context-bridge_list`, `context-bridge_read`, `context-bridge_search` |
| Hint format | `#seq` numbers for direct lookup |
| Install | `script/install.sh` or binary + `context-bridge integration install` |

#!/usr/bin/env bash
#
# Local development runner for context-bridge.
#
# Everything runs against an isolated workspace under .dev/ instead of the real
# database in ~/.local/share/context-bridge. That matters here: the dashboard
# and TUI can delete sessions and outputs, so a dev run must never point at
# captured work you care about.
#
#   ./script/dev.sh              # build, then serve the dashboard on 127.0.0.1:7441
#   ./script/dev.sh web --open   # extra flags pass straight through
#   ./script/dev.sh tui          # terminal browser against the same dev database
#   ./script/dev.sh serve        # ingest server on the dev Unix socket
#   ./script/dev.sh mcp          # MCP stdio server
#   ./script/dev.sh seed         # fill the dev database with demo sessions
#   ./script/dev.sh check        # gofmt, vet, and the full test suite
#   ./script/dev.sh build        # build only
#   ./script/dev.sh stop         # stop the dev dashboard and ingest server
#   ./script/dev.sh reset        # delete the dev workspace
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DEV_HOME="${CONTEXT_BRIDGE_DEV_HOME:-$REPO_ROOT/.dev}"
DEV_DB="$DEV_HOME/db/store.db"
DEV_CONFIG="$DEV_HOME/config.json"
DEV_SOCKET="$DEV_HOME/run/bridge.sock"
DEV_ADDR="${CONTEXT_BRIDGE_WEB_ADDR:-127.0.0.1:7441}"
BINARY="$REPO_ROOT/context-bridge"

# Demo content for `seed`. Each agent gets output shaped like the real thing,
# because filler that repeats one sentence per line makes the dashboard, the
# previews, and the search snippets all look broken when they are not.
SEED_AGENTS=(grep explore executor designer triage web general)
SEED_TASKS=(
    "Investigate auth handler regression"
    "Map the capture path end to end"
    "Run the integration suite"
    "Draft the settings panel"
    "Triage flaky tests"
    "Fetch upstream release notes"
    "Summarize the migration plan"
)
SEED_FILES=(
    internal/server/server.go
    internal/store/store.go
    internal/store/analytics.go
    internal/web/api.go
    internal/web/web.go
    internal/mcp/mcp.go
    internal/tui/update.go
    plugin/opencode/context-bridge.ts
)

# ── helpers ─────────────────────────────────────────────────────────────────

# Progress goes to stderr, never stdout: `dev.sh mcp` hands stdout to a
# JSON-RPC client, and a stray banner there corrupts the stream.
log()  { printf '===> %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

require_cmd() {
    for cmd in "$@"; do
        command -v "$cmd" &>/dev/null || die "$cmd is required but not installed"
    done
}

# usage prints the header comment, so the help text can never drift from it.
usage() {
    awk 'NR > 1 {
        if ($0 !~ /^#/) exit
        sub(/^# ?/, "")
        if (started || $0 != "") { started = 1; print }
    }' "${BASH_SOURCE[0]}"
}

# prepare_workspace creates the private dev workspace and exports the
# environment every subcommand needs. The config file is written eagerly
# because an explicitly configured path that does not exist is a hard error.
prepare_workspace() {
    mkdir -p "$DEV_HOME/db" "$DEV_HOME/run"
    # The store refuses an explicit database directory that is group- or
    # world-writable, and the Unix socket directory must be private too.
    chmod 700 "$DEV_HOME" "$DEV_HOME/db" "$DEV_HOME/run"

    if [ ! -f "$DEV_CONFIG" ]; then
        printf '{"search_mode":"regex"}\n' > "$DEV_CONFIG"
        log "Created dev config $DEV_CONFIG"
    fi
    chmod 600 "$DEV_CONFIG"

    export CONTEXT_BRIDGE_DB="$DEV_DB"
    export CONTEXT_BRIDGE_CONFIG="$DEV_CONFIG"
    export CONTEXT_BRIDGE_SOCKET="$DEV_SOCKET"
}

# require_usable_socket fails early on the sun_path limit (108 bytes on Linux,
# 104 on macOS) instead of letting the bind fail with "invalid argument".
require_usable_socket() {
    if [ "${#DEV_SOCKET}" -gt 100 ]; then
        die "the dev socket path is ${#DEV_SOCKET} bytes, which exceeds the Unix socket limit;
       set CONTEXT_BRIDGE_DEV_HOME to a shorter path (current: $DEV_HOME)"
    fi
}

dev_version() {
    local described
    if described="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null)"; then
        printf 'dev-%s' "$described"
    else
        printf 'dev'
    fi
}

build() {
    require_cmd go
    cd "$REPO_ROOT"
    log "Building context-bridge ($(dev_version))..."
    go build -ldflags "-X main.version=$(dev_version)" -o "$BINARY" ./cmd/context-bridge
}

print_workspace() {
    printf '     database: %s\n' "$DEV_DB" >&2
    printf '     config:   %s\n' "$DEV_CONFIG" >&2
    printf '     socket:   %s\n' "$DEV_SOCKET" >&2
}

# iso_days_ago prints an RFC3339 UTC timestamp N days and H hours back, using
# whichever date(1) dialect this machine ships.
iso_days_ago() {
    local days="$1" hour="$2"
    if date -u -d "-${days} days" +%Y-%m-%d >/dev/null 2>&1; then
        printf '%sT%02d:15:00Z' "$(date -u -d "-${days} days" +%Y-%m-%d)" "$hour"
    else
        printf '%sT%02d:15:00Z' "$(date -u -v-"${days}"d +%Y-%m-%d)" "$hour"
    fi
}

# ── commands ────────────────────────────────────────────────────────────────

cmd_web() {
    build
    prepare_workspace
    log "Starting dashboard on $DEV_ADDR"
    print_workspace
    printf '     no data yet? run: %s seed\n' "./script/dev.sh" >&2
    # exec keeps this pid, so recording it now lets `dev.sh stop` find the
    # dashboard even when it was started in the background.
    echo $$ > "$DEV_HOME/web.pid"
    # The default --addr comes first so a caller-supplied --addr overrides it.
    exec "$BINARY" web --addr "$DEV_ADDR" "$@"
}

cmd_tui() {
    build
    prepare_workspace
    log "Starting TUI"
    print_workspace
    exec "$BINARY" tui "$@"
}

cmd_serve() {
    build
    prepare_workspace
    require_usable_socket
    log "Starting ingest server on $DEV_SOCKET"
    print_workspace
    exec "$BINARY" serve --socket "$DEV_SOCKET" "$@"
}

cmd_mcp() {
    build
    prepare_workspace
    # No banner: this speaks JSON-RPC on stdout.
    exec "$BINARY" mcp "$@"
}

# cmd_stop shuts down everything this script may have started. It deliberately
# skips prepare_workspace: stopping should never recreate a workspace that was
# just reset.
cmd_stop() {
    stop_web
    require_usable_socket
    if [ ! -S "$DEV_SOCKET" ]; then
        log "No dev ingest server is running on $DEV_SOCKET"
        return
    fi
    [ -x "$BINARY" ] || die "binary not built yet; run $0 build"
    "$BINARY" stop --socket "$DEV_SOCKET"
    log "Stopped the dev ingest server"
}

# stop_web kills a dashboard started from this workspace. The pid is only
# trusted when it still points at this repo's binary, so a recycled pid can
# never take down an unrelated process.
stop_web() {
    local pidfile="$DEV_HOME/web.pid" pid
    [ -f "$pidfile" ] || return 0
    pid="$(cat "$pidfile" 2>/dev/null || true)"
    rm -f "$pidfile"

    case "$pid" in
        ''|*[!0-9]*) return 0 ;;
    esac
    kill -0 "$pid" 2>/dev/null || return 0
    if [ "$(readlink -f "/proc/$pid/exe" 2>/dev/null)" != "$(readlink -f "$BINARY" 2>/dev/null)" ]; then
        return 0
    fi

    kill "$pid" 2>/dev/null || return 0
    log "Stopped the dev dashboard (pid $pid)"
}

cmd_build() {
    build
    log "Built $BINARY"
}

cmd_check() {
    require_cmd go gofmt
    cd "$REPO_ROOT"

    log "Checking formatting..."
    local unformatted
    unformatted="$(gofmt -l cmd internal plugin script)"
    if [ -n "$unformatted" ]; then
        printf '%s\n' "$unformatted" >&2
        die "the files above need gofmt"
    fi

    log "Vetting..."
    go vet ./...

    log "Testing..."
    go test ./...

    log "All checks passed"
}

cmd_reset() {
    if [ ! -d "$DEV_HOME" ]; then
        log "Nothing to reset; $DEV_HOME does not exist"
        return
    fi
    stop_web
    if [ -S "$DEV_SOCKET" ] && [ -x "$BINARY" ]; then
        CONTEXT_BRIDGE_CONFIG="$DEV_CONFIG" CONTEXT_BRIDGE_DB="$DEV_DB" \
            "$BINARY" stop --socket "$DEV_SOCKET" >/dev/null 2>&1 || true
    fi
    rm -rf "$DEV_HOME"
    log "Removed $DEV_HOME"
}

# cmd_seed drives the real ingest path: it starts the server on the dev socket,
# posts captures through /capture the way the OpenCode adapter does, and shuts
# it down again. Seeding through the API rather than SQL keeps the demo data
# honest — it goes through normalization, redaction, and FTS indexing.
cmd_seed() {
    require_cmd curl
    build
    prepare_workspace
    require_usable_socket

    if [ -S "$DEV_SOCKET" ]; then
        die "the dev socket is already in use; stop the running server first ($0 stop)"
    fi

    log "Starting a temporary ingest server..."
    "$BINARY" serve --socket "$DEV_SOCKET" >"$DEV_HOME/seed.log" 2>&1 &
    local server_pid=$!
    # shellcheck disable=SC2064  # expand the pid now, not at trap time
    trap "kill $server_pid 2>/dev/null || true" EXIT

    local ready=0 attempt
    for attempt in $(seq 1 50); do
        if curl --silent --max-time 1 --unix-socket "$DEV_SOCKET" \
            http://context-bridge/health >/dev/null 2>&1; then
            ready=1
            break
        fi
        kill -0 "$server_pid" 2>/dev/null || break
        sleep 0.1
    done
    if [ "$ready" -ne 1 ]; then
        cat "$DEV_HOME/seed.log" >&2 || true
        die "the ingest server did not become ready"
    fi

    local sessions=6 captures=0 session index agent task payload
    local session_number capture_number

    for session_number in $(seq 1 "$sessions"); do
        session="ses_dev_$(printf '%02d' "$session_number")f3a9c$session_number"
        post_json /events \
            "{\"type\":\"session.created\",\"properties\":{\"info\":{\"id\":\"$session\"}}}"

        local capture_count=$(( (session_number * 3) % 12 + 4 ))
        for capture_number in $(seq 1 "$capture_count"); do
            index=$(( capture_number + session_number ))
            agent="${SEED_AGENTS[$(( index % ${#SEED_AGENTS[@]} ))]}"
            task="${SEED_TASKS[$(( index % ${#SEED_TASKS[@]} ))]}"

            seed_body "$agent" "$session_number" "$capture_number"

            payload=$(printf '{"parent_session_id":"%s","call_id":"call_%s_%d","agent":"%s","description":"%s","content":"%s","captured_at":"%s"}' \
                "$session" "$session" "$capture_number" "$agent" "$task" "$SEED_BODY" \
                "$(iso_days_ago "$(( (capture_number * session_number) % 20 ))" "$(( (capture_number * 7 + session_number * 3) % 24 ))")")

            post_json /capture "$payload"
            captures=$(( captures + 1 ))
        done
    done

    log "Seeded $sessions sessions and $captures outputs"
    "$BINARY" stop --socket "$DEV_SOCKET" >/dev/null 2>&1 || true
    trap - EXIT
    wait "$server_pid" 2>/dev/null || true
    rm -f "$DEV_HOME/seed.log"
    log "Run '$0' to browse them in the dashboard"
}

# seed_line appends one line to SEED_BODY as a JSON-escaped newline. Content must
# stay free of double quotes and backslashes so the payload needs no escaping.
seed_line() {
    if [ -z "$SEED_BODY" ]; then
        SEED_BODY="$1"
    else
        SEED_BODY="$SEED_BODY\\n$1"
    fi
}

# seed_body builds output that looks like what each agent type actually returns,
# with a size that varies per capture so the size histogram has something to show.
seed_body() {
    local agent="$1" session_number="$2" capture_number="$3"
    local extra=$(( (capture_number * 5 + session_number) % 14 + 3 ))
    local n file
    SEED_BODY=""

    case "$agent" in
        grep)
            seed_line "Searched for the authentication entry points across the repo."
            seed_line ""
            for n in $(seq 1 "$extra"); do
                file="${SEED_FILES[$(( (n + session_number) % ${#SEED_FILES[@]} ))]}"
                seed_line "$file:$(( (n * 37 + session_number * 11) % 900 + 20 )): authentication token check before the handler runs"
            done
            seed_line ""
            seed_line "$extra occurrences across $(( extra / 3 + 1 )) files."
            seed_line "The token guard lives in Routes(); everything else delegates to the store."
            ;;
        explore)
            seed_line "Mapped the capture path from the OpenCode adapter down to SQLite."
            seed_line ""
            seed_line "- plugin/opencode/context-bridge.ts posts the finished Task output"
            seed_line "- internal/server/server.go validates the payload and body limits"
            seed_line "- internal/store/store.go normalizes, redacts, and assigns the sequence"
            seed_line "- captures_fts stays in sync through the AFTER INSERT trigger"
            seed_line ""
            for n in $(seq 1 "$extra"); do
                file="${SEED_FILES[$(( (n * 3 + capture_number) % ${#SEED_FILES[@]} ))]}"
                seed_line "  read $file ($(( (n * 53) % 400 + 60 )) lines)"
            done
            seed_line ""
            seed_line "No package other than internal/store writes to the captures table."
            ;;
        executor)
            seed_line "go test ./..."
            seed_line ""
            for n in $(seq 1 "$extra"); do
                seed_line "ok      context-bridge/internal/pkg$(( n % 9 ))        0.$(( (n * 137) % 900 + 10 ))s"
            done
            if [ $(( (session_number + capture_number) % 3 )) -eq 0 ]; then
                seed_line "FAIL    context-bridge/internal/tui           0.041s"
                seed_line ""
                seed_line "--- FAIL: TestSearchScopeToggle (0.00s)"
                seed_line "    update_test.go:142: expected the all-session scope, got session"
                seed_line "ERROR: transient timeout while fetching the module index"
            else
                seed_line ""
                seed_line "All packages passed in $(( extra + 4 ))s."
            fi
            ;;
        designer)
            seed_line "Draft for the settings panel."
            seed_line ""
            seed_line "- Radio group for the shared search engine: regex or fts5"
            seed_line "- Copy has to say the setting is global, not per search"
            seed_line "- Saving applies to the dashboard immediately; TUI and MCP read it on next start"
            seed_line ""
            for n in $(seq 1 "$extra"); do
                seed_line "  state $n: $(( (n * 7) % 3 )) pending, keyboard reachable, aria-pressed mirrors the model"
            done
            seed_line ""
            seed_line "TODO(cleanup): remove the legacy shim once the modal ships"
            ;;
        triage)
            seed_line "Flaky test triage over the last 20 CI runs."
            seed_line ""
            for n in $(seq 1 "$extra"); do
                seed_line "TestCase$(( (n * 3 + session_number) % 40 ))   $(( (n * 5) % 6 + 1 )) failures   $( [ $(( n % 2 )) -eq 0 ] && printf 'timing dependent' || printf 'network flake' )"
            done
            seed_line ""
            seed_line "Recommend quarantining the socket probe until it stops depending on wall clock."
            seed_line "ERROR: transient timeout while fetching the run history"
            ;;
        web)
            seed_line "Fetched the upstream release notes."
            seed_line ""
            for n in $(seq 1 "$extra"); do
                seed_line "v0.$(( (session_number + n) % 4 )).$(( n % 9 )) - $( [ $(( n % 2 )) -eq 0 ] && printf 'retention is enforced on every read path' || printf 'fts5 queries are escaped literally' )"
            done
            seed_line ""
            seed_line "Nothing in the changelog changes the authentication flow."
            ;;
        *)
            seed_line "Summary of the migration plan."
            seed_line ""
            seed_line "1. Ship the analytics endpoints behind the existing token guard"
            seed_line "2. Move the dashboard off the CDN so it renders with no network"
            seed_line "3. Backfill tests for the new aggregates before touching the TUI"
            seed_line ""
            for n in $(seq 1 "$extra"); do
                seed_line "  note $n: $( [ $(( n % 2 )) -eq 0 ] && printf 'authentication stays a loopback token, unchanged' || printf 'retention window keeps every aggregate honest' )"
            done
            seed_line ""
            seed_line "Open question: keep /api/stats once analytics lands? Yes, it is the cheap poll."
            ;;
    esac
}

post_json() {
    local path="$1" payload="$2" response
    response="$(curl --silent --show-error --max-time 5 \
        --unix-socket "$DEV_SOCKET" \
        -H 'Content-Type: application/json' \
        -X POST -d "$payload" "http://context-bridge$path")" || die "POST $path failed"
    case "$response" in
        *'"ok":true'*) ;;
        *) die "POST $path rejected: $response" ;;
    esac
}

# ── dispatch ────────────────────────────────────────────────────────────────

command="${1:-web}"
[ $# -gt 0 ] && shift

case "$command" in
    web)            cmd_web "$@" ;;
    tui)            cmd_tui "$@" ;;
    serve)          cmd_serve "$@" ;;
    mcp)            cmd_mcp "$@" ;;
    stop)           cmd_stop "$@" ;;
    seed)           cmd_seed "$@" ;;
    build)          cmd_build "$@" ;;
    check)          cmd_check "$@" ;;
    reset)          cmd_reset "$@" ;;
    help|-h|--help) usage ;;
    *)              printf 'error: unknown command %s\n\n' "$command" >&2; usage >&2; exit 1 ;;
esac

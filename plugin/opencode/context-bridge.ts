import type { Plugin } from "@opencode-ai/plugin";
import type { PluginContext as PluginContextV2 } from "@opencode-ai/plugin/v2/promise";
import { realpathSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join, normalize } from "node:path";

function canonicalAbsolutePath(name: string, value: string): string {
  const path = value.trim();
  if (!path) throw new Error(`[context-bridge] ${name} is required`);
  if (!isAbsolute(path)) {
    throw new Error(`[context-bridge] ${name} must be an absolute path`);
  }
  return normalize(path);
}

function defaultConfigHome(): string {
  if (process.platform === "darwin") {
    return canonicalAbsolutePath("user config directory", join(homedir(), "Library", "Application Support"));
  }
  if (process.platform === "win32") {
    const appData = process.env.APPDATA?.trim();
    if (!appData) throw new Error("[context-bridge] APPDATA is required on Windows");
    return canonicalAbsolutePath("APPDATA", appData);
  }
  const xdgConfigHome = process.env.XDG_CONFIG_HOME?.trim();
  if (xdgConfigHome) {
    return canonicalAbsolutePath("XDG_CONFIG_HOME", xdgConfigHome);
  }
  return canonicalAbsolutePath("user config directory", join(homedir(), ".config"));
}

function resolveSocketPath(): string {
  const explicit = process.env.CONTEXT_BRIDGE_SOCKET?.trim();
  if (explicit) {
    return canonicalAbsolutePath("CONTEXT_BRIDGE_SOCKET", explicit);
  }
  return canonicalAbsolutePath(
    "Context Bridge socket",
    join(defaultConfigHome(), "context-bridge", "bridge.sock"),
  );
}

const BRIDGE_SOCKET = resolveSocketPath();
const BRIDGE_URL = "http://context-bridge";
const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";
const BRIDGE_SERVICE = "context-bridge";
const BRIDGE_PROTOCOL = 1;
const HEALTH_TIMEOUT_MS = 400;
const REQUEST_TIMEOUT_MS = 2_000;
const STARTUP_TIMEOUT_MS = 3_000;
const STARTUP_POLL_MS = 100;
const HOOK_PIPELINE_TIMEOUT_MS = 4_000;
const MAX_CAPTURE_BYTES = 1024 * 1024;
const MAX_HINT_BYTES = 64 * 1024;
const MAX_DESCRIPTION_BYTES = 512;
const MAX_AGENT_BYTES = 128;
const TRUNCATION_MARKER = "\n\n[context-bridge: output truncated]";
const HINT_TRUNCATION_MARKER = "\n\n[context-bridge: hint truncated]";
const CONTEXT_BRIDGE_TOOLS = new Set([
  "context-bridge_list",
  "context-bridge_read",
  "context-bridge_search",
]);
const TASK_TOOLS = new Set(["task", "Task"]);

let spawnInFlight: Promise<void> | null = null;

type JsonObject = Record<string, unknown>;
type BridgeProbe = "ready" | "unreachable" | "incompatible";

function isRecord(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value) ?? "[unserializable output]";
  } catch {
    return "[unserializable output]";
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function settleBefore<T>(promise: Promise<T>, deadline: number): Promise<T> {
  const remaining = deadline - Date.now();
  if (remaining <= 0) throw new Error("context-bridge operation deadline exceeded");
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("context-bridge operation deadline exceeded")), remaining);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

function localMCPIsEquivalent(value: unknown): boolean {
  if (!isRecord(value)) return false;
  const command = value.command;
  if (!Array.isArray(command) || command.length !== 2) return false;
  const [executable, subcommand] = command;
  if (typeof executable !== "string") return false;
  const resolveExecutable = (candidate: string): string | null => {
    const located = candidate === "context-bridge" ? Bun.which(candidate) : candidate;
    if (typeof located !== "string") return null;
    try {
      return realpathSync(located);
    } catch {
      return located;
    }
  };
  const expectedExecutable = resolveExecutable(BRIDGE_BIN);
  const configuredExecutable = resolveExecutable(executable);
  return (
    value.type === "local" &&
    subcommand === "mcp" &&
    typeof expectedExecutable === "string" &&
    configuredExecutable === expectedExecutable
  );
}

function isExplicitlyDisabled(value: unknown): boolean {
  return isRecord(value) && value.enabled === false;
}

async function bridgeFetch(
  path: string,
  init: RequestInit = {},
  timeoutMs = REQUEST_TIMEOUT_MS,
  deadline = Date.now() + timeoutMs * 2 + 75,
): Promise<JsonObject | null> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const remaining = deadline - Date.now();
    if (remaining <= 0) return null;
    try {
      const headers = new Headers(init.headers);
      if (init.body !== undefined && !headers.has("Content-Type")) {
        headers.set("Content-Type", "application/json");
      }
      const res = await fetch(`${BRIDGE_URL}${path}`, {
        ...init,
        headers,
        unix: BRIDGE_SOCKET,
        signal: AbortSignal.timeout(Math.max(1, Math.min(timeoutMs, remaining))),
      });
      if (res.ok) {
        const value: unknown = await res.json();
        return isRecord(value) ? value : null;
      }
    } catch {
      // The second bounded attempt handles transient local startup/transport races.
    }
    if (attempt === 0 && deadline - Date.now() > 75) await delay(75);
  }
  return null;
}

async function probeBridge(deadline = Date.now() + HEALTH_TIMEOUT_MS): Promise<BridgeProbe> {
  const remaining = deadline - Date.now();
  if (remaining <= 0) return "unreachable";
  try {
    const response = await fetch(`${BRIDGE_URL}/health`, {
      unix: BRIDGE_SOCKET,
      signal: AbortSignal.timeout(Math.max(1, Math.min(HEALTH_TIMEOUT_MS, remaining))),
    });
    if (!response.ok) return "incompatible";
    const health: unknown = await response.json();
    if (
      isRecord(health) &&
      health.ok === true &&
      health.service === BRIDGE_SERVICE &&
      health.protocol === BRIDGE_PROTOCOL
    ) {
      return "ready";
    }
    return "incompatible";
  } catch {
    return "unreachable";
  }
}

async function ensureBridge(initialProbe?: BridgeProbe, deadline = Date.now() + STARTUP_TIMEOUT_MS): Promise<boolean> {
  initialProbe ??= await probeBridge(deadline);
  if (initialProbe === "ready") return true;
  if (initialProbe === "incompatible") return false;
  if (spawnInFlight) {
    try {
      await settleBefore(spawnInFlight, deadline);
    } catch {
      return false;
    }
    return (await probeBridge(deadline)) === "ready";
  }

  spawnInFlight = (async () => {
    try {
      const child = Bun.spawn([BRIDGE_BIN, "serve", "--socket", BRIDGE_SOCKET], {
        stdout: "ignore",
        stderr: "ignore",
        stdin: "ignore",
      });
      child.unref();
      const startupDeadline = Math.min(deadline, Date.now() + STARTUP_TIMEOUT_MS);
      while (Date.now() < startupDeadline) {
        await delay(STARTUP_POLL_MS);
        if ((await probeBridge(startupDeadline)) !== "unreachable") return;
      }
    } catch {
      // Graceful degradation: OpenCode remains usable when the backend is absent.
    } finally {
      spawnInFlight = null;
    }
  })();

  try {
    await settleBefore(spawnInFlight, deadline);
  } catch {
    return false;
  }
  return (await probeBridge(deadline)) === "ready";
}

function extractOutputText(output: any): string | null {
  if (typeof output === "string") return output;
  if (typeof output?.output === "string") return output.output;
  if (output?.output) return safeStringify(output.output);
  return null;
}

function boundedUTF8(content: string, maxBytes: number, marker: string): string {
  const encoder = new TextEncoder();
  const encoded = encoder.encode(content);
  if (encoded.byteLength <= maxBytes) return content;

  const markerBytes = encoder.encode(marker);
  let end = maxBytes - markerBytes.byteLength;
  while (end > 0 && (encoded[end] & 0xc0) === 0x80) end--;
  return new TextDecoder().decode(encoded.subarray(0, end)) + marker;
}

function boundedCapture(content: string): string {
  return boundedUTF8(content, MAX_CAPTURE_BYTES, TRUNCATION_MARKER);
}

// ── shared runtime core ─────────────────────────────────────────────────────
//
// Every behaviour below is expressed against plain data so the V1 hook pipeline
// and the V2 plugin runtime drive identical logic. Only the wiring differs.

let warnedIncompatibleBridge = false;

async function ensureCompatibleBridge(deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS): Promise<boolean> {
  const probe = await probeBridge(deadline);
  if (probe === "incompatible") {
    if (!warnedIncompatibleBridge) {
      warnedIncompatibleBridge = true;
      console.warn(`[context-bridge] ${BRIDGE_SOCKET} is occupied by an incompatible service; capture is disabled.`);
    }
    return false;
  }
  return ensureBridge(probe, deadline);
}

/** Resolves a session's parent chain, newest first. */
type SessionLookup = (sessionID: string, deadline: number) => Promise<{ id: string; parentID: string } | null>;

/**
 * Registers the session and every ancestor with the backend so captures always
 * attach to the root of the tree, no matter which nested subagent produced them.
 */
async function syncSessionLineage(
  sessionID: string,
  deadline: number,
  lookup: SessionLookup,
): Promise<boolean> {
  const chain: Array<{ id: string; parentID: string }> = [];
  const seen = new Set<string>();
  let currentID = sessionID;

  for (let depth = 0; depth < 64; depth++) {
    if (Date.now() >= deadline) return false;
    if (seen.has(currentID)) return false;
    seen.add(currentID);
    const info = await lookup(currentID, deadline);
    if (!info) return false;
    chain.push(info);
    if (!info.parentID) break;
    currentID = info.parentID;
    if (depth === 63) return false;
  }

  for (const info of chain.reverse()) {
    const result = await bridgeFetch("/events", {
      method: "POST",
      body: JSON.stringify({ type: "session.created", properties: { info } }),
    }, REQUEST_TIMEOUT_MS, deadline);
    if (result?.ok !== true) return false;
  }
  return true;
}

/** Identity of the tool call that produced an output. */
type TaskIdentity = {
  sessionID: string;
  callID: string;
};

/** The bounded payload persisted for one finished subagent. */
type CapturedTask = {
  agent: string;
  description: string;
  content: string;
  childSessionID: string;
};

/**
 * Normalises a finished tool call into a capture payload, or null when it is
 * not a foreground subagent result worth persisting. Background tasks are
 * skipped because their tool output is a placeholder; the real result arrives
 * later as a synthetic message that never reaches this boundary.
 */
function readCompletedTask(tool: string, args: any, output: any): CapturedTask | null {
  if (!TASK_TOOLS.has(tool)) return null;
  if (output?.metadata?.background === true) return null;

  const agent = args?.subagent_type ?? args?.subagentType;
  const content = extractOutputText(output);
  if (typeof agent !== "string" || !content || content.trim().length === 0) return null;

  return {
    agent: boundedUTF8(agent, MAX_AGENT_BYTES, "…"),
    description:
      typeof args?.description === "string" ? boundedUTF8(args.description, MAX_DESCRIPTION_BYTES, "…") : "",
    content: boundedCapture(content),
    childSessionID: typeof output?.metadata?.sessionId === "string" ? output.metadata.sessionId : "",
  };
}

/** Persists one finished subagent output through the backend capture endpoint. */
async function captureCompletedTask(
  identity: TaskIdentity,
  task: CapturedTask,
  lookup: SessionLookup,
): Promise<void> {
  if (!identity.sessionID) return;
  const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
  if (!(await ensureCompatibleBridge(deadline))) return;
  if (!(await syncSessionLineage(identity.sessionID, deadline, lookup))) return;

  if (task.childSessionID) {
    const linked = await bridgeFetch(
      "/events",
      {
        method: "POST",
        body: JSON.stringify({
          type: "session.created",
          properties: { info: { id: task.childSessionID, parentID: identity.sessionID } },
        }),
      },
      REQUEST_TIMEOUT_MS,
      deadline,
    );
    if (linked?.ok !== true) return;
  }

  await bridgeFetch(
    "/capture",
    {
      method: "POST",
      body: JSON.stringify({
        parent_session_id: identity.sessionID,
        child_session_id: task.childSessionID,
        call_id: identity.callID,
        agent: task.agent,
        description: task.description,
        content: task.content,
        captured_at: new Date().toISOString(),
      }),
    },
    REQUEST_TIMEOUT_MS,
    deadline,
  );
}

/** Forwards a session lifecycle event to the backend. */
async function forwardSessionEvent(event: unknown): Promise<void> {
  const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
  if (!(await ensureCompatibleBridge(deadline))) return;
  await bridgeFetch("/events", { method: "POST", body: JSON.stringify(event) }, REQUEST_TIMEOUT_MS, deadline);
}

/** Fetches the bounded hint appended to a session's system prompt. */
async function fetchHint(sessionID: string): Promise<string | null> {
  const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
  if (!(await ensureCompatibleBridge(deadline))) return null;

  const res = await bridgeFetch(
    `/hint?session_id=${encodeURIComponent(sessionID)}`,
    { method: "GET" },
    REQUEST_TIMEOUT_MS,
    deadline,
  );
  const rawHint = res?.text;
  if (typeof rawHint !== "string" || rawHint.length === 0) return null;
  return boundedUTF8(rawHint, MAX_HINT_BYTES, HINT_TRUNCATION_MARKER);
}

// ── V2 runtime ──────────────────────────────────────────────────────────────
//
// The published V2 context (`@opencode-ai/plugin/v2/promise`) exposes agent,
// aisdk, catalog, command, integration, reference, and skill. Capture needs the
// tool and event domains, which the V2 plan lists as agreed but which the
// runtime does not implement yet. The types below describe only what this
// plugin consumes; setup feature-detects them and stays inert until they land,
// leaving the V1 pipeline authoritative. When a runtime does provide them, V2
// takes ownership and the V1 hooks stand down so nothing runs twice.

type V2ToolEvent = {
  readonly tool?: string;
  readonly sessionID?: string;
  readonly callID?: string;
  readonly args?: any;
  readonly output?: any;
};

type V2SessionEvent = {
  readonly type?: string;
  readonly properties?: unknown;
};

type V2FutureDomains = {
  readonly tool?: {
    readonly hook?: (
      name: "execute.before" | "execute.after",
      callback: (event: V2ToolEvent) => Promise<void> | void,
    ) => Promise<unknown>;
  };
  readonly event?: {
    readonly subscribe?: (type: string) => AsyncIterable<V2SessionEvent> | undefined;
  };
};

let v2OwnsCapture = false;

// The V2 context has no session API, so lineage is limited to the pair the tool
// event carries. Reporting an empty parent is safe rather than lossy: the
// backend never demotes a session that already has a parent, and the child edge
// is registered separately before the capture is posted, so the root still
// resolves correctly for nested subagents.
const v2SessionLookup: SessionLookup = async (sessionID) => ({ id: sessionID, parentID: "" });

async function registerV2Capture(domains: V2FutureDomains): Promise<boolean> {
  const hook = domains.tool?.hook;
  if (typeof hook !== "function") return false;

  await hook("execute.after", async (event) => {
    const tool = typeof event?.tool === "string" ? event.tool : "";
    const task = readCompletedTask(tool, event?.args, event?.output);
    if (!task) return;
    const sessionID = typeof event?.sessionID === "string" ? event.sessionID : "";
    if (!sessionID) return;
    await captureCompletedTask(
      { sessionID, callID: typeof event?.callID === "string" ? event.callID : "" },
      task,
      v2SessionLookup,
    );
  });
  return true;
}

async function registerV2Events(domains: V2FutureDomains): Promise<void> {
  const subscribe = domains.event?.subscribe;
  if (typeof subscribe !== "function") return;

  for (const type of ["session.created", "session.deleted"]) {
    const stream = subscribe(type);
    if (!stream || typeof (stream as any)[Symbol.asyncIterator] !== "function") continue;
    void (async () => {
      try {
        for await (const event of stream) await forwardSessionEvent(event);
      } catch {
        // A closed stream ends the subscription; capture is unaffected.
      }
    })();
  }
}

const setup = async (context: PluginContextV2): Promise<void> => {
  const domains = context as PluginContextV2 & V2FutureDomains;
  if (!(await registerV2Capture(domains))) return;

  // Ownership flips only after the capture hook is installed, so a runtime that
  // exposes a partial tool domain never silences the V1 pipeline.
  v2OwnsCapture = true;
  await registerV2Events(domains);
};

// ── V1 runtime ──────────────────────────────────────────────────────────────
//
// Still the only pipeline that can register the MCP server, bind the session ID
// onto Context Bridge tool calls, capture subagent output, and append the hint
// to the system prompt.

const server: Plugin = async (pluginInput) => {
  let bindMcpSession = false;
  let warnedMcpConflict = false;

  const lookup: SessionLookup = async (sessionID, deadline) => {
    try {
      const response = await settleBefore(
        pluginInput.client.session.get({
          path: { id: sessionID },
          query: { directory: pluginInput.directory },
        }),
        deadline,
      );
      const info = response.data;
      if (!info || typeof info.id !== "string") return null;
      return { id: info.id, parentID: typeof info.parentID === "string" ? info.parentID : "" };
    } catch {
      return null;
    }
  };

  return {
    config: async (config) => {
      config.mcp ??= {};
      const registry = config.mcp as Record<string, unknown>;
      const current = registry[BRIDGE_SERVICE];
      if (current === undefined) {
        registry[BRIDGE_SERVICE] = {
          type: "local",
          command: [BRIDGE_BIN, "mcp"],
          enabled: true,
        };
        bindMcpSession = true;
        return;
      }
      if (isExplicitlyDisabled(current)) {
        bindMcpSession = false;
        return;
      }
      if (localMCPIsEquivalent(current)) {
        bindMcpSession = true;
        return;
      }
      bindMcpSession = false;
      if (!warnedMcpConflict) {
        warnedMcpConflict = true;
        console.warn(
          "[context-bridge] MCP entry 'context-bridge' conflicts with the installed adapter; leaving it unchanged.",
        );
      }
    },

    event: async ({ event }) => {
      if (v2OwnsCapture) return;
      if (event.type !== "session.created" && event.type !== "session.deleted") return;
      await forwardSessionEvent(event);
    },

    "tool.execute.before": async (hookInput, output) => {
      if (!bindMcpSession || !CONTEXT_BRIDGE_TOOLS.has(hookInput.tool)) return;
      if (!isRecord(output.args)) {
        throw new Error("[context-bridge] MCP arguments must be an object");
      }
      output.args.session_id = hookInput.sessionID;
    },

    "tool.execute.after": async (hookInput, output) => {
      if (v2OwnsCapture) return;
      const task = readCompletedTask(hookInput.tool, hookInput.args, output);
      if (!task || !hookInput.sessionID) return;
      await captureCompletedTask({ sessionID: hookInput.sessionID, callID: hookInput.callID }, task, lookup);
    },

    "experimental.chat.system.transform": async (hookInput, output) => {
      if (!bindMcpSession) return;
      if (!hookInput.sessionID) return;
      const hint = await fetchHint(hookInput.sessionID);
      if (!hint) return;

      if (output.system.length > 0) {
        output.system[output.system.length - 1] += `\n\n${hint}`;
      } else {
        output.system.push(hint);
      }
    },
  };
};

export default {
  id: "context-bridge",
  setup,
  server,
};

export { server as ContextBridge, setup as ContextBridgeSetup };

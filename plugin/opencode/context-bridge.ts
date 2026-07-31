import type { Plugin } from "@opencode-ai/plugin";
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

const server: Plugin = async (pluginInput) => {
  let bindMcpSession = false;
  let warnedMcpConflict = false;
  let warnedIncompatibleBridge = false;

  const ensureCompatibleBridge = async (deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS): Promise<boolean> => {
    const probe = await probeBridge(deadline);
    if (probe === "incompatible") {
      if (!warnedIncompatibleBridge) {
        warnedIncompatibleBridge = true;
        console.warn(`[context-bridge] ${BRIDGE_SOCKET} is occupied by an incompatible service; capture is disabled.`);
      }
      return false;
    }
    return ensureBridge(probe, deadline);
  };

  const syncSessionLineage = async (sessionID: string, deadline: number): Promise<boolean> => {
    const chain: Array<{ id: string; parentID: string }> = [];
    const seen = new Set<string>();
    let currentID = sessionID;

    for (let depth = 0; depth < 64; depth++) {
      if (Date.now() >= deadline) return false;
      if (seen.has(currentID)) return false;
      seen.add(currentID);
      try {
        const response = await settleBefore(
          pluginInput.client.session.get({
            path: { id: currentID },
            query: { directory: pluginInput.directory },
          }),
          deadline,
        );
        const info = response.data;
        if (!info || typeof info.id !== "string") return false;
        const parentID = typeof info.parentID === "string" ? info.parentID : "";
        chain.push({ id: info.id, parentID });
        if (!parentID) break;
        currentID = parentID;
      } catch {
        return false;
      }
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
      if (event.type !== "session.created" && event.type !== "session.deleted") return;
      const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
      if (!(await ensureCompatibleBridge(deadline))) return;
      await bridgeFetch("/events", { method: "POST", body: JSON.stringify(event) }, REQUEST_TIMEOUT_MS, deadline);
    },

    "tool.execute.before": async (hookInput, output) => {
      if (!bindMcpSession || !CONTEXT_BRIDGE_TOOLS.has(hookInput.tool)) return;
      if (!isRecord(output.args)) {
        throw new Error("[context-bridge] MCP arguments must be an object");
      }
      output.args.session_id = hookInput.sessionID;
    },

    "tool.execute.after": async (hookInput, output) => {
      if (hookInput.tool !== "Task" && hookInput.tool !== "task") return;
      if (output?.metadata?.background === true) return;

      const agent = hookInput.args?.subagent_type ?? hookInput.args?.subagentType;
      const rawContent = extractOutputText(output);
      if (typeof agent !== "string" || !rawContent || rawContent.trim().length === 0 || !hookInput.sessionID) return;
      const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
      if (!(await ensureCompatibleBridge(deadline))) return;
      if (!(await syncSessionLineage(hookInput.sessionID, deadline))) return;

      const childSessionID = typeof output?.metadata?.sessionId === "string" ? output.metadata.sessionId : "";
      if (childSessionID) {
        const linked = await bridgeFetch(
          "/events",
          {
            method: "POST",
            body: JSON.stringify({
              type: "session.created",
              properties: { info: { id: childSessionID, parentID: hookInput.sessionID } },
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
            parent_session_id: hookInput.sessionID,
            child_session_id: childSessionID,
            call_id: hookInput.callID,
            agent: boundedUTF8(agent, MAX_AGENT_BYTES, "…"),
            description:
              typeof hookInput.args?.description === "string"
                ? boundedUTF8(hookInput.args.description, MAX_DESCRIPTION_BYTES, "…")
                : "",
            content: boundedCapture(rawContent),
            captured_at: new Date().toISOString(),
          }),
        },
        REQUEST_TIMEOUT_MS,
        deadline,
      );
    },

    "experimental.chat.system.transform": async (hookInput, output) => {
      if (!bindMcpSession) return;
      if (!hookInput.sessionID) return;
      const deadline = Date.now() + HOOK_PIPELINE_TIMEOUT_MS;
      if (!(await ensureCompatibleBridge(deadline))) return;

      const res = await bridgeFetch(
        `/hint?session_id=${encodeURIComponent(hookInput.sessionID)}`,
        { method: "GET" },
        REQUEST_TIMEOUT_MS,
        deadline,
      );
      const rawHint = res?.text;
      if (typeof rawHint !== "string" || rawHint.length === 0) return;
      const hint = boundedUTF8(rawHint, MAX_HINT_BYTES, HINT_TRUNCATION_MARKER);

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
  server,
};

export { server as ContextBridge };

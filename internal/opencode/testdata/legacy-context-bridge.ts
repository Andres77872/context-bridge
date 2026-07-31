import type { Plugin } from "@opencode-ai/plugin";

const BRIDGE_PORT = parseInt(process.env.CONTEXT_BRIDGE_PORT ?? "7438", 10);
const BRIDGE_ADDR = process.env.CONTEXT_BRIDGE_ADDR ?? `127.0.0.1:${BRIDGE_PORT}`;
const BRIDGE_URL = `http://${BRIDGE_ADDR}`;
const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";

let spawnInFlight: Promise<void> | null = null;

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value);
  } catch {
    return "[unserializable output]";
  }
}

async function bridgeFetch(path: string, init: RequestInit = {}) {
  try {
    const headers = init.method === "GET" ? init.headers ?? {} : { "Content-Type": "application/json", ...(init.headers ?? {}) };
    const res = await fetch(`${BRIDGE_URL}${path}`, { ...init, headers });
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

async function isBridgeRunning(): Promise<boolean> {
  try {
    const res = await fetch(`${BRIDGE_URL}/health`, { signal: AbortSignal.timeout(400) });
    return res.ok;
  } catch {
    return false;
  }
}

async function ensureBridge(): Promise<boolean> {
  if (await isBridgeRunning()) return true;
  if (spawnInFlight) {
    await spawnInFlight;
    return isBridgeRunning();
  }

  spawnInFlight = (async () => {
    try {
      Bun.spawn([BRIDGE_BIN, "serve"], { stdout: "ignore", stderr: "ignore", stdin: "ignore" });
      await new Promise((resolve) => setTimeout(resolve, 600));
    } catch {
      // graceful degradation — plugin remains usable even when backend is absent
    } finally {
      spawnInFlight = null;
    }
  })();

  await spawnInFlight;
  return isBridgeRunning();
}

function extractOutputText(output: any): string | null {
  if (typeof output === "string") return output;
  if (typeof output?.output === "string") return output.output;
  if (output?.output) return safeStringify(output.output);
  return null;
}

const server: Plugin = async (input, options) => {
  await ensureBridge();

  return {
    event: async ({ event }) => {
      if (event.type !== "session.created" && event.type !== "session.deleted") return;
      if (!(await ensureBridge())) return;
      await bridgeFetch("/events", { method: "POST", body: JSON.stringify(event) });
    },

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

    "experimental.chat.system.transform": async (input, output) => {
      if (!input.sessionID) return;
      if (!(await ensureBridge())) return;

      const res = await bridgeFetch(`/hint?session_id=${encodeURIComponent(input.sessionID)}`, { method: "GET" });
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

export default {
  id: "context-bridge",
  server,
};

export { server as ContextBridge };

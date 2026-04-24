///!dep zx@8.8.5 sha512-SNgDF5L0gfN7FwVOdEFguY3orU5AkfFZm9B5YSHog/UDHv+lvmd82ZAsOenOkQixigwH2+yyH198AwNdKhj+RA==
///
/// ringmaster: an MCP server (stdio) that dispatches the `run_discover` tool
/// by invoking `nix build` on the co-located synthetic sandbox-agent flake.
///
/// Protocol: JSON-RPC 2.0, line-delimited, one message per line.
/// Supported methods: initialize, tools/list, tools/call.
/// stdout is the protocol channel — all diagnostics go to stderr.
///
/// POC scope: one hardcoded tool (`run_discover`), single-backend, no broker,
/// no real sandbox (sandcastle is stubbed pending amarbel-llc/eng#41),
/// serialized invocations (single in-flight mutex).

import { $ } from "zx";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { createInterface } from "node:readline";

// Quiet zx. Protocol channel is stdout, must stay clean.
$.verbose = false;

// Log to stderr with a prefix.
const log = (msg: string) => process.stderr.write(`[ringmaster] ${msg}\n`);

// Resolve the synthetic flake directory relative to this script's location.
// When packaged by buildZxScriptFromFile, __dirname points into the store;
// we want the *source* flake next to the script. For the POC, that's the
// current working directory of the script at build time. Use a hardcoded
// fallback if we can't resolve: $PWD when the user ran the script.
const SCRIPT_DIR = (() => {
  try {
    return dirname(fileURLToPath(import.meta.url));
  } catch {
    return process.cwd();
  }
})();

// In the packaged form, the flake is NOT in the store. Ringmaster operates
// against the synthetic flake located in the clown repo (or wherever the
// caller points). For the POC, we look in $CLOWN_POC_FLAKE_DIR (set by
// run.sh) or fall back to a sibling of the script source.
const FLAKE_DIR = process.env.CLOWN_POC_FLAKE_DIR ?? SCRIPT_DIR;

log(`started; FLAKE_DIR=${FLAKE_DIR}`);

// ---- JSON-RPC types ------------------------------------------------------

type Id = string | number | null;
interface JsonRpcRequest {
  jsonrpc: "2.0";
  id?: Id;
  method: string;
  params?: unknown;
}
interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: Id;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

const respond = (id: Id, result: unknown) => {
  const msg: JsonRpcResponse = { jsonrpc: "2.0", id, result };
  process.stdout.write(JSON.stringify(msg) + "\n");
};

const respondError = (id: Id, code: number, message: string, data?: unknown) => {
  const msg: JsonRpcResponse = {
    jsonrpc: "2.0",
    id,
    error: { code, message, ...(data !== undefined ? { data } : {}) },
  };
  process.stdout.write(JSON.stringify(msg) + "\n");
};

// ---- MCP method handlers -------------------------------------------------

const handleInitialize = (id: Id, _params: unknown) => {
  respond(id, {
    protocolVersion: "2024-11-05",
    capabilities: { tools: {} },
    serverInfo: { name: "ringmaster-poc", version: "0.0.1" },
  });
};

const TOOL_RUN_DISCOVER = {
  name: "run_discover",
  description:
    "Dispatch the discover subagent inside a Nix-derivation sandbox (POC: sandbox stubbed).",
  inputSchema: {
    type: "object",
    properties: {
      prompt: {
        type: "string",
        description: "Task description for the subagent.",
      },
      workspace_ref: {
        type: "string",
        description:
          "Absolute path to the workspace the subagent should operate on. Defaults to the current working directory.",
      },
    },
    required: ["prompt"],
  },
};

const handleToolsList = (id: Id, _params: unknown) => {
  respond(id, { tools: [TOOL_RUN_DISCOVER] });
};

// Serialize tool calls so nix-build invocations don't step on each other.
let inflight: Promise<void> = Promise.resolve();

const handleToolsCall = (id: Id, params: any) => {
  const name = params?.name;
  const args = params?.arguments ?? {};

  if (name !== "run_discover") {
    return respondError(id, -32601, `unknown tool: ${name}`);
  }

  const prompt: string = args.prompt ?? "";
  const workspaceRef: string = resolve(args.workspace_ref ?? process.cwd());
  const invocationId = randomUUID();

  if (!prompt) {
    return respondError(id, -32602, "prompt is required");
  }

  log(`tools/call run_discover id=${invocationId} workspace=${workspaceRef}`);

  // Enqueue behind the current in-flight invocation.
  inflight = inflight.then(async () => {
    const start = Date.now();
    try {
      const flake = `${FLAKE_DIR}#sandbox-agent`;

      // Run nix build against the synthetic flake, overriding the workspace
      // input to point at the caller-supplied path.
      const result = await $`nix build ${flake} \
        --override-input workspace path:${workspaceRef} \
        --no-link \
        --print-out-paths \
        --print-build-logs`.nothrow();

      const durationMs = Date.now() - start;

      if (result.exitCode !== 0) {
        log(`nix build failed exit=${result.exitCode}`);
        respond(id, {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                status: "agent_error",
                exit_code: result.exitCode,
                invocation_id: invocationId,
                duration_ms: durationMs,
                stderr: result.stderr.slice(-4096),
              }),
            },
          ],
        });
        return;
      }

      const outRef = result.stdout.trim().split("\n").pop() ?? "";
      log(`success out=${outRef} duration=${durationMs}ms`);
      respond(id, {
        content: [
          {
            type: "text",
            text: JSON.stringify({
              status: "success",
              exit_code: 0,
              out_ref: outRef,
              invocation_id: invocationId,
              duration_ms: durationMs,
            }),
          },
        ],
      });
    } catch (err) {
      log(`unexpected error: ${err}`);
      respondError(id, -32603, `internal error: ${err}`);
    }
  });
};

// ---- stdin loop ----------------------------------------------------------

const rl = createInterface({ input: process.stdin });

rl.on("line", (line: string) => {
  if (!line.trim()) return;

  let msg: JsonRpcRequest;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    log(`bad json: ${err}`);
    return;
  }

  const id = msg.id ?? null;

  try {
    switch (msg.method) {
      case "initialize":
        return handleInitialize(id, msg.params);
      case "notifications/initialized":
        // Client acknowledgment of initialize; no response expected.
        return;
      case "tools/list":
        return handleToolsList(id, msg.params);
      case "tools/call":
        return handleToolsCall(id, msg.params);
      default:
        return respondError(id, -32601, `method not found: ${msg.method}`);
    }
  } catch (err) {
    log(`handler error: ${err}`);
    respondError(id, -32603, `internal error: ${err}`);
  }
});

rl.on("close", () => {
  log("stdin closed, waiting for in-flight work");
  inflight.finally(() => process.exit(0));
});

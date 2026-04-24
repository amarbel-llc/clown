///!dep zx@8.8.5 sha512-SNgDF5L0gfN7FwVOdEFguY3orU5AkfFZm9B5YSHog/UDHv+lvmd82ZAsOenOkQixigwH2+yyH198AwNdKhj+RA==
///
/// zz-pocs/0003 driver.
///
/// Workflow:
///   1. Prepare a fresh mitmproxy state dir so the per-run CA is isolated.
///   2. Spawn mitmdump with the clown-broker addon, binding to 127.0.0.1
///      on an ephemeral port.
///   3. Wait for it to be ready (the CA bundle appears on disk).
///   4. Export CLOWN_BROKER_{HOST,PORT,CA_PEM} so the flake's builtins.getEnv
///      reads them via --impure.
///   5. Run `nix build .#default --impure`.
///   6. Print the probe's results.txt and the broker's flow log.
///   7. Tear mitmdump down.
///
/// NOTE: we do NOT use zx's `$` template for mitmdump — zx 8.8's process
/// wrangler uses `ps` internally and chokes on modern macOS `ps` output
/// ("Malformed grid: row has more columns than headers"). Node's raw
/// child_process.spawn works fine and doesn't go through that code path.

import { spawn } from "node:child_process";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fs, os, path, sleep } from "zx";

const execFileP = promisify(execFile);

const POC_DIR = path.dirname(new URL(import.meta.url).pathname);
const BROKER_DIR = path.join(POC_DIR, "broker");
const ADDON = path.join(BROKER_DIR, "addon.py");
const POLICY = path.join(BROKER_DIR, "policy.json");

const HOST = "127.0.0.1";
// 0 => let the kernel pick a free ephemeral port. We read mitmdump's
// stdout after launch to learn which port it actually bound to.
const LISTEN_PORT_ARG = "0";

const STATE_DIR = await fs.mkdtemp(path.join(os.tmpdir(), "clown-broker-poc-"));
const FLOW_LOG = path.join(STATE_DIR, "flows.log");
const STDOUT_LOG = path.join(STATE_DIR, "mitmdump.stdout");
const STDERR_LOG = path.join(STATE_DIR, "mitmdump.stderr");
const CA_PEM = path.join(STATE_DIR, ".mitmproxy", "mitmproxy-ca-cert.pem");

console.log(`state dir: ${STATE_DIR}`);

// mitmproxy writes its CA into ~/.mitmproxy by default; redirect it by
// pointing HOME at our state dir. mitmdump respects that convention.
const childEnv = { ...process.env, HOME: STATE_DIR };

console.log(`spawning mitmdump on ${HOST}:<ephemeral>`);

const stdoutFd = fs.openSync(STDOUT_LOG, "w");
const stderrFd = fs.openSync(STDERR_LOG, "w");

// Spawn mitmdump. detached: false to keep it in our process group; we
// accept that if the parent dies abnormally, mitmdump may leak, and
// handle explicit reaping via SIGTERM/SIGKILL in shutdownBroker. Adding
// PYTHONUNBUFFERED=1 so mitmdump's stdout/stderr flush to our fds
// promptly (not block-buffered when stdio is a file).
const mitm = spawn(
  "mitmdump",
  [
    "--listen-host",
    HOST,
    "--listen-port",
    LISTEN_PORT_ARG,
    "-s",
    ADDON,
    "--set",
    `policy_file=${POLICY}`,
    "--set",
    "flow_detail=2",
    "-w",
    FLOW_LOG,
  ],
  {
    env: { ...childEnv, PYTHONUNBUFFERED: "1" },
    // Keep stdin as a pipe (do not close it) — mitmdump 12.x exits when it
    // detects no-controlling-input. "pipe" gives us a writable handle we
    // can leave open until shutdown.
    stdio: ["pipe", stdoutFd, stderrFd],
    detached: false,
  },
);

let mitmExited = false;
let mitmExitCode: number | null = null;
let mitmExitSignal: string | null = null;
const mitmExitPromise = new Promise<void>((resolve) => {
  mitm.on("exit", (code, signal) => {
    mitmExited = true;
    mitmExitCode = code;
    mitmExitSignal = signal;
    resolve();
  });
});

// Since we spawned with detached: false, the child is in our process
// group. Signal just the child directly; mitmdump handles its own
// worker shutdown on SIGTERM.
const signalGroup = (sig: NodeJS.Signals) => {
  if (mitmExited) return;
  try {
    mitm.kill(sig);
  } catch {
    /* already gone */
  }
};

// Graceful shutdown: SIGTERM the group, wait up to `timeoutMs`, then
// SIGKILL. Resolves once the child actually reaps or we give up.
const shutdownBroker = async (timeoutMs = 5000): Promise<void> => {
  if (mitmExited) return;
  signalGroup("SIGTERM");
  const raced = await Promise.race([
    mitmExitPromise.then(() => "exited" as const),
    sleep(timeoutMs).then(() => "timeout" as const),
  ]);
  if (raced === "timeout" && !mitmExited) {
    signalGroup("SIGKILL");
    // Give the kernel a beat to deliver.
    await Promise.race([mitmExitPromise, sleep(1000)]);
  }
};

// Belt + suspenders: any exit path from here on kills the broker.
let shuttingDown = false;
const shutdownOnSignal = async (signal: NodeJS.Signals, exitCode: number) => {
  if (shuttingDown) return;
  shuttingDown = true;
  console.error(`\n[run.ts] caught ${signal}, shutting down broker...`);
  await shutdownBroker();
  process.exit(exitCode);
};
process.on("SIGINT", () => {
  void shutdownOnSignal("SIGINT", 130);
});
process.on("SIGTERM", () => {
  void shutdownOnSignal("SIGTERM", 143);
});
process.on("SIGHUP", () => {
  void shutdownOnSignal("SIGHUP", 129);
});

process.on("uncaughtException", async (err) => {
  console.error("\n[run.ts] uncaughtException:", err);
  await shutdownBroker(2000);
  process.exit(1);
});
process.on("unhandledRejection", async (reason) => {
  console.error("\n[run.ts] unhandledRejection:", reason);
  await shutdownBroker(2000);
  process.exit(1);
});

// Last-resort sync kill on normal exit (can't await in 'exit' handler).
process.on("exit", () => {
  if (!mitmExited) {
    try {
      mitm.kill("SIGKILL");
    } catch {
      /* already gone */
    }
  }
});

// Poll stdout for the bound port. mitmdump 12.x logs:
//   HTTP(S) proxy listening at 127.0.0.1:NNNNN.
// (no URL scheme, just host:port).
const discoverPort = async (): Promise<number> => {
  const deadline = Date.now() + 20_000;
  const re = /listening at\s+[^:\s]+:(\d+)/i;
  while (Date.now() < deadline) {
    if (mitmExited) {
      throw new Error(
        `mitmdump exited before binding (code=${mitmExitCode}). See ${STDOUT_LOG} / ${STDERR_LOG}.`,
      );
    }
    try {
      const text = fs.readFileSync(STDOUT_LOG, "utf8");
      const m = text.match(re);
      if (m) {
        return Number(m[1]);
      }
    } catch {
      /* file may not exist yet */
    }
    await sleep(100);
  }
  throw new Error("could not discover mitmdump port in 20s");
};

// Wait up to 20s for the CA bundle + a reachable port.
const waitForCaAndReachable = async (port: number): Promise<void> => {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (mitmExited) {
      throw new Error(
        `mitmdump exited early (code=${mitmExitCode}). See ${STDOUT_LOG} / ${STDERR_LOG}.`,
      );
    }

    if (fs.existsSync(CA_PEM)) {
      try {
        const { stdout } = await execFileP("curl", [
          "-sS",
          "--max-time",
          "1",
          "-o",
          "/dev/null",
          "-w",
          "%{http_code}",
          "-x",
          `http://${HOST}:${port}`,
          "http://httpbun.com/get",
        ]);
        if (stdout.trim().length > 0) {
          return;
        }
      } catch {
        /* retry */
      }
    }
    await sleep(250);
  }
  throw new Error("mitmdump did not become ready in 20s");
};

const dumpLogs = (label: string) => {
  console.log(`\n=== ${label}: ${STDOUT_LOG} ===`);
  try {
    console.log(fs.readFileSync(STDOUT_LOG, "utf8"));
  } catch {
    console.log("(unreadable)");
  }
  console.log(`\n=== ${label}: ${STDERR_LOG} ===`);
  try {
    console.log(fs.readFileSync(STDERR_LOG, "utf8"));
  } catch {
    console.log("(unreadable)");
  }
};

let port = 0;
try {
  try {
    port = await discoverPort();
  } catch (err) {
    dumpLogs("mitmdump startup failure");
    throw err;
  }
  console.log(`mitmdump bound to ${HOST}:${port}`);

  try {
    await waitForCaAndReachable(port);
  } catch (err) {
    dumpLogs("mitmdump readiness failure");
    throw err;
  }
  console.log(`broker is up; CA at ${CA_PEM}`);

  // Host-side smoke test.
  try {
    const { stdout } = await execFileP("curl", [
      "-sS",
      "-x",
      `http://${HOST}:${port}`,
      "--cacert",
      CA_PEM,
      "-o",
      "/dev/null",
      "-w",
      "%{http_code}",
      "--max-time",
      "5",
      "https://httpbun.com/get",
    ]);
    console.log(`host-side smoke test: http_code=${stdout.trim()}`);
  } catch (err) {
    console.error(`host-side smoke test failed: ${err}`);
  }

  // Run nix build.
  console.log("\n=== nix build ===");
  const buildEnv = {
    ...process.env,
    CLOWN_BROKER_HOST: HOST,
    CLOWN_BROKER_PORT: String(port),
    CLOWN_BROKER_CA_PEM: CA_PEM,
  };

  const build = spawn(
    "nix",
    [
      "build",
      "--impure",
      "--print-build-logs",
      "--no-link",
      "--print-out-paths",
      ".#default",
    ],
    {
      env: buildEnv,
      cwd: POC_DIR,
      stdio: ["ignore", "pipe", "inherit"],
    },
  );

  // If we catch a signal mid-build, kill nix-build too.
  const killBuildGroup = () => {
    try {
      build.kill("SIGTERM");
    } catch {
      /* already gone */
    }
  };
  const savedInt = process.listeners("SIGINT").slice();
  const savedTerm = process.listeners("SIGTERM").slice();
  process.on("SIGINT", killBuildGroup);
  process.on("SIGTERM", killBuildGroup);

  let buildStdout = "";
  build.stdout.on("data", (chunk: Buffer) => {
    const text = chunk.toString("utf8");
    buildStdout += text;
    process.stdout.write(text);
  });

  const buildExit: number = await new Promise((resolve) => {
    build.on("exit", (code) => resolve(code ?? 1));
  });

  // Remove the per-build signal handlers; keep the broker ones.
  process.off("SIGINT", killBuildGroup);
  process.off("SIGTERM", killBuildGroup);
  void savedInt; // kept for linter, no-op
  void savedTerm;

  if (buildExit !== 0) {
    dumpLogs("mitmdump logs (build failed)");
    console.error(`nix build failed, exit=${buildExit}`);
    process.exit(buildExit);
  }

  const outRef = buildStdout.trim().split("\n").pop() ?? "";
  console.log(`\n=== ${outRef}/results.txt ===`);
  console.log(fs.readFileSync(path.join(outRef, "results.txt"), "utf8"));

  dumpLogs("mitmdump logs");

  if (fs.existsSync(FLOW_LOG)) {
    const stats = fs.statSync(FLOW_LOG);
    console.log(`\n=== flow log: ${FLOW_LOG} (${stats.size} bytes) ===`);
  }
} finally {
  await shutdownBroker();
  if (!mitmExited) {
    console.error(
      `[run.ts] warning: broker did not exit after graceful+forceful shutdown ` +
        `(code=${mitmExitCode} signal=${mitmExitSignal})`,
    );
  } else {
    console.error(
      `[run.ts] broker exited cleanly (code=${mitmExitCode} signal=${mitmExitSignal})`,
    );
  }
}

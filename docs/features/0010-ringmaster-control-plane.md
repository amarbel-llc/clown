---
status: exploring
date: 2026-05-18
promotion-criteria: clown's `--backend=circus` flow uses ringmaster RPC end-to-end on macOS and Linux; multi-instance start/stop verified; no flat-file pid/port/model artifacts remain on disk
---

# Ringmaster Control Plane

## Problem Statement

Today, circus tracks its single running llama-server via three flat files (`~/.local/state/circus/llama-server.{pid,port}`) plus an implicit "the currently loaded model". Truth lives on disk, drifts on crashes/kills, encodes no friendly model identity, and admits only one instance at a time.

Clown's `--backend=circus` lifecycle automation (start/stop/status/mismatch detection) needs richer signals: which alias is running, what model is loaded behind it, whether the daemon is actually responsive, and — soon — how many instances exist concurrently. Reading flat files cannot answer those reliably.

This FDR introduces **ringmaster**, a long-lived per-user daemon that owns the registry of running llama-server children in memory, and **circus**, rewritten as a JSON-RPC client over a Unix domain socket. Truth lives in process state; flat files are deleted.

## Interface

A new `ringmaster` daemon is started by a home-manager service unit (launchd agent on macOS, systemd user service on Linux). It listens on a Unix domain socket at `~/.local/state/circus/control.sock` and speaks newline-delimited JSON-RPC 2.0.

`circus` is rewritten as a CLI client of that socket. Existing subcommands keep their names but become thin RPCs:

```sh
circus start qwen3-coder                       # StartInstance{alias:qwen3-coder, model:qwen3-coder}
circus start qwen3-coder --alias coder-32k     # StartInstance{alias:coder-32k,  model:qwen3-coder, args:["--ctx-size","32768"]}
circus stop qwen3-coder                        # StopInstance{alias:qwen3-coder}
circus stop --all                              # StopAll{}
circus list                                    # ListInstances{}
circus status qwen3-coder                      # GetInstance{alias} + live health probe
circus address qwen3-coder                     # GetInstance{alias} → prints "host:port"
circus models                                  # ListAvailableModels{}
circus download qwen2.5-coder                  # DownloadModel{name}
```

Each llama-server child is launched with `--alias <name>` so its `/v1/models` endpoint reports a friendly id matching ringmaster's registry entry.

The bind address of llama-server is configurable per-start: `--bind 127.0.0.1` (default), `--bind 0.0.0.0`, or `--bind <tailnet-ip>`. Remote access over tailscale falls out of routine tailnet routing — no proxy work in ringmaster.

## Examples

```sh
# One-time setup via home-manager:
#   programs.ringmaster.enable = true;
# After `home-manager switch`, the service unit keeps ringmaster running.

# Day-to-day:
$ circus start qwen3-coder
ringmaster: started qwen3-coder on 127.0.0.1:43219 (pid 91234)

$ circus start qwen3-coder --alias coder-32k --ctx-size 32768
ringmaster: started coder-32k on 127.0.0.1:43221 (pid 91241)

$ circus list
ALIAS         MODEL         PORT   PID     UPTIME
qwen3-coder   qwen3-coder   43219  91234   2m
coder-32k     qwen3-coder   43221  91241   12s

$ circus address qwen3-coder
127.0.0.1:43219

$ circus stop coder-32k
ringmaster: stopped coder-32k

# Programmatic use (clown):
$ clown --provider=opencode --backend=circus --circus-alias=qwen3-coder
# clown calls GetInstance{alias=qwen3-coder} → 127.0.0.1:43219, points opencode at it
```

## Limitations

**Loose proxy only.** Ringmaster is a control plane, not an HTTP data plane. Clients (clown, curl, browsers) connect directly to each llama-server's TCP port for inference. A strict-proxy mode (single client-facing endpoint, ringmaster routes by model header) is deferred to a future FDR.

**No backwards compatibility.** The pre-ringmaster flat-file layout (`llama-server.pid`, `llama-server.port`) is removed wholesale. No compat shim is shipped — circus and clown both update in the same release. This is acceptable because circus is a young, internal tool with no external consumers.

**Single host, per-user.** The control socket is in `~/.local/state/circus/`, not under `/var/run/`. Two users on the same host run two ringmasters; that's intentional. Multi-host coordination is out of scope; tailscale remoting works by reaching the llama-server's TCP port through the tailnet, not by exposing ringmaster.

**Bootstrap depends on home-manager.** Users who don't run home-manager must launch `ringmaster daemon` manually. There is no auto-launch-on-first-circus-call; if the socket is missing, circus fails fast with a pointer to the home-manager option. This is a deliberate trade for simplicity over magic.

**`circus download` streams large blobs over RPC.** Model GGUFs are multi-GB. Streaming a download via the RPC channel adds protocol overhead vs. a direct fetch. Acceptable for v1; if it becomes a problem, ringmaster can hand back a temp-path the client writes to.

**RPC stability is internal-only.** circus, clown, and any future first-party clients are the only consumers. The JSON-RPC schema may change between releases without a deprecation window. If we ever publish a stable RPC for third parties, that's a separate FDR.

## More Information

- Prior art for home-manager service modules: `amarbel-llc/piggy`'s pivy-agent and ssh-agent-mux setup (launchd + systemd user units).
- Prior art for clown-protocol JSON framing: `internal/pluginhost/handshake.go` uses newline-terminated text; ringmaster will use the same wrapping for JSON-RPC payloads.
- The motivation for this FDR is FDR-0011 (clown `--backend=circus` lifecycle), which depends on these capabilities.
- Related issue: anomalyco/opencode#16885 (separate concern, fixed in clown by a symlink workaround in commit 9a0d6f9).

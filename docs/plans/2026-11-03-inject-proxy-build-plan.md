# InjectProxy build plan (clown#336): the model-API injection proxy for confined sessions

Status: build plan, 2026-11-03, not code; waits for the operator's go.
Design and rulings: `2026-11-03-oauth-credential-confined.md` (B approved,
H1 holder, fallback switch, probe results). Everything below is clown's
unless marked; no spinclass change is needed for v1.

## What the probe fixed

From the dummy-bearer smoke and the operator's two card runs:

| Fact | Consequence for the build |
|---|---|
| claude accepts a loopback `http` base URL with a placeholder in `CLAUDE_CODE_OAUTH_TOKEN`, no login prompt | the unit env is `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>`, `CLAUDE_CODE_OAUTH_TOKEN=clown-placeholder-not-a-credential`, `ANTHROPIC_API_KEY=` (present, empty) |
| the OAuth variable yields the `anthropic-beta` list with `oauth-2025-04-20`; the list varies per run and claude version | relay `anthropic-beta` untouched; never allowlist beta values |
| paths: `HEAD /api/hello` (no `Authorization`), `POST /v1/messages?beta=true` | path allowlist below; `count_tokens` added by design |
| a 118 KB request for a one-word prompt; `stream-json` streamed back | stream both directions, buffer nothing |
| on 401 claude retries twice then stops | pass the upstream status through; no proxy-side retry |
| the real token through the proxy: 200 | the injection works as designed |
| no placeholder in the probe run's transcripts | nothing to scrub in-unit |

## Components (all in the clown frontend)

| Component | Where | What |
|---|---|---|
| `InjectProxy` | `internal/*/agentscope/inject.go` (sibling of `PortForward` and `EgressProxy` in `egress.go`) | an `http.Server` on a unix socket, handler = `httputil.ReverseProxy` to `https://api.anthropic.com` with `FlushInterval = -1`, a `Director` that sets `Host`, replaces `Authorization` with `Bearer <token>` read per request from the token source, drops nothing else; a path/method allowlist checked before the Director; a `Transport` field for tests (fake API). Logs method, path, status, bytes, elapsed; never a header or body. |
| token source | `internal/*/agentscope/tokensource.go` | reads a 0600 file at the convention path per request (a few hundred bytes from tmpfs); refuses a world-readable file, an empty file, or a file not owned by the uid; trims one trailing newline; never logs the value. |
| launch wiring | `cmd/clown/agentscope.go`, `startFrontendEndpoints` | after the egress proxy: create `model.sock` in the unit's socket dir, start `InjectProxy`, append `Relay{Listen: "127.0.0.1:<InUnitPortBase+k>", Socket: model.sock}` to `launch.Forwards` (the relay already splices every entry), record `launch.ModelSocket`. When the token source is missing: the fallback switch decides (below). |
| unit env | `internal/*/agentscope/unit.go`, `environment()` | when `l.ModelSocket != ""`: set `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN` (placeholder), `ANTHROPIC_API_KEY=` last, overriding anything the frontend env or the spec passed; scrub `ANTHROPIC_AUTH_TOKEN` and any inherited `CLAUDE_CODE_OAUTH_TOKEN`. Today the `ANTHROPIC_` and `CLAUDE_CODE_` prefixes pass through (FDR 0020 table); with the proxy on, a real credential in the frontend env must not enter the unit. |
| credential bind | `cmd/clown/agentscope.go`, `stageAgentHome` | when the proxy is on: do not bind the operator's `.credentials.json`; keep the empty seed file (`touch`). When falling back: bind read-only as today. |
| egress | `cmd/clown/agentscope.go`, `agentEgressAllowlist` | when the proxy is on and the spec has no `egress_allow`: use the default minus `api.anthropic.com`, `console.anthropic.com`, `platform.claude.com`, `claude.ai` (operator decision 4 in the design note; proposed as part of this build). A spec list is the sweatfile's and is left alone. |
| capability and result | `internal/*/agentscope/spec.go` | `"model-inject-proxy"` in `Capabilities` when the proxy ran; `Result.ModelCredential` = `"proxy"` or `"login-file"` so spinclass can see which path a unit got (read-only field, spec version unchanged). |
| 401 notice | `cmd/clown/agentscope.go` | on the first 401/403 from the upstream per launch: one stderr line and one `agent-relay.log` line naming the session and "model credential rejected; rotate the entry and run piggy secrets reconcile". A troupe message to the holders is a follow-up (clown#322 territory), not v1. |
| banner | the catalog-trim banner / system-prompt fragment | one line: "model credential: injected by the frontend (placeholder in the unit)" or "model credential: operator login file (read-only bind; fallback)". |

**Peer check.** `authorizeUnitPeer` on every accepted connection, exactly as
`PortForward`: `SO_PEERCRED` pid, cgroup must be the unit's. The in-unit
relay is the peer the frontend sees, which is in the unit's cgroup.

**Path and method allowlist** (checked before injection; a miss is a 403
from the proxy with the path in the log, no upstream call):

| Method | Path |
|---|---|
| `HEAD` | `/api/hello` (forwarded without `Authorization`; it carries none) |
| `POST` | `/v1/messages` (any query string) |
| `POST` | `/v1/messages/count_tokens` |

A launch-time env `CLOWN_MODEL_PROXY_PERMISSIVE=1` (frontend only, never
passed into the unit) forwards any path under `/v1/` or `/api/`, logged,
for the next probe of a new claude version; it is not a profile knob.

**Streaming and connections.** `ReverseProxy` with `FlushInterval = -1`
flushes every chunk (SSE); request bodies stream to the upstream; HTTP/1.1
keep-alive is allowed on the unit side (the probe's connection-close was
a shortcut, not a requirement). Upstream: TLS with the system roots, from
the frontend's own network namespace, never through the unit's egress.
Hop-by-hop headers are stripped by `ReverseProxy`; `Content-Length` is
recomputed; everything else is forwarded as received.

**Token source path convention.**
`$XDG_RUNTIME_DIR/clown/credentials/anthropic-oauth` (0600, tmpfs, hidden
from every unit by `ProtectHome=tmpfs`, FDR 0020). The directory is
clown's (`$XDG_RUNTIME_DIR/clown/` already holds the agent, idle, serve
and control sockets). A piggy secrets manifest entry (circus's lane)
targets that path; `piggy secrets reconcile` writes it at a card-present
moment, once per boot. clown reads it per request and never caches beyond
one request, so deleting the file revokes at once. An env override
`CLOWN_MODEL_TOKEN_FILE` (frontend only) exists for the bats case and for
an operator who keeps the file elsewhere.

**Fallback switch** (ruling 3: default fall back). Recommendation: a
**profile** knob, not a sweatfile one.

- The credential is per operator and host, not per repo; a sweatfile is
  per repo and would need the same line in every repo's hierarchy to
  change the posture once. The profile is the launch posture and is
  pinned by digest, so flipping it is a visible operator act (a resume
  shows the diff), which is what "switch to refusing after a few weeks"
  wants.
- Knob: `CLOWN_MODEL_CREDENTIAL` in the profile's env table, values
  `proxy-or-login` (default; absent means this) and `proxy-only`. An env
  knob needs no `ProtocolVersion` bump (like `CLOWN_DISALLOWED_TOOLS`).
  spinclass can still refuse a spawn whose result says `login-file` if a
  sweatfile wants that later; nothing to build on their side now.
- Behaviour: token file present and readable: proxy, no bind. Absent or
  unreadable under `proxy-or-login`: one stderr line "no model credential
  at <path>; binding the operator login read-only (fallback)", bind as
  today, proxy not started, result says `login-file`. Under `proxy-only`:
  refuse the launch before the unit starts, naming the path and
  `piggy secrets reconcile`; the result file carries the error.
- Unconfined launches are untouched: the proxy exists only under
  `--agent-scope`.

## Test plan

1. **Unit tests, fake API** (`inject_test.go`): an `httptest` TLS server as
   the upstream through the `Transport` field; cases: the bearer is
   replaced and the placeholder never reaches the upstream; `anthropic-beta`
   and the other request headers arrive unchanged; a chunked SSE response
   is flushed per event (read with a deadline per chunk); the request body
   streams (a body larger than any buffer, hashed on both ends); a path off
   the allowlist is 403 with no upstream call; `HEAD /api/hello` passes
   without `Authorization`; 401 passes through and logs once; a non-unit
   peer is refused (the `PeerCgroup` seam, as `TestPortForwardServesOnlyTheUnit`).
2. **Canary redaction test**: a canary token string through the token
   source, every log sink (`slog` handler captured), every error path, the
   launch plan JSON and the result file; assert the string appears nowhere.
   Mirrors `TestLaunchPlanJSON_RedactsSecrets`.
3. **Token source tests**: missing, empty, world-readable, wrong owner,
   trailing newline, replaced between two reads (revocation).
4. **Launch wiring tests** (`agentscope_test.go` style, fake `systemctl`):
   the unit env carries the placeholder and the loopback base URL and no
   `ANTHROPIC_AUTH_TOKEN`; the credential bind is absent with a token file
   and present without one under `proxy-or-login`; `proxy-only` refuses;
   the egress default drops the API hosts with the proxy on; the
   capability and `ModelCredential` are reported.
5. **Host bats, `verify-agent-scope`** (`agent_scope.bats`): with
   `CLOWN_MODEL_TOKEN_FILE` pointing at a temp file holding a fake token
   and a fake upstream (the frontend-only transport override), one
   confined `claude -p` that: reads `/proc/1/environ` and the shell's own
   env inside the unit and finds only the placeholder (never the fake
   token); finds `.credentials.json` empty; gets an answer from the fake
   upstream through the proxy (proves the relay, the socket, the peer
   check); and, in a second launch with the file removed, falls back to
   the login bind and the result says `login-file`.
6. **Live round at the card** (operator): `piggy secrets reconcile` with
   the manifest entry, one confined spawn on `worker`, the banner says
   "injected", a real answer, the report-style transcript grep.

## What the operator does at the card

1. Add the manifest entry in circus (piggy secrets, target
   `$XDG_RUNTIME_DIR/clown/credentials/anthropic-oauth`); piggy to confirm
   a runtime-dir target is accepted.
2. Run `piggy secrets reconcile` once (one PIN); after every reboot again.
3. Rotation: new `claude setup-token`, new ebox entry, reconcile.
4. Nothing per session.

## InjectProxy and V3: parallel or serial

Independent in code: InjectProxy touches the egress/env/credential side of
`startFrontendEndpoints`, `environment()` and `stageAgentHome`; V3 touches
the in-unit moxy side (moxyfile generation, `MOXIN_PATH`,
`InUnitMoxyfile`, a profile field with a `ProtocolVersion` bump announced
to spinclass, and the ratification package for `folio`/`rg`). They can be
built in parallel by two clown sessions; merges serialize on the repo's
queue either way. Recommendation: **InjectProxy first, then V3, in this
session**, because InjectProxy is smaller, has no cross-repo protocol
change, and unblocks the sessions that cannot log in today; V3 needs the
spinclass announcement and the operator's `folio`/`rg` decision first. If
the coordinator wants both at once, V3 goes to a second clown worker with
the protocol bump as its first act.

Rough size: InjectProxy two days (components, tests, bats case) plus the
live round.

# Delivering the Claude Code OAuth credential to confined sessions (clown#336)

Status: design only, 2026-11-03. Nothing here is built; no credential was
read, decrypted or handled while writing it. The recommendation is a
security-policy decision for the operator (clown#336). Companion diagram of
the recommended shape: `2026-11-03-oauth-credential-confined.png`
(`.puml` beside it, rendered with `just explore-render-plantuml`).

Environment the facts were read against: clown dc55f0b (this worktree at
59628ca), claude-code 2.1.310, spinclass 0.2.1+54e0b62, piggy and
piggy-agent manpages as installed on flic on 2026-11-03.

## Operator rulings (2026-11-03, relayed by the coordinator; tentative)

1. Candidate B, the frontend injection proxy: approved, tentatively.
2. Holder: H1 (a tmpfs runtime file written by `piggy secrets reconcile`)
   to start. Moving to piggy's securely hosted secrets is a follow-up issue
   the coordinator is filing in clown.
3. Fallback, **reversed** from this note's first recommendation for v1: a
   confined launch with no token source falls back to today's read-only
   login-file bind. Once the token-file path has proven reliable (a few
   weeks), the operator switches to refusing the launch. So the refusal is
   a **switch** (a sweatfile or profile setting, default: fall back for
   now), not a hardcode. The text below is updated to match.
4. Go for the probe recipe (`just explore explore-model-inject-probe`,
   below); the V3 structural-moxy build and the `InjectProxy` code wait for
   the probe's result and the operator's go.

## Recommendation, in one paragraph

Build the **model-API injection proxy** (candidate B below): the unit's
claude talks plain HTTP to a per-session loopback port that the in-unit relay
splices to a frontend unix socket, where clown replaces the `Authorization`
header with the real bearer and forwards over TLS to the API host. The unit
holds only a placeholder. The real credential is read by the frontend from a
**token source outside the unit**; for the first version that source is a
plain file under the operator's `$XDG_RUNTIME_DIR` written by
`piggy secrets reconcile` at a card-present moment (holder H1), with an
in-memory holder in `spinclass serve` (H2) as the upgrade if the operator
rules out any plaintext on a filesystem. The smallest first step is an
operator-run probe, not product code: confirm that claude accepts a
placeholder token with a loopback base URL and record which headers and
paths it sends. Everything that touches the card or the credential is
operator-gated and listed at the end.

## What is true today

Read from `docs/features/0020-agent-scope-confinement.md`,
`cmd/clown/agentscope.go` (`stageAgentHome`) and
`internal/*/agentscope/{unit,egress,spec}.go`:

1. **The operator's login is bound into every unit, read-only.** The
   operator's `~/.claude/.credentials.json` (claude.ai OAuth access and
   refresh tokens) is bound over the seeded in-unit copy. A confined worker
   can read it, and the default egress allowlist lets it use it directly
   against `api.anthropic.com` and the login hosts. The bind became
   read-only on 2026-10-22; a writable bind over the file is refused by
   `Spec.Validate`.
2. **That read-only bind is the motivating failure.** claude writes
   refreshed tokens back to the file; inside a unit the write fails
   (`EROFS`, or `EBUSY` for a rename over a single-inode bind), which is
   the "your new credentials could not be saved" message in clown#336. So
   the login cannot refresh inside the unit, and the message's own advice
   (a long-lived token in the environment) is what the pass prefixes allow
   next.
3. **Two env prefixes pass model credentials into the unit by design.**
   FDR 0020's environment table passes `ANTHROPIC_*` and `CLAUDE_CODE_*`
   wholesale, and says in so many words that an `ANTHROPIC_API_KEY` or a
   `CLAUDE_CODE_OAUTH_TOKEN` passes as "the agent's own model credential".
   A gateway or juggler-remote profile also exports its token as
   `ANTHROPIC_AUTH_TOKEN` (`cmd/clown/main.go`, `applyNamedProfile`), which
   enters the unit the same way. The FDR divergence audit (row 49) flagged
   the prefix; nothing changed since.
4. **The egress proxy cannot inject.** It is a `CONNECT` tunnel
   (`EgressProxy`): TLS ends inside the unit, so a header cannot be added
   without a CA the unit trusts. FDR 0020's "The operator's claude
   credential" section already sketches the reverse-proxy route and calls
   it "a separate design"; this note is that design.
5. **Loopback services reach the unit only as forwards.** `PortForward`
   serves a frontend unix socket bound into the unit, peer-checked by
   `SO_PEERCRED` plus the caller's cgroup, and splices to one fixed
   frontend TCP target; the in-unit `agent-relay` listens on
   `127.0.0.1:<port>` and splices to the socket. A loopback
   `ANTHROPIC_BASE_URL` is deliberately not added to the egress allowlist.
   `NO_PROXY=localhost,127.0.0.1` keeps claude from sending a loopback
   request through the CONNECT relay.
6. **Redaction exists only at the launch plan.** `--print-launch-plan`
   redacts env values whose key contains `key`, `token`, `secret` or
   `password` (`redactEnvEntry`). Proxy and relay logs record destinations
   and refusals, no headers.

## The threat model inside a unit

Every process in the unit runs as the operator's uid in one cgroup, one pid
namespace and one mount namespace (`PrivateUsers=self` maps only that uid;
a user manager cannot switch uid, and `DynamicUser` is a system-manager
feature). The worker is claude's Bash, Write/Edit and `run_recipe`, all
descendants of the claude process. From there, as I read the kernel's
rules (not measured in a unit; the probe below can measure them):

- `/proc/<pid>/environ`, `/proc/<pid>/cmdline`, `/proc/<pid>/fd/*` and
  `/proc/<pid>/mem` of any same-uid process that is dumpable are readable.
  Yama's `ptrace_scope` restricts `PTRACE_ATTACH`, not these
  `PTRACE_MODE_READ` checks. A process could make itself non-dumpable
  (`PR_SET_DUMPABLE=0`), but `execve` resets the flag, so a wrapper cannot
  do it for claude, and claude does not do it for itself.
- A child inherits its parent's environment unless the parent scrubs it;
  claude does not scrub its Bash tool's environment.
- Any file the unit can see, including `$CREDENTIALS_DIRECTORY` of a
  systemd credential, is readable: same uid.
- Any socket bound into the unit answers the worker: the peer check is per
  cgroup, which is the whole unit. A per-process check cannot tell claude's
  own request from its Bash child's: the pid is in the unit either way, the
  ancestry is the same, and `argv` is forgeable.

Consequence: **a credential that is inside the unit in any form is the
worker's.** The candidates that pass are exactly the ones where the
credential never enters the unit. What a worker can always do, in any
design that gives the unit a model at all, is *use* the model on the
operator's account for the unit's lifetime; the design goal is that it
cannot read, copy, persist or replace the credential, and that revoking it
needs nothing inside the unit.

## Candidates

| | Candidate | What the worker can read | Rotation / revocation | `ps`, `/proc`, crash dumps | Verdict |
|---|---|---|---|---|---|
| A | **Frontend-served credential endpoint.** A socket bound into the unit returns the token; claude fetches it through `apiKeyHelper` (claude 2.1.310 has the knob and its `CLAUDE_CODE_API_KEY_HELPER_TTL_MS`, read from the binary's strings). | The token: the worker calls the same endpoint, and the helper's output is in claude's memory. Per-process attribution is impossible (above). | Served value can change at the frontend, but every copy already fetched lives on in the unit. | Token in claude's memory and in the helper's stdout pipe; in a unit core dump. | Reject. |
| B | **Frontend injection proxy** (recommended; FDR 0020's sketch). `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` forwarded to a frontend socket; the frontend swaps the bearer and forwards over TLS. | A placeholder. The worker can make model calls through the proxy (inherent), nothing else: no token to copy, nothing usable after the unit ends. | At the proxy, with the unit untouched: a new value in the token source is picked up at the next request; deleting the source stops every proxy. | Only the placeholder anywhere in the unit. The real token is in the frontend clown's memory, the operator's own process space, as the operator's own claude holds it today. | **Recommend.** |
| C | **systemd credential** (`LoadCredential`/`SetCredential` on the unit, or a memfd). | The token: `$CREDENTIALS_DIRECTORY` is readable by every process of the unit's uid; a memfd shows in `/proc/<pid>/fd`. claude has no native reader for it, so it would also need A's helper. Whether a user manager honours `LoadCredential` for a transient unit is unverified. | Fixed for the unit's lifetime (the credential is materialised at start). | In the unit's tmpfs and in claude's memory. | Reject. |
| D | **Per-process environment** (`CLAUDE_CODE_OAUTH_TOKEN` set only for claude). This is what the error message in clown#336 recommends and what the pass prefixes allow today. | The token: `/proc/<claude pid>/environ`, and the Bash tool inherits it (`env` prints it). | Needs a unit restart. | `ps e`, `/proc`, any core dump of claude or a child. | Reject. |
| E | **Writable credentials-file bind.** | The token, and the worker can replace it (the issue rejects this). Today's read-only bind is E minus the write: the worker still reads the login's access *and refresh* tokens. | A refresh is impossible read-only (today's failure). | File contents, plus claude's memory. | Rejected in the issue. B lets the operator bind go away: the in-unit file becomes the empty seed `stageAgentHome` already creates when the operator has none. |

Holder for the real credential, outside the unit (only B needs one; the
frontend reads it, nothing inside the unit does):

| | Holder | Card needed when | Survives card removal | Survives reboot | Where the plaintext lives | Lane |
|---|---|---|---|---|---|---|
| H1 | **Runtime file via `piggy secrets reconcile`**: a manifest entry whose target is under the operator's `$XDG_RUNTIME_DIR` (tmpfs, 0600). | Once per boot, at the first reconcile after login (one PIN for the whole manifest). | Yes: a file, not the agent's cache. | No: tmpfs. The next reconcile re-decrypts it (needs the card). | tmpfs, hidden from every unit (`ProtectHome=tmpfs` leaves the unit's `/run/user` an empty tmpfs holding only the sockets bound in, FDR 0020). Readable by any operator-uid process outside units: the same exposure as `.credentials.json` today. | piggy writes, clown reads; circus holds the ebox and the manifest line. |
| H2 | **In-memory holder in `spinclass serve`**, primed at a card-present moment and served to clown frontends (outside units) over the serve socket with a uid peer check. | Once per serve lifetime. | Yes. | No, and a serve restart needs the card again. | Process memory only. | spinclass (a new route and a priming verb), clown (a client). |
| H3 | **Per-frontend decryption at launch** (`piggy pass show` into clown's memory through a pipe, no file). | Every confined launch. | n/a | n/a | Process memory only. | clown, piggy. |

H3 alone fails the unattended case: a spawn at 03:00 has no card. It can
complement H1 or H2 for attended launches if the operator wants no standing
plaintext at all, but then unattended spawns still need H1 or H2. `piggy
pass show-batch --out-dir` is H1 without the manifest and state tracking;
`secrets reconcile` is the fleet's existing answer to "decrypt with the card
present, use with the card absent", so H1 reuses machinery instead of
adding a holder. Whether the manifest accepts a target under
`$XDG_RUNTIME_DIR` is piggy's to confirm (the man page says the targets are
"the plain secret files a JSON manifest declares"; freshness is the
ciphertext hash plus the target's stat fingerprint, so a target that
vanished at reboot reads as missing and is re-decrypted at the next
card-present reconcile).

## Candidate B in detail

**Component.** `InjectProxy` (name provisional) in `internal/*/agentscope`,
an HTTP/1.1 reverse proxy on a per-session unix socket
`$XDG_RUNTIME_DIR/clown/agent/<unit>/model.sock`, bound into the unit like
the egress and forward sockets, peer-checked with `authorizeUnitPeer`
exactly as `PortForward` is. The in-unit relay gets one more entry
(`Relay{Listen: "127.0.0.1:<InUnitPortBase+k>", Socket: model.sock}`).
Capability string `model-inject-proxy` in `Result.Capabilities`, so a
spinclass sweatfile can require it the way it can require
`frontend-deadman`.

**Unit environment.** `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` and a
placeholder credential. Which variable carries the placeholder decides
which headers claude sends: `CLAUDE_CODE_OAUTH_TOKEN` should put claude on
its OAuth code path (bearer plus the OAuth `anthropic-beta` header, no login
prompt); `ANTHROPIC_AUTH_TOKEN` puts it on the plain-bearer path, and the
proxy would then have to add the OAuth beta header itself. The binary's
strings show both variables and a `Bearer ${...}` built from
`ANTHROPIC_AUTH_TOKEN` or `apiKeyHelper`; which path the API accepts a
long-lived OAuth token on is for the probe. The placeholder is a fixed,
obviously fake string (`clown-placeholder-not-a-credential`), so that a
transcript line that quotes it reads as such and no secret scanner fires.
`ANTHROPIC_API_KEY` is set present-but-empty, the gateway contract clown
already uses. The operator's `.credentials.json` is no longer bound; the
seeded empty file stays so claude finds a file.

**Request handling.** Replace the `Authorization` header with
`Bearer <real>`; pass every other header unchanged (`anthropic-version`,
`anthropic-beta`, `x-app`, `user-agent`, `accept: text/event-stream`);
stream request and response bodies without buffering (SSE needs a flush per
chunk; `httputil.ReverseProxy` with `FlushInterval = -1`); forward to
`https://api.anthropic.com` with certificate verification from the
frontend's own network namespace, not through the unit's egress. Log
method, path, status and byte counts; never a header or a body. A path
allowlist (`/v1/messages`, `/v1/messages/count_tokens`, and whatever else
the probe records) is a second-order hardening: too narrow breaks claude,
and the peer check is the real gate, so start permissive-under-`/v1` and
tighten from the probe's log.

**Egress.** With B the unit needs neither `api.anthropic.com:443` nor the
login hosts (`console.anthropic.com`, `platform.claude.com`, `claude.ai`)
on its egress allowlist: all model traffic leaves from the frontend. Dropping
them from `DefaultEgressAllow` for confined launches is a fleet policy
change (operator decision, below) and a measurable gain: a worker that
somehow obtained a credential could not use it directly, and the frontend
can count model calls per session.

**Profiles.** The proxy applies only under `--agent-scope`. A profile with
`backend = "anthropic"` (the `claude-anthropic` profile that clown#336's
sessions use) sets no base URL today, so the launch sets it. A gateway or
juggler-remote profile already carries its own URL and token, and that
token enters the unit as `ANTHROPIC_AUTH_TOKEN` (fact 3): the same proxy
can inject it later, which would close that exposure too; out of scope
here.

**What the unit loses.** Anything else claude authenticates to with the
same OAuth token stops working in confined sessions unless the proxy fronts
it as well. The known case is `mcp-proxy.anthropic.com` (claude.ai
connectors), which the fleet already keeps off in clown sessions. Usage,
telemetry and model-listing endpoints: not known; the probe records what
claude calls with the placeholder.

**Failure modes.**

- The API answers 401 or 403: the proxy returns it to claude unchanged
  (claude shows its auth error), logs one line, and sends the session's
  holders one troupe message naming the session and "model credential
  rejected; rotate the entry and reconcile". It never retries with another
  credential and never falls back to a bound file.
- The token source is missing at launch (first boot before a reconcile, or
  a revoked entry): governed by a **switch** (ruling 3). Default for v1:
  the launch falls back to today's read-only login-file bind and logs one
  line saying so (the fallback is never silent). With the switch set to
  refuse, the launch is refused with a message naming
  `piggy secrets reconcile`, in the spirit of the 2026-10-26 ruling that an
  absent card has no fallback to the inherited ssh-agent (clown#324). The
  operator flips the switch once the token file has proven reliable; while
  the fallback is on, fact 1 (the worker can read the login file) holds for
  those launches, which is the accepted v1 trade.
- The source vanishes mid-run (operator deleted it): the next request fails
  as above. H1's proxy reads the file per request (a few hundred bytes from
  tmpfs), so revocation is immediate and no proxy holds a stale copy
  beyond one in-flight request.
- Expiry: the long-lived token's lifetime is documented by claude as about
  a year (recalled, not verified: the docs were not reachable from this
  session). Rotation is manual and attended: `claude setup-token` at a
  terminal, a new ebox entry, `piggy secrets reconcile` with the card.
  Mid-run expiry on an unattended host looks like the 401 case and waits
  for the operator; the unit's `RuntimeMaxSec` (7 days) bounds it.

**`ps`, `/proc`, crash dumps, in B.**

- In the unit: only the placeholder, anywhere. `LimitCORE=0` on agent units
  is cheap and independent of this design; recommend it regardless.
- In the frontend: the token is a Go string in clown's heap for the
  duration of a request (H1) or of the session (H2/H3). Go cannot pin or
  zero it reliably. The exposure is the operator's own process, the same
  class as the operator's interactive claude. The frontend never places it
  in `os.Environ`, in an argv, in the launch plan, or in the result file.
  Whether `systemd-coredump` makes a user process's core readable by that
  uid is unverified; `LimitCORE=0` for the frontend is the operator's call
  (it would also drop clown's own crash diagnostics).

**Scrubbing.**

- Hook logs (`clown-hook-tee`, `clown-hook-toolcost`, the moxy hook logger)
  run inside the unit and can only ever see the placeholder.
- Transcripts are written by claude from inside the unit: the same. A
  worker's `env` or `cat /proc/1/environ` lands the placeholder in the
  transcript, by design harmless. Whether claude writes request headers to
  its project JSONL is unverified; the probe greps the transcript for the
  placeholder.
- stats-me: the statsd relay forwards `clown-hook-toolcost` datagrams,
  metric names and counts only; the proxy's own counters (calls, 401s,
  bytes) carry no label from a header. Rule: metric names are fixed
  strings.
- Frontend logs (`agent-relay.log`, the egress log, the proxy's log):
  method, path, status, bytes. A unit test runs a canary token through
  every proxy error and log path and asserts it never appears, the way
  `TestLaunchPlanJSON_RedactsSecrets` pins the launch plan.
- Errors: the proxy formats status and path, never the request it saw.
- `--print-launch-plan` shows `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>`
  and `CLAUDE_CODE_OAUTH_TOKEN=<redacted>` (the key contains `TOKEN`, so the
  existing redaction covers the placeholder too).

## Who owns what

| | Owns |
|---|---|
| clown | `InjectProxy`, its socket, relay entry and capability; the unit env (base URL, placeholder, empty API key); dropping the operator credential bind for confined launches when a token source exists; the fallback switch (fall back by default, refuse when set); the 401 notice to holders; the canary redaction test; `LimitCORE=0` on agent units; the FDR 0020 update (environment table, sockets table, "The operator's claude credential"). The token-source path convention (a fixed path under `$XDG_RUNTIME_DIR`, published in FDR 0020) so spinclass needs no spec change for H1. |
| spinclass | Nothing for H1. For H2: the holder, its priming verb and the serve route; optionally `model-inject-proxy` in the sweatfile's required capabilities. The spec stays at version 1. |
| piggy | The ebox entry format, `secrets reconcile` to a runtime-dir target (confirm the manifest allows it), the card-present decryption, the one-PIN batch. |
| circus | The entry in `infra/secrets`, the home-manager manifest line, how the entry is addressed. |
| operator | Everything in the next section. |

## Operator-gated parts

1. **Policy: B at all.** Approve the injection proxy as the credential
   path for confined sessions, replacing the read-only bind.
2. **Policy: where the plaintext may live.** H1 (tmpfs file, same exposure
   class as today's credentials file, machinery exists) or H2 (memory only,
   new spinclass machinery). H3 as an optional attended-only complement.
3. **Policy: fallback switch.** Ruled: v1 falls back to the login-file bind
   when the token source is missing; the refusal is a setting the operator
   flips later. Still open: where the switch lives (sweatfile, so spinclass
   owns the policy per repo, or profile, so clown does) and its name.
4. **Policy: egress.** Drop the API and login hosts from the confined
   default allowlist once B is live.
5. **The card:** every reconcile, every rotation, and the probe below.
6. **The probe:** the only step that handles the real credential, at the
   operator's terminal, and only in the probe's memory.

## Smallest first step

A probe, not product code, run by the operator with the card present:

1. `just explore explore-model-inject-probe` (in `zz-explore/justfile`,
   written 2026-11-03): a throwaway loopback HTTP proxy that reads the real
   bearer from its stdin (never argv, a file, the environment or a log),
   replaces `Authorization`, forwards to the API host, and logs per request
   the method, path, upstream status, the `anthropic-beta` value, what the
   `Authorization` header held (placeholder, other or none, never the
   value), the request header names, bytes and elapsed. Bodies are never
   logged. `just explore debug-model-inject-probe-smoke` checks the
   plumbing with a dummy bearer (the upstream's 401 is the pass signal) and
   needs no card.
2. Run, unconfined, at the operator's terminal with the card present, from
   anywhere inside a clown checkout that has these recipes (they are on
   `master`; the main checkout must be pulled first). Both sides are
   recipes so nothing is typed by hand:

   ```
   just explore explore-model-inject-probe-serve     # terminal 1: card, PIN, then the probe
   just explore explore-model-inject-probe-run       # terminal 2: the three variants, Enter between them
   just explore explore-model-inject-probe-report    # afterwards: transcript check and what to paste back
   ```

   The serve recipe resolves the store to the main circus checkout's
   `infra/secrets` (a worktree is refused; `STORE` overrides), checks the
   entry's ebox exists and that the probe's interpreter starts (python3
   through `nix shell --inputs-from` clown's locked nixpkgs: the host has
   no python3 and the registry is never consulted) before touching the
   card, and pipes `piggy pass show` into the probe. The recipes were
   checked from an `env -i` shell with only the nix and system profiles on
   PATH (`just explore debug-model-inject-probe-plain-env`). The run recipe takes `ISOLATED=true`
   as its first argument to add a fresh `CLAUDE_CONFIG_DIR` under the
   scratch dir, away from the stored login.
3. Record: does claude answer "ok" in each variant (terminal 2); the
   probe's log lines (terminal 1); the report recipe's transcript result.

The probe's log decides the env knob, the header handling and the path
allowlist; then the build is one component (`InjectProxy`), its launch
wiring, unit tests with a fake API, and a `verify-agent-scope` bats case
that asserts the placeholder is what `/proc/1/environ` holds inside a unit.

## Probe findings with a dummy bearer (2026-11-03, no real credential)

`just explore debug-model-inject-probe-run-smoke` ran the three claude
variants (claude-code 2.1.310) against the probe fed a fake bearer; the
upstream answered 401 to every model call, which is the expected result and
enough to settle most of the open points:

- claude accepts `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` with the
  placeholder in either variable: no login prompt, requests go to the base
  URL with `Authorization: Bearer <placeholder>` (`authz=placeholder`).
- The variable decides the header set. With `CLAUDE_CODE_OAUTH_TOKEN` the
  `anthropic-beta` list includes `oauth-2025-04-20`; with
  `ANTHROPIC_AUTH_TOKEN` it does not. So the unit's placeholder goes in
  `CLAUDE_CODE_OAUTH_TOKEN`, and the proxy passes the beta header through
  unchanged.
- Paths called: `HEAD /api/hello` (no `Authorization`; the gateway
  discovery the binary's strings mention) before each run, then
  `POST /v1/messages?beta=true`. That is the path allowlist for a first
  `InjectProxy`, plus `/v1/messages/count_tokens`.
- Streaming: the `--output-format stream-json` run came back through the
  proxy's connection-close-delimited relay.
- On 401 claude retries twice (`api_retry` attempts 1 and 2, under a second
  apart) and then stops with "Failed to authenticate": three model calls
  per run, so a proxy that returns the upstream's 401 needs no special
  handling.
- Request header names seen: `accept`, `accept-encoding`, `anthropic-beta`,
  `anthropic-dangerous-direct-browser-access`, `anthropic-version`,
  `authorization`, `content-length`, `content-type`, `host`, `user-agent`,
  `x-app`, `x-claude-code-session-id`, `x-stainless-*`. Nothing the proxy
  must rewrite besides `Host` and `Authorization`.

- The report recipe found no placeholder in the probe run's transcripts
  (both the normal and the isolated config dir). Caveat: those runs ended
  in 401 after one turn, so a transcript with a full answer may still
  differ; the operator's run re-checks.

**The operator's run at the card (2026-11-03, relayed by the coordinator):**
variant 1 (`CLAUDE_CODE_OAUTH_TOKEN` placeholder, `claude -p`) answered
"ok" with exit 0 through the probe, and the probe reported two requests
served (`HEAD /api/hello` and one `POST /v1/messages`). So the API accepts
the long-lived OAuth token injected by the proxy with the header set the
OAuth variable produces, and the answer relays through the proxy's
connection-close delimited stream. Variant 2 got `ECONNREFUSED`: a TCP
refusal at the loopback port, which the probe cannot produce while it
listens (an upstream failure comes back as a 502 or an upstream status),
and the served count stayed at two, so the probe had already been stopped
with Ctrl-C; no rerun of variant 2 is needed, the dummy-bearer smoke
already showed what that variable changes. The per-request lines of that
run were lost to scrollback; the probe now appends every line to
`.tmp/model-inject-probe/probe.log`. Variant 1 also printed claude's
warning that claude.ai connectors are disabled when another auth source
is set: the connector loss noted above, observed. The report recipe found
no placeholder in the probe run's transcripts.

Still worth one cheap rerun (variant 3 only, one card PIN): the 200 and
`beta=` line for the OAuth variable in the log file, and a full answer
streamed as `stream-json`.

## Open and unverified

- Whether the API accepts the long-lived OAuth token with the header set
  the OAuth variable produces (the dummy-bearer probe settled the rest:
  placeholder and loopback base URL accepted, header set per variable,
  paths, streaming, retry count).
- Whether the piggy secrets manifest accepts a target under
  `$XDG_RUNTIME_DIR`.
- Readability of a user process's core dump by its own uid under
  `systemd-coredump`.
- The long-lived token's lifetime and whether it is revocable separately
  from the claude.ai login.
- The in-unit `/proc` readability claims above are from the kernel's
  documented rules, not measured in a unit.

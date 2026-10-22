# FDR divergence audit (2026-10-22, clown cf0aefd)

A strict audit of clown against the design records it relies on, ordered by the
operator. The per-statement tables are the appendices. Each row is a
normative statement marked MET (cites file:line, a test, or a named live
round), DIVERGES (what the code or a live round does instead), UNTESTED
(read in code only, or never run live), SUPERSEDED, or NOT BUILT, with a
severity and a minimal fix for every row that is not MET.

**Method.** Four read-only agents each took a slice, opened the code they cite,
and ran nothing. The live evidence is the nikulin confined rounds J3, K, N/O
and P, as the coordinator relayed them. Host test evidence is from flic at
cf0aefd: `just verify-agent-scope` 12/12, the nix bats lane 150/150, and the
godyn unit suite. The top items below were re-checked in the code before they
were written here. Everything else is as the appendix rows state; a row's
evidence is its own.

| Appendix | Scope | Rows | MET | DIVERGES | UNTESTED | SUPERSEDED | NOT BUILT |
|---|---|---|---|---|---|---|---|
| [A](2026-10-22-fdr-divergence-audit/a-fdr-0020-part-1.md) | FDR 0020 lines 1-304 (unit, egress, statsd, env, terminal, sockets) | 86 | 67 | 11 | 7 | 1 | 0 |
| [B](2026-10-22-fdr-divergence-audit/b-fdr-0020-part-2.md) | FDR 0020 from line 305 (launcher interface, status, losses, limitations) | 79 | 62 | 8 | 6 | 3 | 0 |
| [C](2026-10-22-fdr-divergence-audit/c-profiles-and-context-floor.md) | profiles design and plan, cheap-context persistence, context floor, `pkg/profilelookup`, FDR 0005 | 84 | 57 | 13 | 2 | 9 | 1 |
| [D](2026-10-22-fdr-divergence-audit/d-fdr-0002-0004.md) | FDR 0002 (stdio bridge), ADR 0006, FDR 0004 (hook bridge) | 47 | 26 | 7 | 1 | 2 | 11 |
| | **Total** | **296** | **212** | **39** | **16** | **15** | **12** |

The counts are taken from each row's Status column. Two rows in A have split
statuses and are not counted.

## Top five, by severity

1. **Security: a profile's saved selection fails open.** When no server of the
   keep-set is started or kept, the launch falls back to `runUnbound`
   (`cmd/clown/main.go:2079-2083`, and after the selection `:2111-2114`). That
   hands claude the original plugin dirs, which still declare every MCP server
   (`cmd/clown/pluginbinding.go:87-90`). A narrowed profile then runs
   un-narrowed. There is no test. Fix: under a saved selection, refuse instead
   of falling back. (C) **Fixed after the audit:** `runUnbound` itself
   refuses under a saved selection, naming the keep-set and what the launch
   found, so all four fallbacks (including `--disable-clown-protocol` and
   nothing discovered) are unreachable; `cmd/clown/unboundfallback_test.go`.
2. **Security: the operator's OAuth credential is bound read-write into the
   unit** (`cmd/clown/agentscope.go:350-357`). A confined worker can read it
   or overwrite it, and the claude.ai and Anthropic API hosts are on its egress
   allowlist. The FDR calls this an open question. Fix: bind it read-only, or
   mint a per-session credential, and record the decision. (A) **Bound
   read-only after the audit**; still readable by the worker (FDR 0020,
   "The operator's claude credential").
3. **Security: the egress proxy reaches a host loopback port** when
   `ANTHROPIC_BASE_URL` is on loopback (a local juggler). That host:port is
   appended to the allowlist (`cmd/clown/agentscope.go:175-177`), and the
   proxy dials any allowed target with no loopback check
   (`internal/agentscope/egress.go:131-137`). claude is kept off it only by
   `NO_PROXY`; any other process in the unit can CONNECT to it. This
   contradicts FDR 0020's "nothing else on the host's loopback". Fix: refuse
   loopback targets in the proxy, or never append a loopback base URL. Add a
   bats case. (B) **Fixed after the audit** (both), with bats cases (5dc5fee).
4. **Security (policy, not boundary): the bridge's call refusal can be
   bypassed, and the route that sets it is open to the worker.**
   - The bridge writes the request body to the child verbatim
     (`cmd/clown-stdio-bridge/translator.go:183-187`). A multi-line body whose
     top-level method is harmless can carry a `tools/call` line for an
     excluded tool. (D)
   - `refuseExcludedCall` matches keys exactly
     (`cmd/clown-stdio-bridge/http.go:281-288`). A Go child decodes keys
     case-insensitively (as the spine's own probe does,
     `internal/mcphttp/server.go:259-263`), so `{"name":"kept","Name":"excluded"}`
     reaches it as the excluded tool. That c8e3971 matched keys exactly was
     the wrong direction for Go children. (B, D)
   - Inside a unit, `/clown/exclude-tools` is unauthenticated loopback, so a
     worker can clear its own set. ADR 0006 (bridge trusts loopback) was not
     reopened when `--agent-scope` broke its premise. (D)

   Fix: compact or reject multi-line bodies; refuse any case variant of the
   keys; reopen ADR 0006. **The first two fixed after the audit** (bodies
   compacted; keys folded, repeats refused). ADR 0006 is not reopened yet. Real enforcement belongs on the frontend
   (principle 7, moxy#487).
5. **Security/correctness: partial exclusion matches launch anyway.** Only a
   zero match refuses (`cmd/clown/exclusionreadback.go:60-63`). Unmatched
   saved names are neither pushed nor refused, as in round P, where 89 of 152
   moxy names were unmatched. A tool that registers after the fetch stays
   visible (`cmd/clown/agentscope.go:675-680`). Under principle 1 the
   denylist itself is the deeper problem: `context_excluded` silently admits a
   tool its server adds later (clown#266, allowlist mode). (C) **Rule
   decided and built after the audit**: unmatched names with no loose
   counterpart are harmless; a loose match or a multi-tool match refuses
   (FDR 0020). Allowlist mode goes to the dynamic-profiles design.

Next by severity:
- **The confined launch's result file is unreliable on failure.** A refusal
  after the spec loads writes no result; a unit that goes active and then
  fails (agent-relay's exclusion refusal, exit 126) leaves a success result;
  and the report goroutine gets only 1s (`internal/agentscope/run.go:165-203`,
  `cmd/clown/agentscope.go:35-49`). spinclass relies on this file. (B)
  **Fixed after the audit**: written once the provider is exec'd, rewritten
  with `exit_code` at exit and with `error` on any refusal after the spec
  loads (FDR 0020 "Result").
- **PID 1 does not reap.** claude is PID 1 (`cmd/clown/agentscope.go:500`),
  and zombies went from 2 to 6 in round P. (A, B)
- **The profile registry stamp is unchecked by the binary.** `profile.Load`
  drops the stamp and unknown keys (`cmd/clown/main.go:45-71`), so an older
  clown silently ignores future restriction keys while the library fails
  closed. Close this before the protocol-2 bump. (C)
- **Fail-closed checks only the pid namespace after start**
  (`internal/agentscope/run.go:137-140`). (A)
- **ssh git from a unit has no known_hosts.** The only test disables strict
  checking itself (`zz-tests_bats/agent_scope.bats:285`). (A)
- **Frontend monitors are not yet verified in a real unit or a live round.**
  `agent-scope-run` passes no monitors (`cmd/clown/agentscope.go:762`). (B)

## Conflicts with the EPIC 0001 principles

- **Principle 7 (enforce at the frontend; nothing in a unit is a boundary):**
  - In-unit servers' exclusions are pushed and read back from inside the
    unit (`cmd/clown/agentscope.go:667`, `applyInUnitExclusions`), where the
    worker can clear them.
  - The bridge's call refusal (`cmd/clown-stdio-bridge/http.go:251-312`) is
    policy wherever the bridge runs in the unit. Its comment above
    `handleMCP` reads as protection.
  - The in-unit readback record is unit-written (FDR 0020 says so).
  - Recipe scoping is unenforced (FDR 0020; round J3 ran run_recipe).
  - The only enforced narrowing in a unit is a whole-server drop.
- **Principle 2 (one artifact of authority):**
  - The profile document carries rights in env: `CLOWN_DISALLOWED_TOOLS`
    (`cmd/clown/claudesettings.go:45`, `profileDisallowedTools`) and
    `CLOWN_DISABLE_CLAUDEAI_CONNECTORS` (`:121-130`).
  - Its `context_servers` and `context_excluded` (`internal/profile/profile.go:23-40`)
    decide what is allowed outside any grant.
  - The builtin templates (`internal/profile/builtin.toml`) are a second place
    that decides.
  - Moving these into the grant chain is the dynamic-profiles design's job
    (rights out of the profile document, ruling A).
- **Principle 1 (absent means forbidden):**
  - `context_excluded` is a denylist per kept server.
  - A profile with no saved selection keeps everything.
  - The fail-open fallback (top 1) is the worst case.
- **Principle 9 (fail closed, say what is held):**
  - The refusals name the failure but not what the caller holds:
    `exclusionRefusal` (`cmd/clown/exclusionreadback.go:104`), the bridge's
    "tool %q is excluded for this session" (`http.go:303`), and the
    agent-scope launch refusals (`cmd/clown/agentscope.go:37-49`).
  - `profilelookup`'s not-found lists the known profiles (`pkg/profilelookup/lookup.go:226`).
  - The fail-open fallback (top 1) is a silent fallback.
- **Principle 14 (every threshold is a lever with a start value and a change
  signal):** the relay defaults (`internal/agentscope/monitor.go`: 1024 lines,
  1-30s backoff, 10s short run, 5 failures), the exclusion push budget (3s,
  `cmd/clown/cheapcontext.go`) and the 1 MiB catalog read have start values
  and no stated change signal.

## Per-document notes (details in the appendices)

- **FDR 0020** (A, B): mostly MET and tested for the unit properties,
  env scrub, egress, relays, frontend monitors, binds and readback.
  - Doc-only drift:
    - the XDG dirs paragraph contradicts the code;
    - the MCP wiring section describes an unbuilt `mcp-socket-relay`;
    - the idle socket has no peer check;
    - several "today" paragraphs are superseded (capability count, 11/11,
      "not yet confined");
    - the async availability rows contradict each other.
  - Decision (a) of 2026-10-22 is recorded in FDR 0020 in this commit: in-unit
    job producers are not supported in confinement.
- **Profiles, cheap-context, context floor** (C): the canonical form, digest
  and skew rules hold, except that `Canonicalize` can emit `[]`
  (`pkg/profilelookup/lookup.go:367-369`).
  - A profile picked interactively is never validated (`cmd/clown/main.go:491-515`).
  - The template tests check names against a hard-coded size (156), not a real
    moxy catalog.
  - The April design and plan, and the persistence plan's clown#175 and
    clown#178 notes, are stale.
  - FDR 0005 (hats) is not built under that name.
- **FDR 0002** (D): built as written, apart from these:
  - `/healthz` semantics differ;
  - dropped outbound messages are not logged or counted;
  - a bare command is not looked up on PATH;
  - concurrent clients with equal ids collide;
  - a child line over 1 MiB silently stops the reader.
- **FDR 0004** (D): not built.
  - Its `--settings` merge conflicts with today's user-wins rule
    (`internal/provider/claude.go:31-32`).
  - A plugin's clown.json `hooks` block is silently ignored.

## Cross-repo items (other sessions' records, not audited here)

- spinclass FDR 0035 said clown binds only the child's own troupe file. The
  code bound the whole dir read-write until ce3cc4a (found live, round N).
  It is MET now (round P; `zz-tests_bats/agent_scope.bats`). spinclass's
  record should cite ce3cc4a/cf0aefd.
- The loopback reach of the spinclass serve (F12) is spinclass's audit. The
  clown side, a per-session serve socket, is a design note to follow
  separately.

Clown :clown: 0.4.4+cf0aefd

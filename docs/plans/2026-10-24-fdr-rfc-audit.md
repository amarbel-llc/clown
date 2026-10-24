# FDR / RFC / ADR audit against reality (2026-10-24, clown pure-teak after b51f040)

The coordinator's standing item. Every record in `docs/features` (22 FDRs),
`docs/rfcs` (17 RFCs) and `docs/adrs` (5 ADRs) was audited, plus
`docs/SANDBOXED_SUBAGENTS_README.md`. A separate sweep looked for the
reverse direction: shipped features with no record or man page. For each doc
the audit asked:
- Is its status true?
- Do its load-bearing claims match the code and the man pages?
- Are its links and issue refs live, and do the refs' states match its
  wording?
- Is anything documented but unbuilt, or built but undocumented?

**Method.** Five read-only agents each took a slice: FDR 0001-0008 with the
ADRs, FDR 0009-0016, FDR 0017-0022, the RFCs, and the shipped surface. They
read code and man pages and looked up issue states with `smith issue-get`.
They built and ran nothing. Their rows were consolidated here. A sample of
the top items was re-checked in code before it was written; the rest are as
the agents reported them.

**Tags.**
- *observed*: a lookup was made, such as an issue's state, a file's
  absence or a live MCP tool schema.
- *read-in-code*: established by reading source, man pages or bats files.
  Nearly every claim row is read-in-code. The ref rows are observed.

**Scope limits.**
- Other repos' code and docs were not read; only the state of their issues
  was. ringmaster/troupe, moxy and spinclass are their owners' lane.
- `smith` could not resolve `linenisgreat/eng` or `linenisgreat/circus`, so
  the eng#107/108/112/73 and circus#163/288/351/353 refs are unchecked.
- FDR 0020's earlier audit rows (2026-10-22, appendices A and B) were not
  redone.

## Counts

Approximate, by class, over rows that need action. Rows the agents marked
"consistent" or "none" are not counted.

| Slice | status-drift | claim-drift | ref-drift | dead-link | unbuilt | undocumented | total |
|---|---|---|---|---|---|---|---|
| FDR 0001-0008, ADRs, README | 8 | 34 | 4 | 0 | 1 | 6 | 53 |
| FDR 0009-0016 | 5 | 24 | 6 | 0 | 1 | 3 | 39 |
| FDR 0017-0022 | 4 | 20 | 15 | 0 | 0 | 6 | 45 |
| RFC 0001-0017 | 8 | 44 | 8 | 6 | 2 | 4 | 72 |
| Shipped surface vs docs/man | 0 | 11 | 0 | 0 | 1 | 22 | 34 |
| **Total** | **25** | **133** | **33** | **6** | **5** | **41** | **243** |

By kind: about 170 are plain drift (mechanical), about 45 need a decision,
and the rest are informational. This change fixes about 40 of the
plain-drift rows (next section). The others are listed per document below.

## Fixed in this change (plain drift)

- **FDR 0020** (agent scope):
  - The 2026-10-19 "not yet a confined unit" bullet is marked superseded.
  - The capability list "on that day" is dated, and the current eight from
    `spec.go` are named.
  - The verify-agent-scope count is updated (11/11 then, 18/18 now).
  - The socket preamble names the serve socket, and says the idle socket has
    no peer check (`cmd/clown/selfclose.go`).
  - The egress default paragraph now flags clown#300: the krone cache is not
    in `DefaultEgressAllow`. The decision on it is left open.
  - b51f040 (clown#301) had already replaced F12's "policy, not a boundary"
    note.
- **FDR 0018:** cites clown#218 (persistence gap) and clown#220
  (re-collision) in Limitations.
- **FDR 0019:**
  - The erratum now reads as history, and says moxy#486 is closed with its
    outcome not recorded.
  - clown#252 is described as open ("to be closed by"), not as having closed
    the gap.
  - The "tool allowlist is a later addition" limitation is marked resolved
    (clown#261).
  - The ringmaster/troupe lookup order and the burned-in paths are stated
    (clown#246, closed).
- **FDR 0015:** the emission gate includes stderr (clown#233), and the
  "tier 2 keeps its dedup" sentence is marked superseded by clown#234.
- **FDR 0003:** `mkCircus` is now `mkJuggler` and `.circus/` is now
  `.clown/`. The lead and dynamic fragment layers are noted.
- **FDR 0005:** `mkCircus` is now `mkJuggler`.
- **FDR 0013:** clown#113 is marked closed.
- **FDR 0016:** the Problem Statement is flagged as the pre-phase-1 state.
- **FDR 0002:** a "since this record" note lists the control routes, the SSE
  heartbeats and the serve-socket mode.
- **RFC 0001:**
  - `mkCircus` is now `mkJuggler`.
  - The `checks.*` row is dropped, since none are returned.
  - The managed-settings guardrail sentence is amended (clown#133,
    clown#143).
  - The `claude-code-settings.5` link is fixed.
- **RFC 0002:**
  - The handshake table and §2.2 add the unix form.
  - Local-only Binding covers the serve socket.
  - The managed-settings sentence is amended.
- **RFC 0008:** the #38 link points at the forge, not GitHub.
- **RFC 0014:**
  - The strip-rule refs move from clown#169 to spinclass#169. clown#169 is
    the clown-name pool.
  - The presence path is now `ringmaster/presence`.
- **ADR 0001 and ADR 0003:** status is superseded with FDR-0001 (never
  built), with a banner.
- **ADR 0006:** a facts-only note on the control routes, serve mode, #288 and
  #301. Reopening it is a decision.
- **README (sandboxed subagents):** a historical banner corrects the
  status, the RFC count and the POC list.
- **Man pages:**
  - `clown-plugin-protocol.7` and `clown-plugin-host.1` gain the unix
    handshake form.
  - `clown-plugin-protocol.7` drops the managed-settings claim.
  - `clown-stdio-bridge.1` gains `--listen-unix`/`--peer-unit` and lists the
    control routes.
  - `clown-presence.1` now says `troupe list` (was `clown chat list`), and
    documents the clown-name column and JSON.
  - `clown.1`:
    - The provider list now has all seven.
    - The `--naked` restrictions now include crush, `--tent`,
      `--agent-scope`, `--mcp-collapse` and `--print-launch-plan`.
    - The `Agent(Explore)` disallowed row is removed, and the redirect is
      explained.
    - **The false MANAGED SETTINGS section is rewritten** as SETTINGS: no
      managed settings, what `--settings` actually injects, and that
      auto-mode and bypass are *not* blocked (clown#143).
- **Code (help text):** `clown --help` no longer advertises the removed
  `job`/`job-watch` subcommands. It lists `presence list` and points at
  ringmaster.

## Decisions needed (not made here)

1. **clown#300, egress allowlist.** Add the krone nix cache to
   `DefaultEgressAllow`, or drop it from FDR 0020's default list. The doc now
   flags it as open.
2. **Lifecycle statuses whose criteria may be met:**
   - FDR 0020 is `proposed`; F12 and frontend monitors are built.
   - FDR 0019 is `proposed`; the issue-filer graph is live.
   - FDR 0015 is `proposed`; it is implemented and amended four times.
   - FDR 0014 is `exploring`; the extraction shipped.
   - FDR 0022's promotion-criteria sentence is garbled.
   - RFC 0001 is `testing` (long shipped); RFC 0013 and RFC 0014 are
     `proposed` (implemented).
3. **Records owned elsewhere now.** FDR 0013 and RFC 0009, 0010, 0011, 0012
   and 0015 specify the `clown job*` CLI and the job platform, which moved to
   the ringmaster/troupe repos. Choose between:
   - mark them superseded or owned by ringmaster;
   - rewrite them to the ringmaster/troupe verbs.

   Related: RFC 0009's terminal state `cancelled` versus ringmaster's
   `aborted`/`interrupted`; RFC 0011's catalog lacks `job_cancel` and the
   `chat_*` tools.
4. **The llama-server control plane moved to `juggler daemon` in clown.**
   - FDR 0010, 0011 and 0014 still describe ringmaster/circus.
   - FDR 0014 records "llama hat stays in ringmaster", and that decision was
     reversed.
   - FDR 0011's `--backend` was never built.
   - FDR 0008's superseded-by target is unbuilt.

   Decide: rename sweep plus "superseded by FDR 0019 / juggler model
   registry", or rewrite.
5. **Tent.** FDR 0007 says `testing`, while FDR 0017 calls it shelved.
   Open questions:
   - set FDR 0007's status;
   - re-scope or close clown#62 (claude now comes from llm-agents);
   - remove `--tent-pass-devshell` or keep it;
   - add `--tent` to clown.1 or mark it shelved there;
   - clown#279 (tent exposes `~/.config/nix`) versus FDR 0017's bind
     rationale.
6. **Dead sandboxed-subagent specs.**
   - RFC 0003, 0004 and 0005: withdraw or mark superseded.
   - ADR 0005: superseded, keeping its findings?
   - RFC 0003 surfaced a probable bug: `parseAgent` silently drops
     `disallowedTools` (flake.nix), which `subagents/discover.md` sets.
7. **RFC 0006 posture versus agent scope.** "clown MUST NOT impose
   confinement on plugin servers" and "clown.json MUST NOT gain a
   confinement field" are contradicted by in-unit servers and the `scope`
   field.
8. **RFC 0013 and RFC 0014 rules overtaken by code:**
   - RFC 0013's "journal+spool is the sole chat store" versus xmpp-native's
     ephemeral wakes.
   - RFC 0014's "clown MUST NOT reference SPINCLASS_*" versus
     `cmd/clown/selfclose.go` reading `SPINCLASS_SESSION_ID`.
9. **RFC 0016.** Mark it superseded: native troupe XMPP shipped instead of
   the recommended gateway.
10. **ADR 0006:** whether to reopen it for serve mode and clown#288.
11. **FDR 0009 (`--naked`).**
    - Profile resolution still runs under `--naked`: gate it, or document
      it.
    - `openrouter` is not refused under `--naked`, though it needs opencode
      config injection.
12. **FDR 0002's still-open rows from the 2026-10-22 audit:**
    - a bare command is prefixed with the plugin dir instead of a PATH
      lookup (#4);
    - dropped outbound messages are counted but never logged (#22);
    - a child line over 1 MiB is only logged (#24);
    - duplicate in-flight ids across clients are rejected (#30).

    Each needs a fix in code or in the doc.
13. **moxy#486 and moxy#487 are closed.** FDR 0019 and FDR 0021 still wait on
    them, and `internal/jugglertools/client.go` says "awaits moxy#486".
    Record their outcomes, and decide whether juggler launches moxy in
    enforce mode.
14. **Shipped features with no FDR or man page:**
    - cheap-context (effectively the shipped half of FDR 0005, hats);
    - the profiles UX (`clown profile`, `profiles.toml`, the pickers, the
      `CLOWN_PROFILE*` env; clown#275);
    - mcp-collapse and `clown-mcp-collapse`;
    - clown-hook-tee;
    - clown-hook-toolcost and the context floor (its metric names are a
      contract);
    - clown-hook-allow (it changes permission semantics);
    - self-close (`clown idle-notify`, `CLOWN_DISABLE_SELF_CLOSE`);
    - pty-suspend.

    Decide which deserve records.
15. **clownbox** is documented as a provider but disabled at build time
    (`clownboxCliPath = ""`). Say so, or remove it.
16. **`CLOWN_STAGING_ROOT`** was "temporary, removed after one release" and
    is still present (FDR 0017 promotion criteria).
17. **Issues whose state contradicts shipped work** (close, or keep open on
    purpose):
    - clown#202 and #203 (phase 1 and its bats lane shipped);
    - clown#169 (clown-names ship);
    - clown#145 (attach self-wrap shipped);
    - clown#233 (fixed, awaits the live check);
    - clown#119;
    - clown#245 (FDR 0019's placeholder);
    - clown#295 (FDR 0022 slices 1-3c);
    - clown#26;
    - clown#58 (the portfile is still read);
    - clown#81.
18. **FDR 0021:** the proposed harness name `juggler-run` collides with
    existing identifiers (`internal/jugglerrun`).

## Remaining plain drift, per document (not fixed here)

These are mechanical; each row names its evidence as the agents reported it.

- **FDR 0001:**
  - The banner overstates tent; FDR 0017 calls it shelved, and live
    confinement is FDR 0020.
  - "ringmaster survived as FDR 0010" misattributes; FDR 0010 is a
    llama-server control plane.
  - `circus` is now `juggler`.
- **FDR 0002:**
  - `/healthz` semantics: 200 while the child is alive, 503 after
    (`cmd/clown-stdio-bridge/routes.go`).
  - "does not interpret MCP semantics" is false; the bridge has the control
    routes and tools/list/tools/call interception.
- **FDR 0004:**
  - It has no "not implemented" line; nothing is built (there is no
    clown-hook-shim and no `CLOWN_HOOK_SOCK`).
  - Its line refs are stale.
- **FDR 0006:**
  - The harness and provider list is out of date (now claude, codex,
    juggler, opencode, openrouter, crush, clownbox).
  - The Validate combos have changed.
  - The builtin.toml path is now `internal/profile/builtin.toml`, with 11
    profiles.
  - The `circus` portfile is now juggler's.
  - The opencode config path is now `~/.config/clown/opencode.toml`.
  - crush is cited as hypothetical but has shipped.
- **FDR 0007:**
  - The "patched cli.js reads managed settings" motivation is stale (the
    unpatched llm-agents binary; clown#133).
  - Line refs are stale.
  - `circus` is now `juggler`.
  - FDR-0001 is called "draft".
  - Three shipped features are undocumented: the lima backend, the
    on-demand image build, and git-worktree binds. The darwin ssh forward is
    also undocumented. clown#99 (explore Lima) is open though lima shipped.
- **FDR 0008:** the banner says `--backend=circus` is how sessions launch;
  it was never built.
- **FDR 0009:**
  - The provider binary is build-pinned, not looked up on PATH.
  - The env is not exactly as inherited: identity env and the clownfile
    `[messaging]` env are still applied.
  - It names `run()`; the code is `runWithFlags()`.
  - There is no `circus` provider.
- **FDR 0010:**
  - ringmaster is now juggler: `control.sock`, `programs.juggler.enable`,
    `readJugglerPortfile`, `verify-juggler-multi`, `juggler.bats`.
  - There is no `address` subcommand.
- **FDR 0011:**
  - It still has the circus/ringmaster names.
  - The builtin.toml path is stale.
  - "#58 fully done" is wrong; #58 is open and the portfile is still read.
- **FDR 0012:**
  - clown#44 is an unrelated issue (a PostToolUse formatter).
  - It says FDR 0004's machinery "is reused", but FDR 0004 is unbuilt.
- **FDR 0013:**
  - The `clown job*` CLI examples should be the ringmaster/troupe verbs.
  - `cancelled` should be `aborted`.
  - The session-key precedence is outdated (it still lists
    `SPINCLASS_SESSION_ID`).
- **FDR 0014:**
  - The flake input URLs are stale.
  - The "until the repos land" note is obsolete.
- **FDR 0016:**
  - The Abstract says the bats lane needs dumbo; `provider_mcp.bats`
    exists.
  - Line refs are stale.
  - The `[providers] hermetic-config` lever is not named.
- **FDR 0017:** the launch-plan JSON's `deferred` field (clown#284) is not
  mentioned.
- **FDR 0019:**
  - It says juggler links `internal/mcphttp`; juggler carries its own client
    (the clown#258 boundary).
  - §2 says the parent's principal and JID are in the unit env; they are in
    the brief and the hook argv only (`internal/jugglerrun/spawn.go`).
  - Line 1048 and line 1486 contradict each other on FDR 0032's merge state.
  - The `juggler handles` verb is undocumented.
  - It does not cross-reference FDR 0020, FDR 0021 or clown#252 under
    "namespace sandboxing deferred".
- **FDR 0020:**
  - clown#288 (bridge loopback) is not cited.
  - clown#278 (unit reads host `nix.conf`) is not cited.
- **FDR 0021:**
  - It says frontend monitors are not built; they are.
  - It says the FDR 0019 erratum was not applied; it was.
  - About eight line citations have moved: `client.go`, `inunit.go`,
    `main.go`, `profile.go`.
  - clown#281 is closed but still described as reproducing.
  - Its default-allowlist description is incomplete.
- **FDR 0022:** `CLOWN_PROFILE_DIGEST`, `CLOWN_PROFILE_SESSION` and
  `profile_form` are in no man page (see decision 14).
- **RFC 0001:**
  - The version-table ordering is wrong: clown is pinned first, and
    ringmaster and troupe have rows.
- **RFC 0002:**
  - The `scope` field is missing from §1.2.
  - §4 still draws the host as a separate exec; it runs in process.
  - The conformance table cites `clown job-mcp`; it is `ringmaster mcp`.
    There is no unix-handshake row.
- **RFC 0004:** its `name = "ringmaster"` collides with the job-platform
  binary's name.
- **RFC 0006:**
  - It says clown#28 is under design; it is closed and the bridge shipped.
  - It still names `mkCircus`.
  - spinclass is called a "future candidate"; it is a live plugin.
  - The managed-settings claims are stale.
- **RFC 0007:** citations of "RFC 0002 §1.3" and "§1.2" point at the wrong
  sections.
- **RFC 0008:**
  - It says "mkCircus injects managed settings", which is stale on both
    counts.
  - clown-hook-allow is missing as prior art.
- **RFC 0009:**
  - The `clown job*`/`job-watch` CLI should be the ringmaster verbs.
  - The `CLOWN_BIN` requirement is stale.
  - The `SPINCLASS_SESSION_ID` precedence is not marked amended.
  - The paths should be `ringmaster/jobs`.
  - `require_bin` should use the `RINGMASTER_BIN` names.
  - clown#113 is closed.
- **RFC 0010:**
  - The CLI names should be ringmaster's.
  - Paths are stale.
  - Its cited test files are not in this repo.
  - The §7 citation is wrong.
- **RFC 0011:**
  - The servers are `ringmaster mcp` and `troupe mcp`.
  - "Use the clown job CLI" should name ringmaster's.
  - The synthesized plugin's monitors and hooks are undocumented.
  - Test refs are stale.
- **RFC 0012:**
  - `clown job whoami` is now `ringmaster whoami`.
  - The precedence is not amended.
  - The "open gaps" clown#132, #135 and #136 are all closed.
- **RFC 0013:**
  - `internal/circus` and `.circus/` are gone.
  - `prepareClaudeSessionID` is now `decideClaudeSession`.
  - The builtin.toml path and the "AGENTS.md Profile system" ref are dead.
  - The provider enum is stale.
  - The default-clownfile example is out of date.
  - The group-channel rule should cross-reference RFC 0014.
  - It says direct messages target a bare key; troupe requires a
    fully-qualified JID.
  - Conformance bats files are missing.
- **RFC 0014:**
  - The interpolation scope is wider than "group-id only".
  - It still refers to `clown chat list`.
  - The zmx example is stale.
  - Its cited `internal/jobwake` tests are not in this repo.
- **RFC 0015:**
  - "internal/jobwake keeps its name" is stale.
  - It calls XMPP future work; it shipped.
  - The deprecation-shim clause contradicts §Compatibility.
  - Its conformance bats are absent.
- **RFC 0016:** it cites `clown job status`.
- **RFC 0017:** it says phase 1 (#202/#203) "shipped" while both issues are
  open (see decision 17).
- **ADR 0006:** "binds 127.0.0.1:0" holds in TCP mode only.
- **ADR 0007:** "bats-default runs every integration test" is no longer
  true; host-only tags are filtered (`bats.nix`).
- **docs/plans 2026-10-22 appendix D:** it says `plugin_host.bats` does not
  exist; it does.
- **Man pages:**
  - `clown.1` still lacks a SUBCOMMANDS section (`resume`, `profile`,
    `list`, `sessions-complete`, `presence`, and the internal agent-* and
    egress-connect verbs).
  - `clown.1` lacks OPTIONS entries for `--profile`, `--cheap-context`,
    `--mcp-collapse`, `--tent`, `--agent-scope`, `--tent-pass-devshell`,
    `--plugin-dir` and `--help`.
  - `clown.1` ENVIRONMENT lacks:
    - `CLOWN_PROFILE*`
    - `CLOWN_TENT*`
    - `CLOWN_AGENT_SCOPE`
    - `CLOWN_ATTACH_FORCE`
    - `CLOWN_BIN`
    - `CLOWN_SESSION_ID`
    - `CLOWN_DISABLE_JUGGLER_MCP`
    - `CLOWN_HOOK_DEBUG_LOG`
  - `clown.1` documents only `plugins` among `mkJuggler`'s arguments.
  - `clown.1`'s synopsis shows `clown [query]`; unknown args are refused,
    so forwarding needs `--`.
  - `clown-stdio-bridge.1` lacks `CLOWN_BRIDGE_TRACE` and
    `CLOWN_BRIDGE_WRITER_LIVENESS_INTERVAL`.

Clown :clown: 0.4.4+4147fe1

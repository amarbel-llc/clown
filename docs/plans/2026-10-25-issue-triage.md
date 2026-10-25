# Open-issue triage (2026-10-25)

Scope: all 153 open issues on code.linenisgreat.com/clown as of 2026-10-25.
Each issue was read with comments (smith issue-get) and checked against this
worktree (`pure-teak`, HEAD 196d338) by reading code, docs and git log.
Nothing was implemented, built or tested.

Buckets:

- **(a)** fixable now, test-first, small, no operator decision
- **(b)** needs a decision
- **(c)** blocked on another repo/tool
- **(d)** stale, verified against current code/docs/git

Evidence labels: **V** observed, **R** read in code/docs/issue, **I** inferred.

Per the triage rules, the juggler extraction/roustabout issues, #263, #274,
#278, #280, and anything that needs the operator's card, ssh agent, a lock
bump or a security-policy call sit in (b) or (c), never (a).

Counts: **a 14 / b 92 / c 23 / d 24**, 153 in total.

## (a) Fixable now

| # | Title | Fix sketch; failing test; risk | Ev |
|---|---|---|---|
| 18 | Allow/prefer `TODO(#NNN):` | Add a `system-prompt-append.d/03-todo-issue-refs.md` fragment preferring `TODO(#NNN)`/`FIXME(#NNN)`. Test: the assembled append prompt contains `TODO(#`. No existing fragment-content test was found, so the test's location still has to be picked. S | V,I |
| 243 | Never use the file-based memory | Add a fragment forbidding reads and writes of `~/.claude/projects/*/memory` and `MEMORY.md`. `CLAUDE_CODE_DISABLE_AUTO_MEMORY` is already injected (`cmd/clown/claudesettings_test.go:182`), but MEMORY.md was still loaded in a live session. Test: the composed prompt contains the directive. S | R,V |
| 120 | Subagents hand-convert epoch timestamps | Add a rule to `system-prompt-append.d/01-runtime-claims.md` (and `subagents/`): use the env date tool or `date -d @N`, never convert by hand. Test: the assembled prompt contains the rule. S | R,I |
| 56 | juggler has no -h/--help | `cmd/juggler/main.go:23-25,87-89`: add `-h`/`--help`/`help` cases that print the usage to stdout and exit 0. Test: `run([]string{"-h"})` returns 0 with the usage on stdout. Completions stay a separate follow-up. S | R |
| 84 | justfile verb/lifecycle taxonomy | The renames are done; what is left is adding `[group]` to the ungrouped root recipes (build, build-nix, clean-result, codemod, default, lint, test, verify, update-inputs, run-*) and to `explore::`. Test: a lint recipe that fails when `just --dump` shows a public recipe with no groups. S | V,R |
| 227 | Prompt includes the `clown version` table | `cmd/clown/buildidentity.go:23-50` renders only the build id. Append the pinned-component rows from the version-table builder (`main.go:3071`), labelled as build-time pins. Test: `buildIdentityFragment()` names a buildcfg-pinned component. S | R |
| 200 | Shared bubbletea picker-run helper | Add a generic `runAltScreenPicker[M tea.Model]` to replace the tails at `main.go:260`, `resume.go:679`, `cheapcontext_picker.go:177` and `openroutermodelpicker.go:245`. Test: the helper returns ok=false and a nil error for a cancel-sentinel model. S | R |
| 235 | t.Skip guards that never run in the nix lane | Set `CLOWN_NIX_TEST_LANE=1` in clown-go-test and add a `skipOrFail` helper that fatals under it. Add git/sh to the derivation and allowlist the darwin Metal skips. There are 12 sites, e.g. `cmd/clown/attach_test.go:337,365,631,662,675`. Test: a unit test that `skipOrFail` fails when the var is set. S | R,I |
| 275 | Document profiles in the manpages | Add a man5 profiles page covering the 8 points in the issue comment, cross-referenced from `man/man1/clown.1` (which has no profile hit today). Check #175's status (point 4) first. Test: the page exists and names `context_servers`, `context_excluded` and `[registry]`. S | R |
| 159 | Launch without the multiplexer attach | Add a `--no-attach` token, stripped next to `extractAttachID`, that skips `maybeReexecMultiplexer` (`attach.go:167`). Test: in `attach_test.go` with `CLOWN_ATTACH_FORCE=1` and posh plus `--no-attach`, there is no re-exec and the flag is not forwarded. S | R |
| 265 | Tell the session which tool categories were disabled | When a cheap-context selection or profile replay applies, append a fragment that names the profile, the stopped servers and the excluded groups. No such notice exists in `cmd/clown` (rg). Test: the fragment for a `Selection{ContextServers, ContextExcluded}` lists them by name. M | R |
| 82 | Thread a per-connection ctx through dispatch | `cmd/juggler/server.go:129,146,172,213,324` pass `context.Background()`. Derive a ctx in `handle` and cancel it on read EOF. Test: a slow fake launcher sees ctx cancelled after the client closes. M. This is juggler code: keep it unentangled for #258. | R |
| 151 | Darwin ptysuspend proxy | Add `ptysuspend_darwin.go` (posix_openpt/grantpt/unlockpt/ptsname, `Supported()=true`) and narrow the build tag in `_other.go`. The test is darwin-only and cannot run on the linux nix lane. M | R,I |
| 79 | mkJuggler accepts tentImageTarball | `flake.nix:1392-1413` has no such parameter, and the tarball is gated on isLinux (`:745,797`). Add `tentImageTarball ? null` and feed the buildcfg ldflag (`:956`). Test: a flake check evaluates it with a dummy tarball. M; the darwin path cannot be checked here. | R |

## (b) Needs a decision

| # | Title | Options; recommendation; unknowns | Ev |
|---|---|---|---|
| 302 | Live MCP permission-prompt metrics | Spike which hook events fire on a prompt and its outcome first. Options: PreToolUse counters via spinclass, a PermissionRequest hook, or offline freud. Rec: spike, then use `clown.permission.*` names matching #263. | I |
| 300 | Egress allowlist lacks the krone cache | `internal/agentscope/egress.go:29-38` has no krone entry, and FDR 0020:101-106 defers it to the operator. Options: add it to the default, fix the FDR sentence, or keep it in the sweatfile. Rec: fix the FDR and keep it in the sweatfile (security). | R |
| 299 | Consolidate clown:// and clown-catalog:// | Rec: `clown://session/...` plus `clown://catalog/...`, with clown-catalog:// kept as an alias. Not urgent. | R |
| 292 | Canonical carries credential-bearing URLs | The env half is fixed (`pkg/profilelookup/lookup.go:41,59,247-251`). The URL half is open (`lookup.go:68-71`). Options: refuse userinfo in Validate, redact with a ProtocolVersion bump, or document only. Rec: refuse userinfo (security call). | R |
| 288 | stdio-bridge loopback accepts any local process | `cmd/clown-stdio-bridge/main.go:53-82`: only the unix plus `--peer-unit` path checks the caller. Options: a bearer token, unix plus SO_PEERCRED for every bridge, or accept the risk. Rec: unix plus peer check everywhere (security). | R |
| 280 | Confined sessions spawning confined children | Required to be (b) by the rules. Open: where the child's frontend and unit live. Coupled to spinclass FDR 0032 D7 and spinclass#382. | R |
| 279 | tent exposes ~/.config/nix | `internal/tent/binds.go:32,58`. Options: drop the bind, use a sanitized copy, or keep it. Rec: drop it and flip `binds_test` (security). | R |
| 278 | Confined unit reads host nix.conf | Required to be (b) by the rules. Rec: a sanitized copy; needs a decision on which keys are sensitive. | R |
| 277 | Opt-in MCP surface for profiles | Rec: defer until #276 and #266 fix the schema. | I |
| 276 | Profile-level spawn permission schemes | Rec: a joint FDR with spinclass. Unknown: where spinclass composes it. | I |
| 271 | Headless juggler-only agent loop | Juggler design (FDR 0021), tied to #260 and #263. Rec: design doc first. | R |
| 270 | Scriptable dummy agent for juggler | Options: RFC 0017 dumbo, or a standalone script agent. Rec: dumbo. Unknown: whether spawn survives step 2. | I |
| 266 | Allowlist mode for cheap-context saves | Needs a profiles.toml schema and ProtocolVersion bump, announced first. Rec: a per-profile mode, with allowlist the default for new saves. | R |
| 263 | juggler native toolcost metrics | Required to be (b) by the rules; deferred until model management moves to juggler. | R |
| 258 | Extract juggler into its own repo | Required to be (b) by the rules. The operator deferred it on 2026-10-14. Open: extract the pre- or post-step-2 shape. | R |
| 251 | juggler spawn `--room` affiliation | Options: fix in `internal/jugglerrun/spawn.go`, drop `--room`, or wontfix because spawn retires at step 2. Rec: deprecate in favour of `--room-domain`. | R,I |
| 245 | FDR placeholder: juggler subagent platform | Juggler design record. Rec: keep it as a placeholder. | R |
| 244 | Note: agent vs frontend scopes | Design note; the posh question is open. | R |
| 241 | plugin-host does not restart or surface a dead child | `internal/pluginhost/server.go:199-222` only logs. Options: restart with backoff, surface it, or both. Rec: surface it first (clear error plus unhealthy state), then restart. | R |
| 220 | Name re-collision on resume | Options: accept the documented limitation (FDR-0018), defer to the incumbent with a generation bump, or evict. Rec: accept for now. | R |
| 218 | Name persistence with no stable session id | Options: resolve the newest id for `--continue`, a cwd-scoped binding, or accept. Rec: the first for `--continue`, accept for bare launches. | R |
| 214 | Monitor exits mid-session | Root cause unknown. Rec: diagnose by comparing SessionStart re-arm before choosing between a ringmaster fix and a re-arm hook. | R,I |
| 212 | `clown tokens` estimation table | Options: offline tokenizers (vendorability unknown), count_tokens API, or drop it. Rec: drop it, or API for claude only. | R,I |
| 211 | mcp_call drops progress notifications | Needs incremental SSE plus progressToken remap. Rec: bundle with the mcp-collapse v1 hardening. | R |
| 206 | opencode/crush config not hermetic | Mitigated by `hermetic-config` (default on). Rec: keep the blunt suppression until someone needs project MCP entries. | R |
| 205 | tentExecutor x configFileBinding | A guard is in place and FDR 0017 records it. Rec: close as superseded by FDR 0017. | R |
| 203 | Phase 1b bats lane, opencode/crush MCP | `zz-tests_bats/provider_mcp.bats` exists. The crush-workspace arm needs dumbo. Rec: close and file a follow-up that depends on dumbo. | V,R |
| 201 | Shared HTTP-fetch helper | Two of the three sites are juggler code (`internal/jugglermodels/download.go:50`, `cmd/juggler/prompt.go:213`). Rec: wontfix until the extraction, so juggler stays unentangled. | R,I |
| 199 | ENABLE_TOOL_SEARCH per provider | Never set by clown. Rec: set it false for non-Anthropic profiles. Unknown: which backends support tool search. | R,I |
| 192 | Unified session addressing | clown's scope is reported done (96d0964, 81c9c04). Rec: close and file the petname-in-picker item separately. | R |
| 177 | h/l collapse in the cheap-context picker | `cheapcontext.go:281-282` defers to #176. Options: move paging off h/l, use other keys, or add no collapse. Rec: page with ctrl+b/f only and use h/l to collapse. | R,V |
| 176 | Collapsible tree picker | Its remaining scope is #177. Rec: close in favour of #177 and fix the stale comment at `cheapcontext.go:281`. | R,V |
| 172 | chat_send fails to wake an active recipient | Caused by the clown/jobs to ringmaster/jobs namespace move; the fix was operational. Rec: close once the operator confirms no live pre-move monitor. Unknown: fleet state. | V,R |
| 166 | RFC draft: XMPP backend for troupe | Likely superseded by #213 and the xmpp-native backend. Rec: close as superseded after operator confirmation. | V,I |
| 165 | `--print-session-id` on spawn | Not implemented. Rec: wontfix unless a consumer needs it, since spinclass's hello already covers it. | V,R |
| 163 | AskUserQuestion 60s auto-continue | Ask #1 is done (`claudesettings.go:115-116`). Left: verify it on a current build, a version pin (lock bump), or a hard disallow. Rec: verify, then close; no pin. | R,V |
| 156 | Prosody home-manager module | Placement (clown, eng or troupe) plus ACME/DNS policy. Rec: troupe or eng. Unknown: where the prototype lives. | V,I |
| 153 | Native resource-to-tool projection | Rec: decide #138 first. | V |
| 150 | Flip the pty-suspend default | Rec: wait for the operator's dogfood sign-off. | V |
| 143 | Re-home auto-mode/auto-memory blocking | Options: add the keys to the inline `--settings` (`main.go:1548`), an HM managed-settings module, or declare tent the only boundary. Rec: inline `--settings` if still wanted. Unknown: whether claude honours those keys via `--settings`. | R,I |
| 142 | madder as durable blob backend | Rec: an RFC co-designed with moxy before any code. | R |
| 141 | troupe as a plugin-protocol hub | Rec: close as superseded by troupe/XMPP unless MCP forwarding is still wanted. | R,I |
| 139 | Conformist module for plugins | Rec: settle the boundary with purse-first#154 first. | R |
| 138 | Bundle moxy into clown | Rec: an FDR; it also decides #104 and #153. | R |
| 134 | Jobs that execute MCP tools | Rec: defer; an executor probably belongs in ringmaster. | R,I |
| 119 | Job-wakeup across hosts | Rec: close as superseded by troupe federation once cross-host is confirmed live. | R,I |
| 118 | Programmatic task-list access | Rec: hook shadowing, only if spinclass still consumes it. | R |
| 117 | clown as the complete job system | Rec: re-scope to ringmaster and close here. | R |
| 116 | Re-enable native WebSearch | Disallowed at `flake.nix:226`. Rec: make it per profile (egress policy). | R |
| 107 | Auto-load open issues at startup | Rec: an opt-in fragment with titles only. The forge is Forgejo, not GitHub. | R,I |
| 104 | FDR-0012: moxy peer-dependency docs | Waits on #103 and #138. | R,I |
| 103 | FDR-0012: narrow tent image | Waits on #34. | R |
| 102 | FDR-0012: deprecate `--tent-pass-devshell` | The flag is live (`main.go:501-509`). Rec: only after #103. | R |
| 101 | Implement FDR-0012 (tracker) | Rec: decide whether FDR-0012 is still wanted now that the Lima backend exists. | R |
| 100 | podman-darwin HM module in clown | Rec: the operator picks between it and retiring podman-machine; the Lima HM module (`flake.nix:1910`) already exists. | R,I |
| 97 | Export tent host paths as a Nix output | Rec: a Nix attrset generated from `binds.go`. Unknown: overlap with the Lima module. | R,I |
| 91 | tent synthetic $HOME | `internal/tent/tent.go:315-324` still binds ~/.claude*. Rec: a synthetic home (security; same family as #279). | R |
| 90 | Rename the clown-tent image | The circus name is retired. Rec: keep clown-tent and close. | R |
| 89 | Decide the #83 workaround | Unknown whether the current claude-code still wedges. Rec: re-run the #83 matrix first. | R,I |
| 88 | ~/.claude.json bind breaks atomic write | Still a leaf bind (`tent.go:318-323`). Rec: fix via #91; needs a repro now that keep-id is gone. | R,I |
| 87 | juggler bind-address forwarding | FDR-0011 is exploring; a juggler feature. Rec: defer past #258. | R |
| 86 | Flexible model registry | Juggler; needs an FDR. Rec: defer past the extraction. | R |
| 83 | `--tent` wedges under zmx+sc | Needs the operator's terminal stack. Rec: re-test on the current claude-code. | I |
| 81 | `*-local` profiles broken | Still true: `readJugglerPortfile` (`cmd/clown/opencode.go:283-301`) has no writer anywhere. Options: juggler RPC client, delete the profiles, or restore the portfile. Rec: RPC client, aligned with `docs/plans/2026-10-20-juggler-extraction.md:140`. | R |
| 78 | CI workflow on push/PR | There is no .github/.forgejo dir; the pre-merge `just` is the CI lane. Rec: close as superseded. | V,R |
| 77 | nixpkgs bump for Go 1.26.3 | Probably stale (nixpkgs locked 2026-10-16), but the Go version in the closure was not checked. A lock-bump issue. Rec: check once in a nix lane, then close. | R,I |
| 76 | juggler `--tailscale-serve` | Rec: defer, after #87. | R |
| 75 | tent hangs inside zmx | Duplicate of #83. `resetTerminal` (`main.go:3113-3117`) lacks `ESC >`. Rec: close as a duplicate and file the DECPAM gap. | R |
| 73 | Interim `--tent-pass-devshell` | `--profile` has landed, but the flag is still wired. Rec: merge into #102. | R |
| 67 | PTY spinner for tent podman-run | Rec: defer behind FDR-0012. | R |
| 66 | Explore `claude --bare` | Main premise gone (no managed-settings). Rec: re-scope to "--bare inside tent". | R |
| 65 | `--tent --select` claude-code version | Rec: defer. | R |
| 58 | circus pid/port handshake | Its remaining work is #81. Rec: close together with #81. | R |
| 57 | gemma3 template vs crush | Rec: re-test, then file at ggml-org/llama.cpp. Unknown: current llama-cpp behaviour. | I |
| 53 | semver/API-compat skill | Rec: move to the eng skills repo. | I |
| 52 | Honour tools/list_changed | Binary patching was dropped (`flake.nix:1148-1153`). Rec: document /mcp Reconnect and close. Unknown: current upstream behaviour. | R,I |
| 44 | PostToolUse formatter hook | Rec: pick a home (moxy, claude-hooks or user config) and move it there. | R |
| 42 | 60s default-timeout footgun | No default injection exists. Rec: docs plus a compile-time warning. Unknown: the "no override" convention. | R |
| 40 | clown-hook-allow deny control test | Needs a live claude API run (operator credentials). Rec: re-run on the current version. | R,I |
| 38 | Pin RFC 0008 design | `docs/rfcs/0008-...md:2` is exploring; security design. | R |
| 37 | Scope FDR 0005 (hats) | Exploring. Rec: check overlap with profiles, then close or narrow. | R,I |
| 34 | Sandbox-escaping hook bridge | RFC 0007 is still a draft. Needs operator acceptance (security). | R |
| 32 | clown.json into .clown-plugin/ | Breaks external plugins. Rec: `.clown-plugin/clown.json` with a dual-read period, announced first. | R,I |
| 27 | Codex thread-id completion | The gap is real (`internal/sessions/claude.go:3`, `completions/clown.fish:166`). Options: sqlite dependency, shell out to sqlite3, or parse codex output. Rec: check the codex schema first. | V,R |
| 26 | RFC: plugin sandboxing posture | RFC 0006 is a draft. Rec: the operator accepts it and closes (security). | R |
| 25 | dewey/golf/command framework | Rec: keep the hand-rolled one. | R,I |
| 21 | Circus GitHub org project | The forge is Forgejo now. Rec: close as obsolete. | R,I |
| 17 | nixpkgs bump for llama-cpp | `nixpkgs-llama` is still used (`flake.nix:32,159,1164`); lock bump. Rec: check main nixpkgs' llama-cpp, then drop the input. | R |
| 12 | Non-TTY posture | Rec: an FDR with a single IsInteractive predicate. | R |
| 11 | Move the Discover subagent to moxy | `subagents/discover.md` is still in clown. Rec: file a moxy issue, then reclassify as (c). | V,I |
| 7 | Parameterized mkClown | Its motivation is gone. Rec: close or re-scope via an FDR. | R,I |
| 4 | Release notes as moxin resources | Rec: move to moxy. | R,I |

## (c) Blocked on another repo/tool

| # | Title | Blocking repo / issue | Ev |
|---|---|---|---|
| 293 | Wake previews truncated | ringmaster (wake preview text; no `(truncated)` literal in clown). No issue number found. | R,I |
| 274 | Worker git identity / read-only .git | spinclass FDR 0036 steps 3-8; afterwards remove grit from `agentscope.InUnitMoxyfile`. | R |
| 260 | FDR 0019 amendment per ringmaster FDR-0001 | ringmaster#34 (FDR-0001 step-2 RFC). | R |
| 252 | OS isolation for juggler units | ringmaster FDR-0001 step 2, and clown#260. | R |
| 242 | Prompt: async jobs outlive a restart | ringmaster#40 (fragment template); close after a ringmaster bump. | R |
| 240 | Restricted-toolset subagents | spinclass#302. | R |
| 239 | Presence shows dormant sessions | spinclass (companion issue not numbered) plus troupe/ringmaster presence. | R,I |
| 228 | MUC inbound loses the real JID | troupe (`troupe receive` envelope); no issue number. | R,I |
| 213 | troupe replaces parley | amarbel-llc/eng#290 (workstation activation); the clown side has landed (`jobmonitor.go:290-295`). | R |
| 209 | Write(path) rules ignored | Upstream Claude Code permissions; no issue number. | R,I |
| 208 | Subagent Glob/Grep denied | Upstream Claude Code Agent tool grants; no issue number. | R,I |
| 193 | Stale RUNNING / no launch primitive | ringmaster (launch primitive; liveness is ringmaster#33). | R,I |
| 189 | Vanity routing for /clown | Fleet infra/eng proxy config (prior art spinclass#221). | R,I |
| 175 | Per-tool filtering misses httpServers | amarbel-llc/moxy#399. | V |
| 173 | Variable expansion in prompt fragments | ringmaster/troupe (fragments are external); no issue number. | R,I |
| 171 | Quiet DecodeChat noise | troupe (`internal/xmpp/envelope.go`, not in-tree). | V |
| 170 | group-id `/` breaks room JID | troupe (`internal/xmpp/addressing.go`). | V |
| 168 | Extend a running job's timeout | ringmaster and/or moxy async; no issue number. | R,I |
| 167 | chat_send accepts a bad target | troupe; partly mitigated by the strict FQ-JID schema. Unknown: a well-formed JID with no recipient. | V,I |
| 137 | Spinclass reads clown presence | spinclass (FDR-0017 consumer side). | R,I |
| 121 | Spinclass preamble with no session id | spinclass (the preamble text is not in clown; rg). | R,I |
| 94 | Nested sandbox blocks fence | moxy#274 / the just-us plugin. | R,V,I |
| 39 | MCP calls time out at ~60s | amarbel-llc/moxy#201 and amarbel-llc/spinclass#46 (their clown.json timeouts); upstream state unchecked. | R,I |

## (d) Verified stale

| # | Title | Evidence | Ev |
|---|---|---|---|
| 296 | Hold the first turn until MCP connects | `cmd/clown/mcpstartup.go:9-60` (NONBLOCKING=false, STARTUP_WAIT_MS), wired at `main.go:754`, tested in `mcpstartup_test.go:20-48`, unit at `internal/agentscope/unit.go:237`. One live probe is optional. | R |
| 295 | resume re-applies the spawn profile | `cmd/clown/resume.go:219-236`; drift refused at `profilegate.go:138-148,212-230`. | R |
| 294 | "## Session Identity" twice | `cmd/clown/promptfragments.go:49` `mergeSessionIdentity`, wired at `main.go:2472`, count pinned in `promptfragments_test.go:17-19`; 183f506. | R |
| 286 | FDR 0020 per-connection child vs code | `docs/features/0020-agent-scope-confinement.md:781,836-837` now matches the one-child code; 95ca057. | R |
| 257 | Consume the ringmaster reaper / retry done | `internal/jugglerrun/exitwake.go:218-254` `doneWithRetry`; FDR 0019:915-923; `zz-tests_bats/juggler_reaper.bats:60`. | R |
| 233 | OSC-2 title gate ignores stderr | be71143; `cmd/clown/attach.go:111,116` gate on `term.IsTerminal(os.Stderr)`. The live tty check is unrun but optional. | R |
| 215 | Reap chat messages after consumption | The receiver landed at `cmd/clown/jobmonitor.go:280-296` (ephemeral wakes). Per the issue's own comment, the legacy path is wontfix. | R |
| 202 | FDR 0016 phase 1 provider parity | FDR 0016:2 `status: testing`; `cmd/clown/opencode.go:196`; `zz-tests_bats/provider_mcp.bats`. | R,V |
| 195 | OpenRouter dynamic model picker | b96f66d, 52a4273; `cmd/clown/profileform.go:155-159`. | R |
| 188 | Remaining github.com URLs | rg finds none in `man/man1` or `completions/`; `nix/hm/juggler.nix:69` uses the forge. | V |
| 174 | build-go/gomod2nix recipes hang | Neither recipe exists, and there is no go.mod/gomod2nix.toml (AGENTS.md, go.nix). | V |
| 169 | Famous-clown session names | `internal/clownname/clownname.go:1-25`; e478edf, fc469fc, 7bd02c0. Gaps live in #218/#220. | V |
| 157 | Drop zmx as the default multiplexer | `default-clownfile:31` `multiplexer = "posh"`; c188b3f. | V |
| 145 | clownfile [attach] self-wrap | `attach.go:167` `maybeReexecMultiplexer`; `default-clownfile:30-31`; `main.go:307,420`; `internal/clownfile/attach_test.go`. | V,R |
| 99 | Explore a Lima alternative | 8a2a309; `internal/tent/backend_lima.go`; `flake.nix:1743-1747,1898-1910`. | R |
| 98 | Podman command builder | `internal/tent/backend.go:24-58`, `backend_podman.go:36-44`; call sites use `tent.Backend`. | R |
| 96 | HM managed-settings module + hook-allow package | No managed-settings ship (`flake.nix:1148-1162`); the hook ships via `--plugin-dir` (`flake.nix:647-649`). | R,I |
| 95 | claude-code binary patch corrupts the binary | The patch was removed (#133); `claudeCliPath` is unpatched (`flake.nix:1148-1162`); no `mkPatchedClaudeCode`. | R |
| 85 | Registry SHA-sourcing pitfall | `justfile:1470-1492` `install-model-ad-hoc` hashes the downloaded file and cites #85. | R |
| 63 | just 1.50.0 blocks the nixpkgs-master bump | nixpkgs-master is locked at b4fd65b (2026-09-29); just 1.58.0 is on PATH. The comment at `flake.nix:15-21` is out of date. | V,R |
| 62 | claude-code past 2.1.111 | llm-agents claude-code 2.1.150+ (`flake.nix:24-26,147-148,247`); the patchPhase is gone. | R |
| 46 | claude-code baked twice | No managed-settings variants (`flake.nix:1148-1161`); the single unpatched binary is at `:1162`; the 2.1.111 import is unused (`:147-154`). No closure build was run. | R,I |
| 33 | Disable auto-memory via managed-settings | No managed-settings ship (`flake.nix:1148-1161`); the goal moved to #143. Close as a duplicate. | R |
| 9 | Runtime managed-settings test | No managed-settings to test (`flake.nix:1148`); rg finds no `managedSettingsRead`. | V,R |

## Recommended order for (a), smallest risk first

1. #18: TODO(#NNN) prompt fragment (S)
2. #243: no file-based memory fragment (S). It can share a commit and test with #18 and #120.
3. #120: epoch-timestamp rule (S)
4. #56: `juggler -h/--help` (S)
5. #84: `[group]` on ungrouped recipes plus a lint (S)
6. #227: version table in the identity fragment (S)
7. #200: `runAltScreenPicker` helper (S)
8. #159: `--no-attach` (S)
9. #275: profiles man page (S; check #175 first)
10. #235: `skipOrFail` under `CLOWN_NIX_TEST_LANE` (S, but it may turn up real lane failures)
11. #265: disabled-categories fragment (M)
12. #82: per-connection ctx in juggler dispatch (M; keep it juggler-local)

Kept out of the order: #151 and #79, both M with darwin-only verification.

## Verified stale, ready to close (with evidence)

- **#296**: `cmd/clown/mcpstartup.go:9-60`, `main.go:754`, `mcpstartup_test.go:20-48`
- **#295**: `cmd/clown/resume.go:219-236`, `profilegate.go:138-148`
- **#294**: `cmd/clown/promptfragments.go:49`, `promptfragments_test.go:17-19`, commit 183f506
- **#286**: `docs/features/0020-agent-scope-confinement.md:781,836-837`, commit 95ca057
- **#257**: `internal/jugglerrun/exitwake.go:218-254`, `zz-tests_bats/juggler_reaper.bats:60`
- **#233**: commit be71143, `cmd/clown/attach.go:111`
- **#215**: `cmd/clown/jobmonitor.go:280-296`; the issue's own closing condition is met
- **#202**: `docs/features/0016-non-claude-provider-parity.md:2`, `cmd/clown/opencode.go:196`
- **#195**: commits b96f66d, 52a4273; `cmd/clown/profileform.go:155-159`
- **#188**: rg finds no github.com in `man/man1` or `completions/`; `nix/hm/juggler.nix:69`. The files the issue names are done; `man/man7/clown-plugin-protocol.7` still has 2 github.com hits (not in the issue's list, checked 2026-10-25 by rg): close and file the remainder, or sweep it first.
- **#174**: no build-go/gomod2nix recipe, and no go.mod/gomod2nix.toml in the tree
- **#169**: `internal/clownname/clownname.go:1-25`; commits e478edf, fc469fc, 7bd02c0
- **#157**: `default-clownfile:31`; commit c188b3f
- **#145**: `cmd/clown/attach.go:167`, `default-clownfile:30-31`
- **#99**: commit 8a2a309, `internal/tent/backend_lima.go`
- **#98**: `internal/tent/backend.go:24-58`, `backend_podman.go:36-44`
- **#96**: `flake.nix:647-649,1148-1162`
- **#95**: `flake.nix:1148-1162` (patch removed in #133)
- **#85**: `justfile:1470-1492`
- **#63**: flake.lock pins nixpkgs-master at b4fd65b (2026-09-29). Also fix the out-of-date comment at `flake.nix:15-21`.
- **#62**: `flake.nix:24-26,1148-1162`
- **#46**: `flake.nix:147-154,1148-1162`
- **#33**: `flake.nix:1148-1161`; duplicate of #143
- **#9**: `flake.nix:1148`; rg finds no `managedSettingsRead`

Likely closable once the operator confirms (kept in (b)): #176 (in favour of #177), #172, #166, #192, #205, #203, #141, #117, #90, #78, #75 (duplicate of #83), #58 (with #81), #77, #21, #7.

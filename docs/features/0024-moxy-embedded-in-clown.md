---
status: exploring
date: 2026-11-19
issue: clown#138
related: FDR 0019 (juggler agent substrate), FDR 0020 (agent scope confinement), FDR 0021 (headless agent loop), RFC 0002 (clown plugin protocol), RFC 0018 (capability catalog contract), clown#104, clown#153, clown#258, clown#361, moxy#487
promotion-criteria: >
  proposed once moxy's maintainers have agreed which pkg/moxy surface clown
  may depend on (and how the two flakes stop pinning each other), and one of
  the options below is chosen. Nothing here is built.
---

# moxy embedded in clown

> **Design notes only.** Claims not read in source or measured are marked
> **UNVERIFIED**. moxy's internals are moxy's lane: what is said about them
> here is what its exported facade (`pkg/moxy`) documents, not a design for
> moxy.

## Problem Statement

Every clown session runs its own moxy as a separate plugin server. That
moxy and its child servers (configured servers and moxins) are a process
tree clown starts but does not shape:

- **No profile control.** A profile cannot choose moxy's configuration. The
  moxyfile hierarchy is walked from `$HOME` to the working directory, and a
  profile can only hide tools after the fact (`context_excluded`, pushed to
  moxy). So "do not start these servers in most sessions" needs a
  stopgap: a `$CLOWN_MOXY_LAYER` include in the global moxyfile set from a
  profile's `env`.
- **Leaks.** A moxy tree outlives a clown that dies without unwinding:
  orphaned trees can linger after their worktree is gone (clown#361).
  Fixed on clown's side, with a parent watch planned on moxy's.
- **Skew.** moxy and its moxins are versioned apart from clown, and one
  session can load a moxin that another session's moxy refused.

moxy now has an embedding facade (`pkg/moxy`: `Prepare`/`Open`/`Instance`,
moxy 27e0304, moxy#487), so clown could host moxy in process. This record
collects what that would buy and what it would cost, before anyone decides.

## What exists today

- **moxy is a plugin, not part of clown.** moxy's `clown.json` declares one
  `httpServers.moxy` entry (`moxy serve http`, a 300 s timeout, a `/healthz`
  check). clown's pluginhost starts it like any other managed server: in its
  own process group, now with a parent-death signal on Linux (clown#361),
  stopped by the deferred `host.Shutdown()`.
- **moxy is not a clown flake input.** It can't be: moxy's flake pins clown
  (flake.nix's comments at mkJugglerGo and mkJuggler). Whoever assembles the
  bundle supplies moxy. The running plugin is reported as moxy
  0.6.46+ea8544a, which contains 27e0304.
- **Profiles reach moxy two ways.** `context_servers` keeps or drops the
  whole `moxy/moxy` server. `context_excluded["moxy/moxy"]` hides tools by
  name through moxy's `POST /clown/exclude-tools`. The confined path also has
  `moxy_placement`, `moxy_allow_tools` and `moxy_tools` (protocol 3): clown
  writes a fixed ceiling moxyfile for the in-unit moxy
  (agentscope.InUnitMoxyfile) and checks the unit's layers with
  `moxy narrows`.
- **A profile's `env` reaches moxy.** clown sets it into its own environment
  before StartAll, and managed servers inherit `os.Environ()`. That is what
  makes the `$CLOWN_MOXY_LAYER` stopgap work. Confined in-unit servers get
  only their Def.Env.

## What the facade offers (from pkg/moxy's doc comments)

`pkg/moxy.Options` takes exactly one of `Dir` (walk the hierarchy, as the
binary does), `File` (one moxyfile plus its includes) or `Config` (an
already-loaded moxyfile). It also takes:

- `Parent`, a moxyfile the effective one must narrow;
- `MoxinPath` and `SystemMoxinDir`;
- `SessionID`;
- `Permissions` (Delegate by default, or Enforce);
- `Gate`, the policy-broker seat;
- `Shadow`, the ajour shadow observer;
- `NameTemplate` and `Expose`;
- `MetaTools`.

The library reads no environment. `Instance` implements go-mcp's tool,
resource and prompt providers, so a transport can serve it as it would the
proxy, and it supervises the child servers.

## Options

1. **Status quo plus stopgaps.** Keep moxy a plugin. Ship the
   `CLOWN_MOXY_LAYER` include (in the deployed moxyfile) and moxy's parent
   watch (moxy). Cost:
   none in clown. Leaves profile control indirect (an env var naming a file)
   and the version skew as is.
2. **A profile names moxy's layer.** Still a plugin, but a profile key such
   as `moxy_layer` that clown exports to the moxy server, plus a moxy-side
   flag or env var for one extra layer. That is a profile protocol bump
   (pkg/profilelookup), and moxy has to accept the flag. Cost: small in both
   repos; the env-var stopgap becomes a first-class key.
3. **Embed moxy in clown's frontend.** clown links `pkg/moxy` and serves an
   `Instance` to claude over its own endpoint instead of starting
   `moxy serve http`. The profile maps directly onto Options:
   - `Config` with its `disable-servers`, so no layer file is needed;
   - `Permissions` set to Enforce for unattended profiles;
   - `Shadow`, which clown's ajour shadow hook could become;
   - `Gate`, the policy broker clown already runs.
   One process fewer per session and no moxy/moxin version skew inside a
   session, because the moxins still load from `MoxinPath`. Cost:
   - clown takes moxy's whole Go dependency tree into go.nix;
   - the mutual flake pin has to be broken first (moxy would stop pinning
     clown, or the shared code moves out);
   - every moxy fix then needs a clown relock to reach sessions.
4. **Embed in juggler, not clown.** FDR 0021 recommends MCP with enforce
   mode now and embedding "when the library exists, as the end state", in
   the headless loop. That is juggler's lane (clown#258). It does not answer
   the attended claude session's cost.

## Open questions

- **Q1. The flake cycle.** moxy's flake pins clown. What does moxy use clown
  for? Its clown.json protocol, tests or tooling: **UNVERIFIED**. Option 3
  needs that edge gone or turned around. Ask moxy.
- **Q2. Is the facade stable enough to link?** It landed in 27e0304 (FDR 0014
  steps 5 and 6 in moxy). Which of its symbols moxy will keep stable is
  moxy's call.
- **Q3. Memory.** How much does a per-session moxy process cost, without its
  children? The children (configured servers, moxins) stay processes either
  way. **UNVERIFIED**; measure before choosing option 3 on cost grounds.
- **Q4. Confined units.** The in-unit moxy runs inside the agent unit under a
  ceiling clown writes. An embedded moxy in the frontend would move the tool
  plane out of the unit, which FDR 0020 placed inside. The confined path would
  keep a separate moxy either way.
- **Q5. clown#104 and clown#153.** The 2026-10-25 triage says this record
  decides #104 (FDR 0012's moxy peer-dependency docs) and #153 (native
  resource-to-tool projection). Under option 3, #104 is moot, and #153 would
  be clown's to build on Instance's resource provider.
- **Q6. Who owns the moxyfile.** Today it belongs to the user's home
  config (`~/.config/moxy`), managed outside clown. Under option 3 a profile's
  `Config` would override or narrow it, and which wins needs a rule, likely
  "a profile may only narrow" (`Parent`).

## Recommendation (for discussion)

Ship option 1's stopgaps now (in flight). Consider option 2 if
`CLOWN_MOXY_LAYER` proves useful, since it is cheap and keeps moxy a plugin.
Treat option 3 as the end state only after Q1 to Q3 are answered by moxy's
session and a measurement. Do not start it while clown#258 (juggler leaving
clown) and the confined-moxy work are still moving.

## More Information

- clown#138 (this record's issue); clown#361 (leaked plugin servers);
  clown#104, clown#153.
- moxy#487 and moxy 27e0304 (`pkg/moxy`); moxy's parent-watch follow-up
  (tracked in moxy).
- FDR 0021 §"Embed (moxy#487) or call over MCP?", which makes the same
  trade-off for the headless loop.
- docs/plans/2026-10-25-issue-triage.md (#138: "Rec: an FDR; it also
  decides #104 and #153").

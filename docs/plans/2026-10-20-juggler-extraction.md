# Extracting juggler into its own repository (clown#258)

Status: plan. Date: 2026-10-20. Tracking: clown#258 (accepted, deferred
2026-10-14; the operator has since ruled "extract juggler first").

This plan has three parts. Part 1 is an inventory of what belongs to juggler
and what crosses the boundary. Part 2 is the preparation done in place in clown,
in small merges that keep every lane green and clown's behaviour unchanged.
Part 3 is the ordered cutover, saying who does each step.

Evidence was read in this worktree at 4692a09 (file:line references below),
from clown issues #258 #260 #257 #256 #261 #252 #254 #249 #248, from the circus
and roustabout checkouts on this host (read only), and from purse-first's
`dagnabit(7)` source. Anything not read is marked **UNVERIFIED**.

## TL;DR

1. **The Go cut is already almost clean.** Juggler is 10 packages: `cmd/juggler`
   and nine `internal/juggler*` packages, plus two test fixture mains. Only two of
   those import clown code that is not juggler's own:
   - `internal/jugglertools` → `internal/mcphttp`: `PostJSONRPC`, about 70 stdlib lines.
   - `internal/jugglerrun` → `internal/pluginhost`: `ParseHandshake`, `URL`, about 60
     stdlib lines. This one also drags in `staging`, `huh` and `x/term`.

   Prep merge P1 copies both into `jugglertools` and adds a lint that keeps the
   boundary closed.
2. **Clown's Go-level dependence on juggler is small.** `cmd/clown` uses the
   daemon RPC client in `internal/juggler` (resolve and list models) and one dead
   picker that uses `internal/jugglermodels`. After the cut, juggler must export
   that client from a public package. clown cannot import another module's
   `internal/`.
3. **The real coupling is in nix.** It consists of:
   - `mkJugglerGo` and `juggler-go`
   - `JugglerCliPath`, which is burned into clown
   - `mkJuggler` (despite the name, this is clown's own package builder and stays in clown)
   - the godyn test lane
   - three bats files, two fixtures and three man pages
   - `homeManagerModules.juggler`
   - the `nixpkgs-llama` input, which only juggler uses
4. **circus consumes juggler through `clown.packages.<system>.juggler`**
   (`circus/infra/hosts/flake.nix:668`). It also reaches juggler indirectly
   through `clown.lib.<system>.mkJuggler` (`infra/hosts/lib/clown.nix:151`,
   `eng/lib/circus.nix:132`). The first must be repointed. The second keeps
   working because clown burns juggler in from its new input.
5. **Recommendation:** preserve history with `git filter-repo`, using a path list
   plus a message rewrite of bare `#N` to `clown#N`. Keep the current
   `cmd/juggler` and `internal/juggler*` layout through the cut and do any
   dagnabit relayout afterwards in the new repo, so that import paths change only
   once in clown.

## Part 1 — Inventory

### 1.1 Juggler's packages

The module path is `code.linenisgreat.com/clown` (`go.nix:18`). The packages are
listed in dependency order:

| Height | Package | Imports from the clown module |
|---|---|---|
| 0 | `internal/juggler` (RPC frames, socket paths, remote-model registry) | none |
| 0 | `internal/jugglermodels` (GGUF registry; `//go:embed registry.json`) | none |
| 0 | `internal/jugglereval` (gojq evaluator) | none |
| 0 | `internal/jugglerdecide` (Decisions-API router) | none |
| 0 | `internal/jugglerrun/jugglerruntest` (fakes) | none |
| 1 | `internal/jugglerloop` | `juggler` |
| 1 | `internal/jugglerbrief` | `jugglereval` |
| 2 | `internal/jugglertools` | `jugglerloop`, **`mcphttp`** (client.go:34) |
| 3 | `internal/jugglerrun` | all of the above, **`pluginhost`** (toolplane.go:16) |
| — | `cmd/juggler` | `juggler`, `jugglerrun`, `jugglermodels`, `jugglerbrief`, `jugglerdecide`; tests use `jugglerruntest` |
| — | `cmd/juggler/testdata/fake-platform` (main) | `jugglerruntest` |
| — | `cmd/juggler/testdata/fake-llama-server` (main) | none |

No other clown package is imported only by juggler. There are no import cycles.

The comments at `cmd/juggler/buildcfg.go:10-13` and
`internal/buildcfg/buildcfg.go:29-31` say juggler "imports nothing from clown".
That becomes true after P1.

### 1.2 Back-imports (juggler → clown) to break

| Site | Symbols | Other clown importers | Disposition |
|---|---|---|---|
| `internal/jugglertools/client.go:34,257` | `mcphttp.PostJSONRPC` (+ `ExtractSSEData`) | `cmd/clown-stdio-bridge`, `internal/mcpcollapse`, `internal/pluginhost`, `cmd/clown-mcp-collapse` | copy into `jugglertools` (P1) |
| `internal/jugglerrun/toolplane.go:16,112-117` | `pluginhost.ParseHandshake`, `Handshake.URL` | `cmd/clown`, `cmd/clown-plugin-host`, `cmd/clown-mcp-collapse` | copy into `jugglertools` (P1) |

The other option is to share these through clown's `go-pkgs`. That would make
the juggler module depend on clown while clown depends on juggler, which is a
module cycle. Copying avoids it. The handshake line format is specified in
`clown-plugin-protocol(7)`. A wire spec is the right thing to share; a Go package
is not.

No juggler package imports any of these: `buildcfg`, `profile`,
`pkg/profilelookup`, `agentscope`, `staging`, `promptwalk`, `userpath`,
`clownfile`, `sessions`. #258 lists "buildcfg, clownfile, pluginhost, staging"
as the entanglements to clear. Of those, `pluginhost` (and `staging` through it)
is the only one that is real today. `buildcfg` was split earlier into
`cmd/juggler/buildcfg.go`.

### 1.3 Forward imports (clown → juggler) that stay in clown

| Site | Symbols |
|---|---|
| `cmd/clown/jugglerresolve.go:8` | `juggler.SocketPath`, `NewClient`, `(*Client).ResolveModel`, `ListModels`, `Close`, `ResolveModelParams`, `ResolveModelResult`, `ModelKind` |
| `cmd/clown/main.go:29,914` | `juggler.ModelKindRemote` (in `applyNamedProfile`) |
| `cmd/clown/juggler.go:11,26-27` | `jugglermodels.Dir`, `List` in `pickJugglerModel`, which nothing calls (kept for "plan 2", main.go:2245) |
| tests: `cmd/clown/jugglerresolve_test.go`, `main_test.go` | `juggler.Envelope`, `ReadFrame`, `WriteFrame`, `Error`, `MethodResolveModel`, `MethodListModels` |

Clown also depends on juggler in ways that are not Go imports. These stay in
clown and depend only on the juggler binary or on conventions:
- `buildcfg.JugglerCliPath` (`internal/buildcfg/buildcfg.go:6`), used by
  `resolveProvider` (main.go:2499) and by `synthJugglerPluginDir`
  (`cmd/clown/jugglermonitor.go:41-92`, which runs `juggler mcp`).
- The `--provider juggler` stub, `runJuggler` (main.go:2235-2257).
- `readJugglerPortfile` (`cmd/clown/opencode.go:274-295`), which reads
  `~/.local/state/juggler/llama-server.port`.
- The legacy `~/.config/juggler/` fallback (`internal/userpath/userpath.go:25,41`).
- The deliberate omission of `juggler-prompt` from the allow list in
  `cmd/clown-hook-allow/main.go:57-69`.

### 1.4 External Go modules juggler uses

These come from `go.nix` and the import statements.
- ringmaster `pkgs/jobwake`. Juggler exact-matches ProtocolVersion 3, per #256
  and #257. It is shared with clown.
- tommy `pkg/marshal`, shared with clown.
- `BurntSushi/toml`, shared with clown.
- `charmbracelet/bubbles` and `bubbletea`, plus `x/term`, from `cmd/juggler/download.go`.
  All three are shared with clown.
- `itchyny/gojq` (and `timefmt-go`). **Only juggler uses these**, so clown can drop
  them from go.nix after the cut.
- purse-first/dewey and go-mcp are **not** used by juggler.

The juggler binary also calls these at runtime, with paths burned in through
`cmd/juggler/buildcfg.go` ldflags: `ringmaster`, `troupe`, `systemd-run`,
`systemctl`, `llama-server`, and optionally `moxy`. Juggler also shells out to
`moxy narrows` and `moxy show-effective-moxyfile` (#248, #254).

### 1.5 Nix wiring in `flake.nix`

| Lines | What | After the cut |
|---|---|---|
| 30-32, 159, 1164 | `nixpkgs-llama` input → `llamaServerPath`; used only by juggler | moves to juggler's flake; clown drops it |
| 291-307 | `buildClownGo` (`manifest = ./go.nix`) | juggler gets its own `buildGoAuto` call over its own go.nix |
| 972-1017 | `mkJugglerGo { moxyPath }`, `juggler-go`; ldflagsX `main.{LlamaServer,Ringmaster,Troupe,SystemdRun,Systemctl,Moxy}Path` | moves; juggler's flake exposes `lib.mkJugglerGo` (or `packages.juggler` + an override) so clown's `mkJuggler` can still burn in a consumer's moxy |
| 881-911 | `mkClownGo { jugglerGo ? juggler-go }` → `buildcfg.JugglerCliPath` | stays; the default becomes `juggler.packages.${system}.default` |
| 1334-1355 | `mkClownPkg` adds `jugglerGo` to the symlinkJoin | stays |
| 1391-1456, 1835 | `mkJuggler` (clown's package builder; picks `effectiveMoxy`, builds a moxy-burned juggler) | stays and keeps its name (downstream `clown.lib.mkJuggler` callers); calls juggler's `mkJugglerGo` |
| 1059-1088 | `fake-llama-server-go`, `fake-platform-go` | move |
| 1105-1124 | `mkClownGodynTests` sets `CLOWN_TEST_FAKE_LLAMA_SERVER` for `cmd/juggler/launcher_test.go:20` | moves with the juggler test lane |
| 474-539 | `batsBinaries`: `JUGGLER_BIN`, `FAKE_LLAMA_SERVER_BIN`, `FAKE_PLATFORM_BIN` | move to juggler's bats lane |
| 1212, 1222-1223 | `clownAuthoredManPages`: `juggler.1`, `juggler.7`, `juggler-testing.7` | move |
| 1769-1773 | `packages.juggler = juggler-go` | stays as a re-export of the input for one release (circus reads it), then is dropped |
| 1870-1878 | `homeManagerModules.juggler` (`nix/hm/juggler.nix`) | moves; clown re-exports for one release |

`nix/hm/juggler.nix:2` refers to `docs/plans/2026-05-18-juggler-control-plane.md`,
which does not exist. The file is `2026-05-18-ringmaster-control-plane.md`. P2
fixes this.

### 1.6 Tests

- **Go unit tests.** Juggler has 54 `_test.go` files across its packages. All
  run in `clown-godyn-tests` (`just test-go-godyn`). They move with the code.
- **Clown-side tests that stay:** `cmd/clown/jugglerresolve_test.go`,
  `main_test.go` (316-565, 1013-1046), `jugglermonitor_test.go`,
  `staging_test.go`, `profileform_test.go`.
- **Bats** (`zz-tests_bats/`, run by `bats-default`):
  - `juggler.bats` (daemon and models) and `juggler_lifecycle.bats` (about 55
    tests: spawn, decide, run, resolve, exit-wake, clean) **move**.
  - `juggler_reaper.bats` needs only `RINGMASTER_BIN` and tests `ringmaster reap`.
    It belongs with juggler's reaper use (#257), so move it. Whether ringmaster
    should own it instead is a question for the ringmaster session.
  - `lib/common.bash` (`require_bin`) is copied.
- **Justfile recipes that move:** `verify-juggler`, `verify-juggler-multi`,
  `install-model-*`, `debug-probe-real-llama-server`, `debug-juggler-api`, and
  the juggler halves of `debug-godyn-*`.
- **Justfile recipes that stay**, because they use the binary as a client:
  `run-build-juggler`, `run-juggler` (these are `mkJuggler`), the tailnet
  `verify-*` recipes, and `zz-explore`'s crush and `mkJuggler` explorations.

### 1.7 Docs and man pages

- **Move:** `man/man1/juggler.1`, `man/man7/juggler.7`, `man/man7/juggler-testing.7`.
- **Move as the live record:**
  - FDR 0019. Clown keeps a stub with status `relocated`, a banner and
    `relocated-to`, as #258 asks. **UNVERIFIED:** whether `relocated` is an
    accepted status in the eng FDR lifecycle. Check `/eng:fdr` at cutover.
  - The juggler plans `2026-07-07-juggler-model-registry*` and
    `2026-07-11-juggler-subagent-tool*` (history only; they can stay in clown).
- **Stay, but get references edited at cutover:**
  - `clown.1` (mkJuggler), `clownfile.5` (`.Xr juggler 7`)
  - FDRs 0016, 0017, 0018, 0020, 0021
  - RFC 0017
  - AGENTS.md (lines 9, 14, 19-24, 135), README.md (15, 60-103)
- **FDR 0021** keeps the headless loop in `internal/jugglerloop`, with "the brief
  is the seam" (§2.4, open question Q8). The loop and its permission filter
  (#261) are `juggler run` code and move. `agentscope`, `promptwalk` and `staging`
  stay in clown and reach the loop only as strings and paths inside the brief
  (risk 7). This plan keeps that seam: P1 removes the only Go edge that crossed it.

### 1.8 Consumers outside clown (read only)

- **circus `infra/hosts/flake.nix`**:
  - `clown.url = "git+https://code.linenisgreat.com/clown.git"` (195).
  - `juggler = clown.packages.${system}.juggler;` and
    `ringmaster = clown.packages.${system}.ringmaster;` (668-669). Comments at 199-204
    say the graph's producers take "the rev juggler itself links".
- **circus `mkClown`** (`infra/hosts/lib/clown.nix:151`) and
  `eng/lib/circus.nix:132` call `clown.lib.${system}.mkJuggler`. They are
  unaffected if `mkJuggler` keeps its signature.
- **circus glue** (`pebble-graph-glue`, `issue-filer-glue`,
  `krone/pebble-webhook-origin.nix`): these are CLI consumers of `juggler
  spawn/decide/resolve/job-ledger/clean` and of the state dir
  `$XDG_STATE_HOME/juggler`. The extraction does not change any of that. #260
  (post-step-2) does.
- **roustabout:** its conformance suite uses a `fake-juggler` stand-in. Its
  `flake.nix` reaches clown only through `just-us.inputs.clown`. It is not a
  consumer today. It decides library versus binary for itself (#258).
- **spinclass:** links only `pkg/profilelookup` (clown#264), which imports no
  juggler package (its `lookup.go:128` only comments on the legacy juggler
  config fallback). It is unaffected.
- **moxy:** moxy's flake pins clown (flake.nix:984-986 comment). The juggler flake
  **must not** take moxy as an input either, or juggler and moxy would pin each
  other through clown. Keep `MoxyPath` burned in by the consumer, as today.

## Part 2 — Preparation in place (this worktree, live-ebony)

Each step is one merge. None of them deletes anything, and none changes clown's
or juggler's behaviour.

- **P1. Close the Go boundary.**
  - Add `jugglertools/handshake.go` (`ParseHandshake`, `Handshake.URL`) and
    `jugglertools/transport.go` (`postJSONRPC`, `extractSSEData`).
  - Point `jugglertools/client.go` and `jugglerrun/toolplane.go` at them.
  - Port the matching unit tests.
  - Add the flake check `checks.juggler-boundary`, a grep over `goSrc`. It fails
    if any `.go` file under `cmd/juggler` or `internal/juggler*` imports a
    `code.linenisgreat.com/clown/...` path outside that set. It runs in
    `nix flake check` and in `just` through a new `lint-juggler-boundary`
    recipe.
  - `internal/pluginhost` and `internal/mcphttp` are not changed.
- **P2. Doc truth-ups** (landed in the same merge as P1). Fix the dead plan
  reference in `nix/hm/juggler.nix:2`. Point the "imports nothing from clown"
  comments at the new check.
- **Deliberately not done in clown:**
  - **A dagnabit relayout** (`internal/0/…`, `internal/alfa/…`, a `pkgs/` facade,
    as ringmaster does). It would rewrite every juggler import path in clown, and
    the cutover rewrites them again (`code.linenisgreat.com/clown/...` →
    `code.linenisgreat.com/juggler/...`). Do it once, in the new repo, as its first
    follow-up. The current heights (§1.1: 0/1/2/3) already map one-to-one onto
    `0`/`alfa`/`bravo`/`charlie`.
  - **Moving the clown-facing RPC client into a public package.** That package's
    path is the new repo's to choose. Proposed: `pkgs/jugglerrpc`, a facade over
    `internal/juggler`'s `SocketPath`, `NewClient`, `Client`, the
    `ResolveModel`/`ListModels` types, and the frame helpers the clown tests use.
  - **Moving `mkJugglerGo` into its own nix file.** It is short, and it moves with
    the flake rewrite.
  - **Removing the dead `pickJugglerModel`.** This plan deletes nothing. The
    operator should decide whether to drop it at cutover (it is clown's last
    `jugglermodels` import).

## Part 3 — Cutover

### 3.1 Decisions needed before cutover (operator)

1. **Which shape to extract.** #258 (2026-10-14) and #260 ask this first: extract
   today's juggler, or the post-step-2 shape (`juggler run` only)?
   **Recommendation:** extract today's shape. Ringmaster FDR-0001 has "nothing
   implemented yet" (#260). Waiting would tie this to the ringmaster service
   schedule, and the new repo can shed `spawn`, `exit-wake`, the lock and the hook
   at step 2 with its own FDR. #252 already says no confinement code goes into
   `juggler spawn`.
2. **Repo name and host.** No issue names it. **Proposed:**
   `linenisgreat/juggler` on forge.starbrandshoes.com, module path
   `code.linenisgreat.com/juggler`, following ringmaster and roustabout.
   Per circus#310, the vanity-plane visibility of a new repo is today a manual
   forge-side setting, not automatic. It is part of step 1 below.
3. **Open juggler work in flight.** #257 (open; touches `juggler spawn` and the
   exit-wake hook) must land before the history cut or be transferred to the new
   repo. #255 and #250 were not read; their state is **UNVERIFIED**.
4. **Clown's Go dependency on juggler.** Either (a) clown imports the new repo's
   `pkgs/jugglerrpc` through go.nix `flakeInputs` (recommended; one
   wire-protocol owner), or (b) clown keeps a private copy of the
   resolve/list client, about 60 lines, plus frame helpers for tests, and depends
   on juggler only as a binary. (b) avoids a Go edge. It also means two copies of
   the daemon wire format can drift.

### 3.2 Ordered steps

| # | Step | Who |
|---|---|---|
| 0 | Merge P1/P2 here; land or transfer #257 | clown (worker) |
| 1 | Create the forge repo `juggler` (§3.4). circus#310's one-verb `circus repo-create` does not exist yet, so this is today's manual sequence, as was done for roustabout | operator / circus |
| 2 | Produce the history (§3.3) from clown master at a pinned sha `S`, in a scratch clone outside clown's worktrees | clown worker, or the operator |
| 3 | On top of that history, in the new repo, add one commit with each of these: rewrite imports `code.linenisgreat.com/clown/` → `code.linenisgreat.com/juggler/`; write `go.nix` (module path, ringmaster + tommy as `flakeInputs`, toml, gojq, bubbles/bubbletea, x/term), `flake.nix` (`packages.default`, `lib.mkJugglerGo`, fixtures, `checks` = godyn tests + bats lane + manpages + formatting, `homeManagerModules.juggler`, `go-pkgs` via `mkGoPkgs` for the RPC facade), `justfile` (default: lint build test), `sweatfile`, `AGENTS.md`, `version.env`; add the `pkgs/jugglerrpc` facade; move the bats files and `lib/common.bash` | new juggler session |
| 4 | Push and tag `v0.1.0` (eng-versioning(7)) | operator (push rights), juggler session prepares |
| 5 | In clown, one merge: add a `juggler` flake input (follows: nixpkgs, igloo, ringmaster, troupe, tommy); add juggler to go.nix `flakeInputs`; repoint `cmd/clown` imports to `pkgs/jugglerrpc`; make `mkClownGo`'s default `jugglerGo` the input's package and have `mkJuggler` call `juggler.lib.${system}.mkJugglerGo { moxyPath }`; keep `packages.juggler` and `homeManagerModules.juggler` as re-exports; drop `nixpkgs-llama`, gojq/timefmt from go.nix, the juggler bats binaries and the juggler man pages | clown worker |
| 6 | In the same or the next clown merge: `git rm` `cmd/juggler`, `internal/juggler*`, the three bats files and the three man pages; replace FDR 0019 with a `relocated` stub; update AGENTS.md, README.md, `clown.1`, `clownfile.5` and FDR 0021 references | clown worker |
| 7 | circus: switch `infra/hosts/flake.nix:668` to a `juggler` input (or keep `clown.packages.juggler` while the re-export lasts). Keep `ringmaster = juggler's ringmaster` so the producers' rev still matches what juggler links (comment at 199-204) | circus session |
| 8 | Deploy, in the order from #256: ringmaster → clown/spinclass/troupe → krone | coordinator (circus) |
| 9 | After one release: drop clown's `packages.juggler` and `homeManagerModules.juggler` re-exports | clown worker |

Steps 5 and 6 are kept apart only so that step 5 can be reverted on its own
while the code still exists in clown.

### 3.3 History

**Recommendation: `git filter-repo`, keeping juggler's history.** Run it in a
fresh clone at `S`:

```
git filter-repo \
  --path cmd/juggler/ \
  --path-glob 'internal/juggler*' \
  --path zz-tests_bats/juggler.bats \
  --path zz-tests_bats/juggler_lifecycle.bats \
  --path zz-tests_bats/juggler_reaper.bats \
  --path man/man1/juggler.1 \
  --path man/man7/juggler.7 \
  --path man/man7/juggler-testing.7 \
  --path docs/features/0019-juggler-agent-substrate.md \
  --path nix/hm/juggler.nix \
  --replace-message msg-rewrite.txt
```

Here `msg-rewrite.txt` turns a bare issue reference (`#N` not already prefixed
by a repo name) into `clown#N`, so old commit messages do not point at the new
repo's issue numbers. The exact regex needs testing on a scratch run. Check
`git log --grep '#[0-9]'` before and after.

- **Pros:** `git blame` and `log -- <path>` keep working for the FDR 0019 arc.
  The commits that cite clown issues stay meaningful. This is what FDR 0014 did
  for ringmaster. **UNVERIFIED:** whether ringmaster's extraction actually used
  filter-repo; check `git log` in ringmaster.
- **Cons:** commits that touched both sides become partial, keeping only
  juggler's files. Pre-rename history of files that were moved into these paths
  is lost unless those old paths are added too: juggler's daemon came from the
  old `cmd/ringmaster` daemon (flake.nix:1019-1023), and that code also left with
  ringmaster. It takes a scratch run and a review pass before pushing, and it
  needs `git-filter-repo` on the host (**UNVERIFIED:** whether it is in any
  devShell).
- **Alternative: a fresh start.** Make one import commit of the tree at `S`, with
  its message naming `clown@S` and the path list. It is simpler and cannot carry
  surprises. The cost is that blame stops at the import, and every "why" goes
  back to clown's log. This is acceptable if the operator values a clean start
  over archaeology. Clown's history keeps everything either way, because step 6
  only deletes going forward.

### 3.4 Creating the repository (circus#310)

circus#310 lives on GitHub (`amarbel-llc/circus`), not on the forge; that
is why smith's forge lookup could not find it. Read through cutting-garden at
`smith://gh-amarbel-llc/amarbel-llc/circus/issues/310`, 2026-10-20, state open.

It describes how a fleet repo is created today, in four manual steps:

1. **Create the empty repo on the forge:** `just infra/hosts/forgejo-create-repo
   juggler linenisgreat org [private] [desc]` in circus. This auto-initialises
   the default branch, and the repo is private by default. Labels, units, branch
   protection, the GitHub push mirror (`forgejo-add-push-mirror`), vanity-plane
   visibility and the description are then set by hand.
2. **Clone it** into the operator's repos directory, using the owner-free SSH
   clone URL (papi#85).
3. **Bootstrap the tree:** `conformist conform api.linenisgreat.com` with the
   domain's PAPI flake template, then `--repair`.
4. **Commit and push**, and apply the forge settings the fleet expects (default
   branch, labels from the org template, the mirror).

The `circus repo-create NAME [--owner] [--template] [--public] [--dry-run]` verb
it asks for would do all four in order and refuse at the first unclean step. If
it lands before the cutover, step 1 becomes one command.

**Interaction with the history choice (§3.3).**
- **filter-repo:** the forge's auto-init commit and the `conform` bootstrap both
  create history that is unrelated to the filtered history. Push the filtered
  history first, to the empty repo (create it without auto-init, or force-push
  over the init commit before anything else lands). Then run `conformist conform
  --repair` on top of it as the plan's step 3 commit.
- **Fresh start:** bootstrap with `conform` first, then add the import commit.

**UNVERIFIED:** whether `forgejo-create-repo` can skip auto-init. Check the
recipe in `circus/infra/hosts/justfile` before step 1.

## Open questions for the operator

1. Current shape or post-step-2 shape? (Recommendation: current.)
2. Repo name, host and module path? (Proposed: `juggler` at
   `code.linenisgreat.com/juggler`.)
3. History: filter-repo or fresh start? (Recommendation: filter-repo.)
4. Clown's RPC client: import the facade from juggler, or keep a private copy?
5. Should `juggler_reaper.bats` go to juggler or to ringmaster?
6. Drop the dead `pickJugglerModel` at cutover?
7. Create the repo by hand now (§3.4), or wait for circus#310's
   `circus repo-create`?

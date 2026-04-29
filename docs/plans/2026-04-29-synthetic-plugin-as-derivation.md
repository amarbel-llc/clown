# Synthetic Plugin as Nix Derivation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Eliminate worktree pollution from `just` by turning the synthetic plugin and the stdio mock into Nix derivations whose outputs live in `/nix/store`, not the source tree.

**Architecture:** Two new Nix packages in `flake.nix`. `mock-stdio-mcp` is a bare Go binary built via `buildGoApplication`. `synthetic-plugin` is a `runCommand` derivation that wraps the existing source fixture (`clown.json`, `agents/`, `.claude-plugin/`, `.clown-plugin/`) plus a freshly compiled `mock-mcp-server` binary into a single store path with the layout the plugin host expects. Test recipes consume the store paths via `nix build --no-link --print-out-paths`. The `inspect-compiled` test driver is conceptually a test harness, not a plugin component, so it moves to `tests/scripts/`.

**Tech Stack:** Nix flakes, `gomod2nix.legacyPackages.${system}.buildGoApplication`, `pkgs.runCommand`, `lib.fileset.toSource`, just recipes.

**Rollback:** `git revert` the commits. The old `build-mock-server` / `build-mock-stdio-mcp` recipes and the source-tree `bin/mock-*` paths come back.

**Manifest rewriting:** `tests/synthetic-plugin/clown.json` ships in source with `"command": "bin/mock-mcp-server"` (a relative path). Although that relative path *would* resolve correctly inside `$out`, this project prefers absolute paths in plugin manifests so behavior doesn't depend on the host's CWD. The synthetic-plugin derivation uses `substituteInPlace` with `--replace-fail` to rewrite the relative path to the absolute store path `$out/bin/mock-mcp-server` at build time. `--replace-fail` errors if the pattern isn't found, catching drift if the source `clown.json` is ever edited (matches the project's existing `substituteInPlace --replace-fail` pattern in `flake.nix:320` and `flake.nix:380`).

---

## Task 0: Verify pre-conditions

**Promotion criteria:** N/A.

**Files:**
- Read: `flake.nix`, `justfile`, `tests/synthetic-plugin/clown.json`

**Step 1: Confirm clean working tree**

Run: `git status`
Expected: `nothing to commit, working tree clean` (untracked binaries from prior `just` runs are OK and will be cleaned by Task 7).

**Step 2: Confirm `goSrc` covers the testdata directories**

Run: `grep -A 8 'goSrc =' flake.nix`
Expected: `fileset.unions` includes `./internal` (which contains `internal/pluginhost/testdata/mockserver` and `internal/pluginhost/testdata/mockstdiomcp`).

If `./internal` is not in the unions list, STOP and surface this — the testdata Go sources won't be visible to `buildGoApplication` and the plan needs a workaround.

**Step 3: Note the current baseline**

Run: `git log --oneline -1`
Expected: a commit SHA. Record it; if anything goes wrong, `git reset --hard <sha>` returns to baseline.

---

## Task 1: Move `inspect-compiled` to `tests/scripts/`

**Promotion criteria:** N/A. Delete the empty source `bin/` afterward.

**Files:**
- Move: `tests/synthetic-plugin/bin/inspect-compiled` → `tests/scripts/inspect-compiled`
- Modify: `justfile` (one path reference, line ~160)

**Step 1: Move the file with git**

Run:
```
mkdir -p tests/scripts
git mv tests/synthetic-plugin/bin/inspect-compiled tests/scripts/inspect-compiled
```
Expected: `inspect-compiled` is staged as a rename.

**Step 2: Update the justfile reference**

In `justfile`, find the line `-- "$plugin_dir/bin/inspect-compiled" 2>&1) || {` (around line 160 in `test-plugin-host`).

Change to: `-- "$(pwd)/tests/scripts/inspect-compiled" 2>&1) || {`

**Why `$(pwd)` and not relative?** The recipe sets `plugin_dir` to the *store path* later (Task 5), at which point `$plugin_dir/bin/...` would point inside the store, where `inspect-compiled` no longer lives. Pinning the path to the worktree's `tests/scripts/` is the right semantic.

**Step 3: Confirm no other references to the old path**

Run: `rg 'tests/synthetic-plugin/bin/inspect-compiled'`
Expected: no matches.

Run: `rg 'inspect-compiled'`
Expected: only matches at `tests/scripts/inspect-compiled` (the file itself) and one in `justfile` (the reference you just updated).

**Step 4: Verify the source `bin/` directory is empty**

Run: `ls tests/synthetic-plugin/bin/`
Expected: empty (only stale `mock-*` outputs from a prior `just` run, which are gitignored-via-untracked, not tracked).

If anything tracked is still there, surface it — the plan's assumption was wrong.

**Step 5: Don't run `just` yet** — `test-plugin-host` will fail at this point because the source-tree `bin/mock-mcp-server` no longer matches the store path the future derivation will produce. Tasks 2–5 are required before things work again.

**Step 6: Commit**

```
git add tests/scripts/inspect-compiled tests/synthetic-plugin/bin/inspect-compiled justfile
git commit -m "refactor(tests): relocate inspect-compiled out of synthetic-plugin

inspect-compiled is a test harness driver, not a plugin component —
it never appears in clown.json and is invoked directly by the
test-plugin-host recipe. Co-locating it inside the plugin fixture's
bin/ directory has been a source of confusion (mixed source +
build outputs).

Part of the synthetic-plugin-as-derivation refactor. The next
commits add Nix derivations that take over from the go-build-into-
source-tree pattern."
```

---

## Task 2: Add `mock-stdio-mcp` derivation to `flake.nix`

**Promotion criteria:** `build-mock-stdio-mcp` justfile recipe can be removed once `test-stdio-bridge` consumes the derivation output (Task 5).

**Files:**
- Modify: `flake.nix:163-174` area — add a new `mock-stdio-mcp` derivation alongside `clown-stdio-bridge`.
- Modify: `flake.nix:563-565` — expose it via `packages.mock-stdio-mcp`.

**Step 1: Read the existing `clown-stdio-bridge` derivation as a template**

Run: `grep -n -A 11 'clown-stdio-bridge = buildGoApplication' flake.nix`
Expected: lines 163-174.

**Step 2: Add the new derivation after `clown-stdio-bridge`**

Insert this block right after the `clown-stdio-bridge = buildGoApplication { ... };` declaration (around line 174, before the `clown-hook-allow` block):

```nix
        # Mock stdio MCP server used by the test-stdio-bridge integration
        # test. Built as a derivation so the test recipe consumes a store
        # path instead of dropping a binary into the worktree.
        mock-stdio-mcp = buildGoApplication {
          pname = "mock-stdio-mcp";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockstdiomcp" ];
          modules = ./gomod2nix.toml;
          ldflags = [ "-s" "-w" ];
        };
```

**Why `subPackages = [ "internal/pluginhost/testdata/mockstdiomcp" ]`?** That's the path inside `goSrc` where the `package main` lives. `buildGoApplication` infers the binary name from the *last path segment* (`mockstdiomcp`), NOT from the `pname`. So the binary will land at `$out/bin/mockstdiomcp`, NOT `$out/bin/mock-stdio-mcp`. Task 5 must use `$out/bin/mockstdiomcp` when consuming this derivation, OR Task 2 must use a `runCommand` to rename. We use the latter for consistency with how the binary is referenced today.

**Step 3: Decide between renaming and accepting `mockstdiomcp` as the binary name**

This is a design call. Two options:

- **(a) Accept `$out/bin/mockstdiomcp`** — simplest, but the test recipe and any debug invocations will need to use the new name.
- **(b) Wrap with `runCommand` to rename to `mock-stdio-mcp`** — preserves the existing name, costs a tiny extra derivation.

This plan picks **(b)** to minimize changes elsewhere. Replace the block from Step 2 with:

```nix
        # Mock stdio MCP server used by the test-stdio-bridge integration
        # test. Built as a derivation so the test recipe consumes a store
        # path instead of dropping a binary into the worktree. The
        # buildGoApplication output is wrapped in runCommand to preserve
        # the historical "mock-stdio-mcp" binary name (Go's default
        # would be "mockstdiomcp" — the leaf of the subPackage path).
        mock-stdio-mcp-go = buildGoApplication {
          pname = "mock-stdio-mcp";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockstdiomcp" ];
          modules = ./gomod2nix.toml;
          ldflags = [ "-s" "-w" ];
        };

        mock-stdio-mcp = pkgs.runCommand "mock-stdio-mcp" { } ''
          mkdir -p $out/bin
          cp ${mock-stdio-mcp-go}/bin/mockstdiomcp $out/bin/mock-stdio-mcp
        '';
```

**Step 4: Expose the derivation in the top-level `packages` attrset**

In `flake.nix`, find the block:
```nix
      {
        packages.default = mkClownPkg emptyPluginMeta;
        packages.clown-manpages = clown-manpages;
```

Add a line:
```nix
        packages.mock-stdio-mcp = mock-stdio-mcp;
```

**Step 5: Verify the derivation builds**

Run: `nix build .#mock-stdio-mcp --no-link --print-out-paths`
Expected: a `/nix/store/...-mock-stdio-mcp` path on stdout.

If the build fails complaining about a missing package or a `goSrc` issue, STOP and surface the error — the plan's assumption about `goSrc` covering the testdata is wrong and needs revision.

**Step 6: Verify the binary actually exists**

Run: `ls $(nix build .#mock-stdio-mcp --no-link --print-out-paths)/bin/`
Expected: a single entry, `mock-stdio-mcp`.

**Step 7: Commit**

```
git add flake.nix
git commit -m "feat(flake): add mock-stdio-mcp derivation

Wraps internal/pluginhost/testdata/mockstdiomcp as a Nix package so
the test-stdio-bridge recipe can consume a store path instead of
dropping a binary into tests/synthetic-plugin/bin/. The runCommand
wrapper preserves the historical mock-stdio-mcp binary name —
buildGoApplication would default to mockstdiomcp (the subPackage
leaf)."
```

---

## Task 3: Add `synthetic-plugin` derivation to `flake.nix`

**Promotion criteria:** `build-mock-server` justfile recipe can be removed once `test-plugin-host` consumes the derivation output (Task 5).

**Files:**
- Modify: `flake.nix` — add `synthetic-plugin` and a supporting `mock-mcp-server-go` derivation.
- Modify: `flake.nix` — expose `packages.synthetic-plugin`.

**Step 1: Add a `mock-mcp-server-go` derivation (private, used only by `synthetic-plugin`)**

Place this after the `mock-stdio-mcp` block from Task 2:

```nix
        # Compiled binary that the synthetic-plugin derivation embeds.
        # Not exposed as a top-level package — consumers should use
        # synthetic-plugin instead, which lays out the full plugin dir.
        mock-mcp-server-go = buildGoApplication {
          pname = "mock-mcp-server";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockserver" ];
          modules = ./gomod2nix.toml;
          ldflags = [ "-s" "-w" ];
        };
```

Note: same `subPackages` leaf-name issue as Task 2 — the binary will be at `$out/bin/mockserver`. We rename it via `runCommand` inside the synthetic-plugin derivation, not here.

**Step 2: Add the synthetic-plugin source fileset**

The plugin's static content (manifest, agents, plugin metadata) lives in `tests/synthetic-plugin/`. We need a `lib.fileset`-based source so Nix only sees the right files. Add this near the other fileset definitions (after `goSrc`):

```nix
        syntheticPluginSrc = lib.fileset.toSource {
          root = ./tests/synthetic-plugin;
          fileset = lib.fileset.unions [
            ./tests/synthetic-plugin/clown.json
            ./tests/synthetic-plugin/.claude-plugin
            ./tests/synthetic-plugin/.clown-plugin
            ./tests/synthetic-plugin/agents
          ];
        };
```

**Why an explicit fileset and not `./tests/synthetic-plugin` directly?** Two reasons:
1. The source tree's `bin/` directory may exist with stale outputs from prior `just` runs. Including the directory wholesale would pull those into the Nix store unnecessarily and make the derivation hash unstable.
2. Explicit allowlists are easier to audit than `cleanSource`-style filters.

**Step 3: Add the synthetic-plugin derivation**

After `mock-mcp-server-go`, add:

```nix
        # Synthetic plugin used by the test-plugin-host integration test.
        # Combines the static fixture (clown.json, agents, plugin
        # metadata) with a compiled mock-mcp-server binary. The plugin
        # host receives this store path as --plugin-dir.
        #
        # The source clown.json declares the mock server's command as
        # the relative path "bin/mock-mcp-server"; substituteInPlace
        # rewrites that to the absolute store path at build time so the
        # manifest is CWD-independent. --replace-fail errors if the
        # pattern is missing, catching drift in source clown.json edits.
        synthetic-plugin = pkgs.runCommand "synthetic-plugin" { } ''
          mkdir -p $out
          cp -r ${syntheticPluginSrc}/. $out/
          chmod -R u+w $out
          mkdir -p $out/bin
          cp ${mock-mcp-server-go}/bin/mockserver $out/bin/mock-mcp-server
          substituteInPlace $out/clown.json \
            --replace-fail 'bin/mock-mcp-server' "$out/bin/mock-mcp-server"
        '';
```

**Why `chmod -R u+w`?** Nix store paths are read-only. The `cp -r` from another store path would copy read-only permission bits, then the `mkdir $out/bin` would fail because `$out` itself was made read-only by the copy. The chmod opens it back up for the rest of the build.

**Why `cp -r ${syntheticPluginSrc}/.` (with the trailing `/.`)?** Without it, `cp -r ${path} $out/` would copy the whole directory *into* `$out/`, producing `$out/synthetic-plugin/clown.json`. The `/.` makes `cp` copy the contents instead of the directory itself.

**Step 4: Expose the derivation**

In the top-level `packages` attrset (where you added `packages.mock-stdio-mcp` in Task 2), add:

```nix
        packages.synthetic-plugin = synthetic-plugin;
```

**Step 5: Verify the derivation builds and has the expected layout**

Run: `nix build .#synthetic-plugin --no-link --print-out-paths`
Expected: a `/nix/store/...-synthetic-plugin` path.

Run:
```
out=$(nix build .#synthetic-plugin --no-link --print-out-paths)
ls "$out"
ls "$out/bin"
ls "$out/.claude-plugin"
ls "$out/.clown-plugin"
ls "$out/agents"
cat "$out/clown.json"
```

Expected:
- `$out/`: `clown.json`, `agents/`, `bin/`, `.claude-plugin/`, `.clown-plugin/`
- `$out/bin/`: `mock-mcp-server`
- `$out/.claude-plugin/`: `plugin.json`
- `$out/.clown-plugin/`: `system-prompt-append.d/`
- `$out/agents/`: `yaml-agent.md`, `toml-agent.md`
- `$out/clown.json`: the original manifest unchanged.

**Step 6: Confirm `substituteInPlace` rewrote the command to an absolute path**

Run:
```
out=$(nix build .#synthetic-plugin --no-link --print-out-paths)
jq -r '.httpServers["mock-mcp"].command' "$out/clown.json"
```
Expected: an absolute path that begins with `/nix/store/` and ends in `/bin/mock-mcp-server`, matching `$out/bin/mock-mcp-server`.

If the output is still `bin/mock-mcp-server`, the substitution didn't run — the derivation will have failed earlier with a `--replace-fail` error. Re-read `flake.nix` and confirm the `substituteInPlace` line landed in `synthetic-plugin`.

**Step 7: Commit**

```
git add flake.nix
git commit -m "feat(flake): add synthetic-plugin derivation

Lays out the test-plugin-host fixture as a single store path:
clown.json + agents/ + .claude-plugin/ + .clown-plugin/ from source,
plus bin/mock-mcp-server compiled from
internal/pluginhost/testdata/mockserver. The plugin host receives
\$out as --plugin-dir.

substituteInPlace rewrites clown.json's relative
'bin/mock-mcp-server' command to the absolute store path at build
time, matching the project's preference for CWD-independent
manifests. --replace-fail catches drift if the source clown.json
is ever edited."
```

---

## Task 4: Rewrite `test-stdio-bridge` recipe to consume the derivation

**Promotion criteria:** `build-mock-stdio-mcp` recipe can be deleted once this lands and passes.

**Files:**
- Modify: `justfile:36-37` (delete `build-mock-stdio-mcp`)
- Modify: `justfile:52-56` (rewrite `test-stdio-bridge` dep + variable)

**Step 1: Delete the `build-mock-stdio-mcp` recipe**

Remove lines 34-37 from `justfile`:
```
# Build the mock stdio MCP server used by clown-stdio-bridge integration tests.
[group("go")]
build-mock-stdio-mcp:
    go build -o tests/synthetic-plugin/bin/mock-stdio-mcp ./internal/pluginhost/testdata/mockstdiomcp
```

**Step 2: Update `test-stdio-bridge` dependencies and variable**

Change line 52 from:
```
test-stdio-bridge: build build-mock-stdio-mcp
```
to:
```
test-stdio-bridge: build
```

Change line 56 from:
```
    mock="$(pwd)/tests/synthetic-plugin/bin/mock-stdio-mcp"
```
to:
```
    mock=$(nix build .#mock-stdio-mcp --no-link --print-out-paths)/bin/mock-stdio-mcp
```

**Why two separate `nix build` calls instead of one with `-o`?** Each test recipe is self-contained: no symlink to clean up, no shared state. The store path is cached in Nix's eval after the first build, so subsequent builds are near-free.

**Step 3: Run the recipe**

Run: `just test-stdio-bridge`
Expected: ends with `OK: clown-stdio-bridge integration test passed`. No new files in the worktree.

**Step 4: Verify worktree stays clean**

Run: `git status`
Expected: no new untracked `tests/synthetic-plugin/bin/mock-stdio-mcp` entry.

**Step 5: Commit**

```
git add justfile
git commit -m "refactor(justfile): test-stdio-bridge consumes mock-stdio-mcp derivation

Drops the build-mock-stdio-mcp recipe (no longer needed — the
binary is now a Nix derivation) and points test-stdio-bridge at
the store path via 'nix build --no-link --print-out-paths'. No
more pollution of tests/synthetic-plugin/bin/ on test runs."
```

---

## Task 5: Rewrite `test-plugin-host` recipe to consume the derivation

**Promotion criteria:** `build-mock-server` recipe can be deleted once this lands and passes.

**Files:**
- Modify: `justfile:29-32` (delete `build-mock-server`)
- Modify: `justfile:151-154` (rewrite `test-plugin-host` dep + plugin_dir)

**Step 1: Delete the `build-mock-server` recipe**

Remove lines 29-32 from `justfile`:
```
# Build the mock MCP server used by integration tests
[group("go")]
build-mock-server:
    go build -o tests/synthetic-plugin/bin/mock-mcp-server ./internal/pluginhost/testdata/mockserver
```

**Step 2: Update `test-plugin-host` dependencies and `plugin_dir`**

Change line 151 from:
```
test-plugin-host: build build-mock-server
```
to:
```
test-plugin-host: build
```

Change line 154 from:
```
    plugin_dir="$(pwd)/tests/synthetic-plugin"
```
to:
```
    plugin_dir=$(nix build .#synthetic-plugin --no-link --print-out-paths)
```

**Step 3: Verify `inspect-compiled` reference is correct**

Confirm Task 1's edit landed: the `-- "$plugin_dir/bin/inspect-compiled"` is now `-- "$(pwd)/tests/scripts/inspect-compiled"`.

If it still reads `$plugin_dir/bin/inspect-compiled`, the test will fail because the store path doesn't contain `inspect-compiled` — that file moved to `tests/scripts/`.

**Step 4: Run the recipe**

Run: `just test-plugin-host`
Expected: ends with `OK: plugin-host integration test passed`. No new files in `tests/synthetic-plugin/bin/`.

**Step 5: Verify other plugin-using recipes still work**

`test-plugin-agents` and `explore-agents-schema` reference `$(pwd)/tests/synthetic-plugin` directly (not the derivation). They expect the *source* layout (which doesn't have `bin/mock-mcp-server`, since this Task 1 onwards deletes the empty source `bin/` directory).

Check by running:
```
just test-plugin-agents
```
Expected: PASS — this test only exercises the agents-listing path and doesn't need the mock binary.

**If `test-plugin-agents` fails because of a missing `bin/mock-mcp-server`** — surface that. The plugin host might be eagerly resolving the `httpServers` block even for unrelated commands. Possible fixes (decide with user):
1. Point `test-plugin-agents` at the derivation too.
2. Strip `httpServers` from the source `clown.json` and rely on the derivation having added it (would require Task 3 to re-add the block — more complex).
3. Accept that source-tree consumers need the binary, restoring the old recipe for them only (defeats the purpose).

**Step 6: Commit**

```
git add justfile
git commit -m "refactor(justfile): test-plugin-host consumes synthetic-plugin derivation

Drops build-mock-server and points test-plugin-host at the
synthetic-plugin store path via 'nix build --no-link
--print-out-paths'. The plugin host receives \$out as --plugin-dir;
clown.json's command field has been rewritten to the absolute
store path by substituteInPlace at build time. inspect-compiled
is now invoked from tests/scripts/inspect-compiled (moved in an
earlier commit) since it's a test driver, not a plugin component."
```

---

## Task 6: Confirm `tests/synthetic-plugin/bin/` is fully gone

**Promotion criteria:** N/A.

**Files:**
- Delete (if present): `tests/synthetic-plugin/bin/` (empty dir on disk).

**Step 1: Check the directory state**

Run: `ls -la tests/synthetic-plugin/bin/ 2>/dev/null && echo EXISTS || echo GONE`

If `EXISTS`: Run `rmdir tests/synthetic-plugin/bin` to remove the empty directory.
If `rmdir` complains it's not empty, list the contents — they're stale outputs from before this restructure landed. Run `rm tests/synthetic-plugin/bin/*` then retry `rmdir`.

If `GONE`: nothing to do.

**Step 2: Confirm the source tree no longer references `tests/synthetic-plugin/bin`**

Run: `rg 'tests/synthetic-plugin/bin'`
Expected: zero matches across the repo (if `flake.nix`'s `syntheticPluginSrc` fileset omits `bin/`, which it does per Task 3).

If matches remain, audit each one and decide whether the path needs updating.

**Step 3: No commit needed** — Git doesn't track empty directories. The restructure is implicit.

---

## Task 7: Final verification

**Promotion criteria:** N/A — this is the smoke test.

**Files:** none modified.

**Step 1: Clean stale binaries from prior `just` runs**

Run:
```
git clean -fd
```
Expected: removes `circus`, `clown`, `bin/circus`, and any leftover `tests/synthetic-plugin/bin/mock-*` from the worktree. (Don't use `-x` — that would also remove `.tmp/` and `result*` symlinks, which is more cleanup than needed.)

**Step 2: Run the default recipe**

Run: `just`
Expected: build, test, check all pass. No `tests/synthetic-plugin/bin/mock-*` files appear.

**Step 3: Confirm the worktree stays clean**

Run: `git status`
Expected: `nothing to commit, working tree clean`. The only untracked entries that should remain are pre-existing build artifacts not produced by `just` (e.g. `bin/circus` if it was reintroduced — but that's a separate cleanup discussion).

**Step 4: If `git status` shows `tests/synthetic-plugin/bin/mock-*`**

Something didn't take. Most likely cause: a justfile recipe still does `go build -o tests/synthetic-plugin/bin/...`. Run `rg -- '-o tests/synthetic-plugin/bin'` to find it.

**Step 5: If everything is clean, no commit is needed** — the restructure is complete. The next merge-this-session will roll up Tasks 1–6.

---

## Open questions / known limitations

1. **`test-plugin-agents` and `explore-agents-schema`** still read from `$(pwd)/tests/synthetic-plugin` (the source tree). If the plugin host fails when `bin/mock-mcp-server` is absent — even for non-MCP commands — Task 5 will surface it and we'll need a follow-up decision (see Task 5 step 5).

2. **`build-mock-server` removal:** confirm no CI workflow or external script invokes `just build-mock-server` directly before deleting the recipe. If you can't be sure, leave the recipe as a deprecation shim that just calls `nix build .#synthetic-plugin --no-link` and prints a notice.

3. **GC pressure:** because tests use `--no-link --print-out-paths`, the synthetic-plugin and mock-stdio-mcp store paths have no GC root. Running `nix-collect-garbage` between test runs forces a rebuild. If this becomes annoying in practice, add a debug recipe that drops `result-synthetic-plugin` / `result-mock-stdio-mcp` symlinks.

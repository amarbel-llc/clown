# Held patch: clown's floating nixpkgs copies and the pinned base (shithub, operator ruling Q3)

Status: held, 2026-11-03. The patch is
`2026-11-03-shithub-follows.patch` beside this note; it is **not** applied
in any committed tree, and the lock change is the operator's this week.
Priority low; written while idle on V3.

## What the ruling asks

Keep nixpkgs rev-pinned and make floating copies follow the pin, to cut
the api.github.com lookups a `nix flake update` makes (GitHub's API limit
halted circus's update cascade twice on 2026-11-03). For clown: numtide
llm-agents.nix's nixpkgs (it floats on `nixpkgs-unstable`) and the
`nixpkgs-*` channel inputs follow the rev-pinned nixpkgs, unless one is
deliberately different, in which case name it and why.

## Where clown's inputs stand (read from flake.nix and flake.lock)

clown has no direct `nixpkgs` input. Its base is `igloo` (a forge tarball,
rev-pinned by the lock's narHash; imported as nixpkgs:
`pkgs = import igloo {...}`). The github-typed inputs:

| Input | Original | Floats? | Decision |
|---|---|---|---|
| `llm-agents` | `github:numtide/llm-agents.nix` (branch) | yes, the input itself | left as is: the ruling names its *nixpkgs*; pinning llm-agents to a rev is a separate choice the operator can make the same way |
| `llm-agents/nixpkgs` | `github:NixOS/nixpkgs/nixpkgs-unstable` (locked at f20cd90e) | **yes** | **stays separate: the named exception.** Evidence, not theory: an accidental evaluation of the devshell with `llm-agents.inputs.nixpkgs.follows = "igloo"` in place (a recipe runs under `nix develop`, which rewrote the worktree's lock; both files were restored from HEAD, nothing committed) failed with `callPackageWith: Function called without required argument "zig_0_17"` from llm-agents' `packages/chock/package.nix`: igloo's base has zig 0.12 to 0.14. The devshell reaches that package, so the follows breaks clown's own evaluation, not only an unused package. The patch records this in the input's comment. |
| `nixpkgs-claude-code` | `github:NixOS/nixpkgs/b2b9662f` (rev) | no | **removed by the patch**: unused since claude-code comes from llm-agents (the `pkgs-claude-code` binding and the outputs argument go with it, as its own comment asked) |
| `nixpkgs-codex` | `github:NixOS/nixpkgs/0de8465d` (rev) | no | stays separate: codex exists as a `pkgs/by-name` definition only at this pre-migration SHA (AGENTS.md, "Nix conventions"); the thin-overlay base has no codex |
| `nixpkgs-llama` | `github:NixOS/nixpkgs/c0df0d08` (rev) | no | stays separate: llama-cpp with the Anthropic Messages API (upstream PR #17570) exists only at this SHA; the base's build predates it |
| `nixpkgs-master` | `github:NixOS/nixpkgs/b4fd65b1` (rev) | no | stays separate: it sources the devshell's `just`, held at a pre-1.50.0 SHA while the cargo-vendor failure on `windows-sys-0.60.2.tar.gz` is open upstream; every forge input already follows it |

So the patch's only lock-affecting change is the removal of one rev-pinned
github input; no floating copy in clown can follow the pin today. The
`nixpkgs-*` inputs that stay are rev-pinned `github:` originals. My
understanding of nix's github fetcher is that a rev-pinned input is not
re-resolved against a branch on `nix flake update`, so they do not cost the
lookup the ruling targets; whether an update still touches the API for them
(for the commit's `lastModified`, say) is something the operator's next
update log will show. Removing that cost would mean dropping those inputs,
and with them codex, the Anthropic-API llama-cpp and the held `just`: a
separate decision.

The remaining floating lookups in clown's update are therefore
`llm-agents` itself (a branch) and its `nixpkgs` (a branch). The way to
remove both without the follows is to pin llm-agents to a rev
(`github:numtide/llm-agents.nix/<rev>`); its locked nixpkgs then stays at
whatever that rev's lock holds until the pin moves. That is a one-line
change the operator can make at the lock change if they want it; the patch
does not presume it.

## The lock change (operator)

After `git apply docs/plans/2026-11-03-shithub-follows.patch` from the
checkout root:

1. `nix flake lock`. Expected lock diff: the node `nixpkgs-claude-code`
   disappears; nothing else moves. A diff that moves `igloo`,
   `nixpkgs-codex`, `nixpkgs-llama`, `nixpkgs-master` or llm-agents' nodes
   to a new rev was not intended by this patch.
2. `just build` (the gate); nothing in the build used the removed input.
3. Commit flake.nix and flake.lock together.

Checked here: `git apply --check` of the patch against master
(`just explore debug-check-held-patch docs/plans/2026-11-03-shithub-follows.patch`).

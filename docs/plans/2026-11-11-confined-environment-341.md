# clown#341: the unit's environment, what clown changes

Status: plan, 2026-11-11. Input: clown#341 (spinclass half: spinclass#462).

## Finding

The issue's ask 1 is mostly already how `agentscope.Launch.environment()`
works (`internal/*/agentscope/unit.go`): systemd-run forwards nothing, and the
unit gets only the `passKeys`/`passPrefixes` allowlist from the frontend, the
provider binding's `ExtraEnv`, the spec's `Env` and clown's own set. The 76
direnv variables of the measurement are mostly dropped by that allowlist. What
still passes from the launcher, by design (FDR 0020 "Environment"):

- `PATH` verbatim (ask 2: this is the launcher's devShell/login PATH);
- the whole `NIX_` prefix (the devShell's toolchain wiring);
- `TROUPE_`, `CLAUDE_CODE_`, `ANTHROPIC_` prefixes.

## What clown changes

1. **Log-only diff (ask 3). Needs no ruling; landed with this note.**
   `Launch.EnvironmentReport()` and a stderr line at launch: names only, split
   into inherited-not-passed / passed / stated, plus PATH entry counts. Run a
   few spawns across profiles, then confirm the list in the issue.
2. **PATH stated by clown (ask 2). Needs an operator ruling**, because it
   changes what a confined agent can execute. Open questions: which entries
   (clown, ringmaster/troupe, spinclass `sc`, git, `just`, ... from pins or the
   system profile); whether the devShell's toolchain stays reachable for
   `run_recipe` (the paved path would break without `just` and the Go tools).
3. **Drop the `NIX_` blanket prefix to a named set.** Same ruling as 2; it is
   the devShell wiring that makes the toolchain resolve.
4. **Host baseline (ask 4, open for clown).** Recommendation: keep the user
   manager's environment as the baseline (it is what any unit gets), and keep
   unsetting what clown does not list (`ManagerEnvKeys`). No change needed.

## Not clown's

The spec `env` field being the whole of the launcher's contribution is
spinclass#462.

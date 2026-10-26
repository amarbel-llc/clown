# D0 spike: dontAsk through --settings (for later)

Status: not run. Feeds FDR 0023 (clown#323). Needs a real claude and a PTY;
nothing here is automated.

## Questions

1. Does `--settings '{"permissions":{"defaultMode":"dontAsk"}}'` put an
   interactive session in `dontAsk`, the same as `--permission-mode
   dontAsk`? (V today only for `--permission-mode dontAsk` under `-p`.)
2. With both given (spinclass's splice and clown's setting), is the result
   still `dontAsk`, with no warning?
3. Does a user `~/.claude/settings.json` `defaultMode` lose to `--settings`?
   Does a managed one win? (R: the vendored man/man5/claude-code-settings.5,
   lines 41-50, says CLI settings override all file-based settings and
   managed settings take highest precedence; dated April 2026, not checked
   against the installed claude.)
4. Under `dontAsk`: is a PreToolUse hook `allow` still honoured, and is an
   `allow` list entry still allowed without a dialog?
5. Under `dontAsk`: what happens to `AskUserQuestion` (answered, denied,
   or the session waits)? With `CLAUDE_AFK_TIMEOUT_MS` at clown's value.
6. Which hook events fire for a denied call: `PreToolUse` only, or also
   `PermissionDenied`/`PostToolUseFailure`? (None of the three permission
   counters can count it: they start at `PermissionRequest`.)
7. Can the TUI switch out of `dontAsk` (shift+tab), and does the footer
   show the mode?

## Method

Use the recipe in docs/plans/2026-10-25-permission-hook-events.md with a
throwaway HOME and a recording hook on every event, once with
`--permission-mode dontAsk`, once with only the settings key, once with
both. Ask the model to run a disallowed Bash call, an allow-listed read,
and an AskUserQuestion. Record the event sequence and the visible outcome
for each.

## Result

Not run yet.

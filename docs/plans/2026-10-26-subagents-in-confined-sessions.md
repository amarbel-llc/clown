# Subagents in confined and unattended sessions (clown#331), 2026-10-26

Labels: **R** read in code, **V** observed (this session's own tool lists, tests),
**I** inferred, **U** not checked. Operator direction (relayed by the
coordinator): native subagents are to be replaced by roustabout graphs or
juggler subagents (roustabout#7), so no generated tool-list mechanism and no
subagent registry; make confined/unattended sessions safe now.

## What fails and why

- **R** The only subagent definition is `subagents/discover.md`. The
  `clown-hook-allow` PreToolUse hook rewrites every Agent launch with
  `subagent_type "Explore"` to `Discover` (`cmd/clown-hook-allow`), so the
  model's ordinary exploration helper is Discover.
- **R** Discover's `tools` was 30 names of the form `mcp__plugin_moxy_moxy__<tool>`.
  **V** against moxy's current tool list (this session): `folio_glob`,
  `folio_read`, `folio_ls`, `rg_search`, `man_list`, `man_toc`, `man_section`
  exist; `folio_read-range`, `folio_read-excluding`, `man_search`,
  `man_semantic-search`, `grit_diff`, `grit_git-rev-parse` and the whole
  `get-hubbed_*` / `get-hubbed-external_*` family do not (7 of the 30 are real).
  The prompt body also told the subagent to call `get-hubbed api-get` first and
  to use `man_search`, so it directed the model at tools it never had.
- **R (from the refusal text in the issue)** claude refuses to spawn a subagent
  whose tools resolve to nothing; one that resolves some names is spawned with
  those. So the stale names were harmless in an attended session (7 resolve)
  and fatal wherever none do.
- **R** Where none do: a template that drops moxy whole. `worker` and
  `canary-spinclass` keep `clown-builtin-jobs`, `spinclass` and `just-us`
  only, so a subagent there has no search tool at all, whatever Discover's list
  says. Templates that keep moxy (`reviewer`, `porter`, `canary`, `canary-grit`)
  keep at least `folio_read`, `folio_ls` and `rg_search`
  (`builtin_templates_test.go`), so Discover resolves there.
- **I** The in-unit moxy is declared as server `moxy/moxy`
  (`agentscope.InUnitServer`), the same plugin and server name as outside, so
  its tools should carry the same `mcp__plugin_moxy_moxy__` prefix. The issue
  guessed a different prefix; I found no code that renames it. **U** not seen in
  a live unit. Which profile the reporting nikulin session used is not stated;
  if it was `worker` or `canary-spinclass`, moxy is absent and the refusal is
  expected regardless of prefix.

## Which would fail

| Session | Discover before | Discover after this change |
|---|---|---|
| attended, full moxy | works with 7 real tools, stale prompt | works, tools and prompt are real |
| confined, template keeps moxy (canary, canary-grit) | works if the in-unit prefix is the same (I) | same |
| confined or unattended, moxy dropped (worker, canary-spinclass) | refused | Agent tool is disallowed, so the model is not offered it |

## The change

1. `discover.md`: tools reduced to names moxy serves (`folio_glob`, `folio_read`,
   `folio_ls`, `rg_search`, `man_list`, `man_toc`, `man_section`, `grit_stage`,
   `grit_branch`) and the prompt stops naming the missing ones. Small cleanup,
   not a mechanism; a test pins the names to a list of moxy's current tools and
   the portable core. Droppable if the operator prefers to leave Discover alone.
2. `worker` and `canary-spinclass` add `Agent` to `CLOWN_DISALLOWED_TOOLS`
   (the two templates with no moxy). A test pins "Agent is disallowed exactly
   when a builtin template drops moxy". This is the one-line disallow.

## Answers to the coordinator's questions

- **Is the one-line disallow enough?** For the two moxy-less templates, yes:
  the Agent tool is not offered, so nothing spawns and nothing refuses. It does
  not help a profile that keeps moxy but loses it inside a unit (none today), nor
  a user profile that copies the template without the line.
- **What is the model told when Agent is absent? (I, U)** `CLOWN_DISALLOWED_TOOLS`
  becomes claude's `--disallowed-tools`, which removes the tool from the list the
  model is given; I found nothing in clown's system-prompt fragments that mentions
  the Agent tool or subagents, so there is no text that now points at a missing
  tool. I did not observe a session without Agent.
- **Digest cost (R).** The env value is digested per value (`envDigests`), so
  adding `Agent` changes the digest of `worker` and `canary-spinclass` only. A
  session pinned to the old digest is refused on `clown resume` and by a
  restart's dry-run profile gate until it is re-pinned (launcher approval). Any
  grant that names the old digest stops matching. The registry protocol is
  unchanged. **U** whether any session pinned to those two digests exists:
  `worker` was added today and nothing is deployed from this branch as far as I
  know.

## Sketch: a profile `capabilities` list (not implemented; operator rules first)

A closed set (`subagents`, `monitor` [clown#330], later others) that confined
and unattended templates leave empty and `worker` states outright.

- **Fits beside** `context_servers` / `context_excluded`: it is another
  allow-list a profile carries. Two shapes:
  1. **Sugar over existing fields**: `capabilities = ["monitor"]` expands at
     load time into the disallowed-tools and server selections that implement it.
     The digest is then the digest of the expansion, so the canonical form and
     the digest of every existing profile stay as they are, and no
     `ProtocolVersion` bump is needed for digests. A registry written with the new
     key must still be refused by older readers (`pkg/profilelookup` fails
     closed on unknown keys), which needs the registry stamp to say so; today
     `ProtocolVersion` is one constant used both for the stamp and inside the
     canonical JSON (`canonical.Protocol`), so stamping a higher registry protocol
     means bumping it, which changes every digest (below). Splitting it into a
     registry-schema number and a canonical-form number is a library-spec change
     to announce to spinclass first (**I**).
  2. **A new canonical field**: honest and explicit, but the package doc says a
     new field means bumping `ProtocolVersion`, and `Protocol` is inside the
     canonical JSON, so every profile's digest changes, every pinned session
     is refused on resume and restart, and every approval recorded against an old
     digest is void. Do not do this unannounced or in the same week as a
     restart-dependent unattended run.
- **Recommendation (I):** do nothing beyond the Agent disallow now; if the
  operator wants capabilities, take shape 1 and split the two protocol numbers
  in one announced change.

## Not verified

- A live unit's tool names and the model's view of a session without Agent.
- Whether claude accepts a partially resolvable tools list in 2.1.310 as the
  issue's refusal text implies (R from the text, not reproduced).

---
status: proposed
date: 2026-11-10
---

# The capability-profile ruleset: what a profile may write

## Abstract

ajour (cutting-garden FDR 0028) evaluates whatever a stencil says. What a
capability profile is ALLOWED to say is a separate, named ruleset, owned by
clown (ruled 2026-11-10: its own phase-3 stage). This RFC states every rule
with an id, a severity, the exact message, and at least one vector
(document in, verdict out). The rules read the profile as RFC 0018's
vocabulary defines it: a rule that names a node type or a field is RFC
0018's type or field.

The vectors are also data: `internal/*/profilerules/vectors.json`, whose
test checks that every rule here has a vector and every vector names a rule
here. The validator that runs them is not built yet (it needs
`pkgs/ajour`'s `ParseStencil`; see §6).

Tracks clown#348. Companion: RFC 0018 (the catalog contract).

## 1. Where the rules run

One library function, two callers:

    Validate(document string) []Finding     // Finding{Rule, Severity, Node, Message}

- **The bless path** (whatever blesses a profile: spinclass FDR 0034's
  bless, the card prompt). A finding of severity **refuse** blocks the
  bless; a **warn** finding is shown at the bless prompt beside the
  node's matched set (D2 §1, E6.3) and does not block.
- **A lint verb**, `clown capability-profile check <file>`, beside
  `show-effective-profile` (D2 open item 5): prints every finding, exits 1
  on a refusal. Advisory: it is what a writer runs while authoring.

The rules are not in ajour: ajour evaluates any parsed stencil and has no
catalog knowledge (FDR 0028 "What is deliberately NOT in it"), and a
profile that passes ajour's parser may still be one no one should bless.
Cascade rules (a child adds no node, a `_base` cycle; D2 V-D2-3, V-D2-5,
V-D2-10) need two documents and are the cascade library's, not these.

A **patch** (an act body) is checked by `ajour.ParsePatch` (every leaf
carries `deny`) and by the rules marked "patches too" below; the `open`
rule (R-4) exempts patches.

## 2. Body rules

Severity: **refuse** blocks the bless; **warn** does not.

| Id | Rule | Severity | Applies to | Message |
|---|---|---|---|---|
| R-1 | the body parses as the stencil fragment (`ajour.ParseStencil`) | refuse | stencils, patches | `R-1: <ajour's parse error>` |
| R-2 | a `_` node carries `!type` | refuse | stencils, patches | `R-2: [_ …] has no !type: the null id matches by type and atoms, so say which type (e.g. [-> _ !tool-input-v1 open])` |
| R-3 | a `+` node carries `!type` | refuse | stencils, patches | `R-3: [+ …] has no !type: a creation allowance names the type it creates` |
| R-4 | an atom-less typed `_` node carries the tag `open`, on every level, interior and deny-chain nodes included (R 2026-11-10). A `perm` atom does not count as an atom: it matches nothing, so a `_` whose only atom is `perm` is still a wildcard (FDR 0028) | refuse | stencils | `R-4: [_ !<type>] has no atoms and no open tag: write [_ !<type> open] to admit every <type>, or add atoms` |
| R-5 | a `_` node on the plugin or server level appears only on a chain whose every leaf carries `deny` (a cross-cutting deny root) | refuse | stencils | `R-5: [_ !<clown-plugin-v1|mcp-server-v1>] admits through every <plugin|server>: name it, or end every branch beneath it in deny` |
| R-6 | no atom names the verb `create` (`op=create`, `op=[…, create]`, `^op=create`) | refuse | stencils, patches | `R-6: op=create at <node>: creation is spelled by a + node ([-> + !<type> …]), never by op=create` |
| R-7 | every atom names a field the node's type carries (RFC 0018 §1, §3); a plugin's own cg type is not checked. So no `op=` on a catalog node or an `fs-path-v1` node (RFC 0018 §6: the tool is the verb). `perm` is exempt: it is a decision, not a field, and may sit on any node (R-14) | refuse | stencils, patches | `R-7: <field> is not a field of <type> (it carries: <fields>); an atom on a missing field never holds` |
| R-8 | an `op=` atom's values other than `create` (R-6's) are among `read`, `patch`, `put`, `delete` | refuse | stencils, patches | `R-8: op=<v> at <node>: verbs are read, patch, put, delete` |
| R-9 | no `tool-arg-v1` node (RFC 0018 §9 item 1, agreed 2026-11-10) | refuse | stencils, patches | `R-9: tool-arg-v1 is retired: write [-> _ !tool-input-v1 field=<arg> value=[…]] beside [-> _ !tool-input-v1 ^field=<arg> open]` |
| R-10 | no `cg-roots-v1` or `cg-node-v1` node (retired 2026-11-09) | refuse | stencils, patches | `R-10: <type> is retired: hang cg roots under [-> resources !mcp-resources-v1] and give each step the plugin's own type (or none)` |
| R-11 | a locked id's digest is a markl id that parses, purpose `blake2b256`, blech32 checksum valid | refuse | stencils, patches | `R-11: <id>@<digest> is not a valid markl id (<reason>): copy the lock from the catalog listing` |
| R-12 | a resource-step node (`mcp-resources-v1`, `mcp-resource-v1`, `cg-root-v1`, a plugin's cg type) that admits a subject by itself (a leaf of its allowance, or a node carrying `subtree`) and is not `deny` carries an `op=` atom. A pass-through container step, whose only job is to lead to a child with its own `op`, may omit it; `+` and `cg-field-v1` nodes are exempt | refuse | stencils | `R-12: <node> admits every verb: say which, e.g. op=[read]` |
| R-13 | a `cg-field-v1` node's parent carries `op=` including `patch` or `put` | refuse | stencils | `R-13: cg-field-v1 under <parent> is unreachable unless <parent> admits op=patch or op=put` |
| R-14 | a `perm` atom is a decision, not a pin (cutting-garden FDR 0028 "Facts are pins, perm is a decision", 2026-11-10): at most one per node, `perm=` with one value of `allow`, `ask` or `dynamic`; never negated, never `perm^=` or another operator, never a list, never beside `deny`, and none in a patch (asking from a patch awaits an operator ruling). ajour's parser refuses the same forms (R-1); R-14 names them with its wording | refuse | stencils, patches | `R-14: atom `<atom>`: <why>`, where `<why>` is ajour's: `a patch may only deny; perm decides, so it belongs in a stencil`, `a node carries one perm atom`, `perm is a decision, not a condition, so it cannot be negated`, `perm takes = only`, `perm takes one value`, `perm is allow, ask or dynamic`, `a node that carries deny decides nothing else` |
| W-1 | a stencil admits `harness/Bash` or `moxy/toolbox/run` with `command` open or unconstrained | warn | stencils | `W-1: <tool> admitted with an open command: the command line names files and hosts ajour cannot scope (RFC 0018 §7)` |
| W-2 | a `ReadMcpResourceTool` or `ListMcpResourcesTool` node under `clown-builtin-harness/harness` | warn | stencils | `W-2: <tool> presents no tool step; this node is inert. Allow the resource under its owner's surface (RFC 0018 §7)` |
| W-4 | `subtree` on a catalog node | warn | stencils, patches | `W-4: subtree on <type> is inert: catalog ids are not paths` |
| W-5 | an `mcp-tool-v1` id in Claude Code's mangled spelling: one starting `mcp__`, or, under plugin `moxy`, `<s>_<t>` where `<s>` is a moxy server `catalog.json` names (RFC 0018 §2) | warn | stencils, patches | `W-5: <id> is Claude Code's spelling of moxy's <s>.<t>; it matches nothing: write [-> <s> !mcp-server-v1 [-> <t> !mcp-tool-v1 …]]` |
| W-6 | a `kind=` atom on an `mcp-tool-v1` node under plugin `moxy` (clown#356: no moxy tool presents a kind until moxy publishes one, so the atom never holds and every call through the node is refused) | warn | stencils | `W-6: kind=<v> at <node> never holds for a moxy tool (none presents a kind, clown#356): name the tool, e.g. [-> read !mcp-tool-v1 …]` |

Notes on the choices the brief left open:

- **R-5, `_` on catalog levels.** `_` on the tool level is legal with or
  without atoms: `[-> _ !mcp-tool-v1 kind=read …]` ("every read tool of
  this server") was written by three probe writers unprompted, and `[-> _
  !mcp-tool-v1 open …]` is "every tool of this server". `_` on the plugin
  or server level would admit tools of plugins the writer never looked at,
  so it is legal only as the cross-cutting deny root FDR 0028's complete
  document uses. Recommended; needs eager-holly's agreement for D2.
- **Catalog predicates.** A `_` node with atoms on a catalog level
  (`[-> _ !just-recipe-v1 group=[test, lint]]`, `[-> _ !mcp-tool-v1
  kind=read]`) is legal when every atom names a field the type carries
  (R-7). This replaces D2 V-D2-7's "catalog nodes are ground, `group=` the
  one exception": the exception generalizes to "the type's declared
  fields", and `[-> _ !mcp-tool-v1 name^=job_]` stays invalid because
  `name` is not a field (R-7), which is what V-D2-7 wanted.
- **R-12, the op-less default.** FDR 0028 lets an op-less resource
  allowance admit every verb. Recommended: refuse it, because the verb is
  the one thing a resource allowance exists to scope, and an omitted verb
  is far more often a slip than an intent; `op=[read, patch, put,
  delete]` spells "everything" when meant.

## 3. Head rules (hyphence metadata)

circus D2 §2 owns the head's semantics; these are its rules as the
validator enforces them. The operator ruled H-6 and H-7 as written
(2026-11-10, via circus/eager-holly, recorded in D2). The rest are to be
agreed with circus/eager-holly before the validator ships.

| Id | Rule | Severity | Message |
|---|---|---|---|
| H-1 | the type line is `! espalier-profile-v1` | refuse | `H-1: the type line is <x>; a capability profile is ! espalier-profile-v1` |
| H-2 | `role`, `harness` and `mode` are present, once each | refuse | `H-2: <key> is required` / `H-2: <key> appears twice` |
| H-3 | `mode` is `attended` or `unattended` | refuse | `H-3: mode=<v>: attended or unattended` |
| H-4 | `harness` is `claude-code` when attended, `juggler-run` when unattended | refuse | `H-4: harness=<v> with mode=<m>: <m> profiles run <harness>` |
| H-5 | `provider` and `spend_cap_usd` are absent when attended, present when unattended; `spend_cap_usd` is a positive decimal | refuse | `H-5: <key> is <forbidden|required> in <mode> profiles` |
| H-6 | `model` is present when unattended | refuse | `H-6: model is required in unattended profiles (it is what the spend cap buys)` |
| H-7 | `_base` is absent or one `@<markl id>` (R-11's check); required when unattended; a root profile omits it | refuse | `H-7: _base …` |
| H-8 | `role` matches `(manager|engineer)-<leaf>`, the leaf lowercase letters, digits and hyphens | refuse | `H-8: role=<v>: manager-<leaf> or engineer-<leaf>` |
| H-9 | no key outside `role`, `harness`, `mode`, `model`, `provider`, `spend_cap_usd`, `_base` | refuse | `H-9: unknown head key <k>` |

## 4. Mode rules (body under the head's `mode`; D2 §5)

| Id | Rule | Severity | Message |
|---|---|---|---|
| M-2 | unattended: no `troupe/chat_send` and no `spinclass` server node that is not `deny` | refuse | `M-2: <node> in an unattended profile: a node reports through its job` |
| M-3 | attended: `clown-builtin-jobs/ringmaster` and `clown-builtin-jobs/troupe` are present and tagged `required` | refuse | `M-3: attended profiles require <server> tagged required (the hello)` |

M-1 ("unattended: no `perm=ask` atom") is retired (2026-11-10, cutting-garden
FDR 0028 "Facts are pins, perm is a decision"). `perm=ask` is allowed in any
profile, and the enforcer resolves it by its posture: attended prompts;
unattended escalates when a channel exists, else refuses. The head's `mode`
is a declaration, not that posture. RFC 0018 §11 has the contract: clown's
hook is the one escalator, and posture reaches moxy as `MOXY_POSTURE`. D2 §5's
text follows (circus/eager-holly).

## 5. Vectors

Bodies are written with their head omitted unless the row is about the
head or the mode; a head-less body is checked alone, and the H-* and M-*
rules do not run on it. `ok` is no finding; `refuse R-n` and `warn W-n`
name the finding. The `…` elisions below are abbreviations;
`vectors.json` spells every document whole.

| Id | Document | Verdict |
|---|---|---|
| PR-1 | `[moxy !clown-plugin-v1 [-> folio !mcp-server-v1 [-> read !mcp-tool-v1 [-> file:/wt !fs-path-v1 subtree] [-> _ !tool-input-v1 open]]]]` | ok |
| PR-2 | `[moxy [-> folio [-> read [-> file:/wt !fs-path-v1 subtree` (unbalanced) | refuse R-1 |
| PR-3 | `… [-> read !mcp-tool-v1 [-> _ open]]` | refuse R-2 |
| PR-4 | `… [-> cal-7 !caldav-calendar-v1 op=[read] [-> + summary^=x]]` | refuse R-3 |
| PR-5 | `… [-> read !mcp-tool-v1 [-> _ !tool-input-v1]]` | refuse R-4 |
| PR-6 | `[_ !clown-plugin-v1 [-> _ !mcp-server-v1 [-> _ !mcp-tool-v1 [-> file:/wt/secrets !fs-path-v1 subtree deny]]]]` (no `open`, the cross-cutting deny root as FDR 0028 first wrote it) | refuse R-4 (three nodes) |
| PR-7 | PR-6 with `open` on the three `_` nodes | ok |
| PR-8 | a patch `[moxy [-> folio [-> _ !mcp-tool-v1 [-> file:/wt/x !fs-path-v1 subtree deny]]]]` | ok (R-4 exempts patches) |
| PR-9 | `[_ !clown-plugin-v1 open [-> _ !mcp-server-v1 open [-> _ !mcp-tool-v1 open]]]` | refuse R-5 (plugin), R-5 (server) |
| PR-10 | `[moxy !clown-plugin-v1 [-> folio !mcp-server-v1 [-> _ !mcp-tool-v1 kind=read [-> file:/wt !fs-path-v1 subtree]]]]` | ok (a `_` tool with a declared atom) |
| PR-11 | `[just-us !clown-plugin-v1 [-> just-us !mcp-server-v1 [-> run_recipe !mcp-tool-v1 [-> _ !just-recipe-v1 group=[test, lint]]]]]` | ok |
| PR-12 | `… [-> _ !mcp-tool-v1 name^=job_]` | refuse R-7 |
| PR-13 | `… [-> resources !mcp-resources-v1 [-> ynab:main !cg-root-v1 subtree op=[read, create]]]` | refuse R-6 |
| PR-14 | a patch `… [-> cal-7 !caldav-calendar-v1 op=create deny]` | refuse R-6 |
| PR-15 | `… [-> read !mcp-tool-v1 [-> file:/wt !fs-path-v1 subtree op=[read]]]` | refuse R-7 |
| PR-16 | `… [-> ynab:main !cg-root-v1 subtree op=[list]]` | refuse R-8 |
| PR-17 | `… [-> grit_stage !mcp-tool-v1 [-> status !tool-arg-v1 arg=subcommand]]` | refuse R-9 |
| PR-18 | `… [-> ynab:main !cg-node-v1]` | refuse R-10 |
| PR-19 | `… [-> run_recipe@blake2b256-s1 !mcp-tool-v1]` | refuse R-11 |
| PR-20 | `… [-> run_recipe@<a valid blake2b256 markl id> !mcp-tool-v1]` | ok |
| PR-21 | `… [-> resources !mcp-resources-v1 [-> ynab:main !cg-root-v1 subtree]]` | refuse R-12 |
| PR-22 | `… [-> resources !mcp-resources-v1 [-> ynab:main !cg-root-v1 subtree op=[read]]]` | ok |
| PR-23 | `… [-> resources !mcp-resources-v1 [-> caldav:fastmail !cg-root-v1 [-> cal-7 !caldav-calendar-v1 [-> + !caldav-object-vtodo-v1 summary^="[auto]"]]]]` | ok (interior steps need no `op=`; `+` exempt) |
| PR-24 | `… [-> budget/b1 !cutting_garden-ynab-budget-v1 op=[read] [-> _ !cg-field-v1 field=[memo]]]` | refuse R-13 |
| PR-25 | `… [-> _ !cutting_garden-ynab-transaction-v1 op=[patch] [-> _ !cg-field-v1 field=[category, memo]]]` | ok |
| PR-26 | `[clown-builtin-harness !clown-plugin-v1 [-> harness !mcp-server-v1 [-> Bash !mcp-tool-v1 [-> _ !tool-input-v1 open]]]]` | warn W-1 |
| PR-31 | `[moxy !clown-plugin-v1 [-> toolbox !mcp-server-v1 [-> run !mcp-tool-v1 [-> _ !tool-input-v1 open]]]]` | warn W-1 |
| PR-27 | `… [-> Bash !mcp-tool-v1 [-> command !tool-input-v1 value^="just "]]` | ok |
| PR-28 | `[clown-builtin-harness … [-> ReadMcpResourceTool !mcp-tool-v1 [-> _ !tool-input-v1 open]]]` | warn W-2 |
| PR-29 | `… [-> run_recipe !mcp-tool-v1 [-> _ !just-recipe-v1 group=[test]] [-> _ !tool-input-v1 open]]` | ok (the recipe is a value path; its sibling input node is reached, RFC 0018 §1) |
| PR-32 | `… [-> ynab:main !cg-root-v1 [-> budget/b1 !cutting_garden-ynab-budget-v1 [-> transactions [-> _ !cutting_garden-ynab-transaction-v1 op=[patch] [-> _ !cg-field-v1 field=[memo]]]]]]` | ok (op-less pass-through steps) |
| PR-33 | `… [-> resources !mcp-resources-v1 [-> madder://blobs !mcp-resource-v1]]` | refuse R-12 (op-less leaf) |
| PR-30 | `… [-> folio !mcp-server-v1 subtree …]` | warn W-4 |
| PR-34 | `[moxy !clown-plugin-v1 [-> moxy !mcp-server-v1 [-> folio_read !mcp-tool-v1 …]]]` | warn W-5 |
| PR-35 | `… [-> folio !mcp-server-v1 [-> mcp__plugin_moxy_moxy__folio_read !mcp-tool-v1 …]]` | warn W-5 |
| PR-36 | `[spinclass !clown-plugin-v1 [-> spinclass !mcp-server-v1 [-> grit_commit !mcp-tool-v1 …]]]` | ok (spinclass's own tool name; W-5 reads moxy only) |
| PR-37 | `[moxy !clown-plugin-v1 [-> folio !mcp-server-v1 [-> _ !mcp-tool-v1 kind=read …]]]` | warn W-6 (FDR 0028's engineer example; the first ajour dry run refused every read through it) |
| PR-38 | `[clown-builtin-harness !clown-plugin-v1 [-> harness !mcp-server-v1 [-> _ !mcp-tool-v1 kind=read …]]]` | ok (the built-ins carry their catalog kind) |
| PR-39 | `… [-> read !mcp-tool-v1 ^perm=allow …]` | refuse R-14 (never negated) |
| PR-40 | `… [-> read !mcp-tool-v1 perm=[allow, ask] …]` | refuse R-14 (one value) |
| PR-41 | `… [-> read !mcp-tool-v1 perm^=a …]` | refuse R-14 (`=` only) |
| PR-42 | `… [-> read !mcp-tool-v1 perm=allow perm=ask …]` | refuse R-14 (one per node) |
| PR-43 | `… [-> file:/wt/secrets !fs-path-v1 subtree perm=ask deny]` | refuse R-14 (never beside deny) |
| PR-44 | `… [-> read !mcp-tool-v1 perm=never …]` | refuse R-14 (allow, ask or dynamic) |
| PR-45 | a patch `… [-> read !mcp-tool-v1 perm=ask [-> file:/wt/x !fs-path-v1 subtree deny]]` | refuse R-14 (none in a patch) |
| PR-46 | `… [-> resources !mcp-resources-v1 perm=ask [-> ynab:main !cg-root-v1 subtree op=[read]]]` | ok (perm may sit on any node; R-7 exempts it) |
| PR-47 | `[moxy !clown-plugin-v1 [-> folio !mcp-server-v1 [-> _ !mcp-tool-v1 perm=allow]]]` | refuse R-4 (a perm-only `_` is still a wildcard) |
| PH-1 | head with `! espalier-profile-v2` | refuse H-1 |
| PH-2 | head with no `mode` | refuse H-2 |
| PH-3 | `mode = supervised` | refuse H-3 |
| PH-4 | `mode = unattended`, `harness = claude-code` | refuse H-4 |
| PH-5 | `mode = attended` with `provider = openrouter` | refuse H-5 |
| PH-6 | `mode = unattended` with no `model` | refuse H-6 |
| PH-7 | `mode = unattended` with no `_base` | refuse H-7 |
| PH-8 | `mode = attended` with no `_base` (a root profile) | ok |
| PH-9 | `role = reviewer` | refuse H-8 |
| PH-10 | head with `- owner = me` | refuse H-9 |
| PM-1 | unattended, body has `… [-> read !mcp-tool-v1 perm=ask]` | ok (M-1 retired: the enforcer resolves ask by posture) |
| PM-2 | unattended, body has `[clown-builtin-jobs … [-> troupe !mcp-server-v1 [-> chat_send !mcp-tool-v1]]]` | refuse M-2 |
| PM-3 | attended, body lacks `clown-builtin-jobs/troupe` | refuse M-3 |
| PM-4 | D2 sample A, body rewritten per RFC 0018 (`tool-arg-v1` nodes as `tool-input-v1`, `!just-recipe-v1` given the id `_` and `open` rules applied) | ok |

## 6. Status of the code

`internal/*/profilerules` carries the rule table (`Rules`: id, severity,
message template) and `vectors.json` (the rows above, the `…` elisions
completed into whole documents). Its test pins that the two agree with
each other. `Validate` is not written: it needs `pkgs/ajour`'s parser,
which reaches clown through a new flake input; that input, and the
validator, are clown#349. For R-7 on plugin cg types the validator would
also read the live catalog listing (RFC 0018 §10). The listing's first cut
carries no resource surfaces, so those types stay unchecked.

## More information

- RFC 0018 (the vocabulary these rules read).
- cutting-garden FDR 0028 "The stencil fragment", "Matching", "Open items"
  3 (`open`); `ajour(7)`.
- circus `docs/plans/2026-10-22-d2-profile-schema.md` §2 (the head), §5
  (the two rulesets), §7 (V-D2-* vectors).
- The ajour writing probes, runs 2 to 4 (cutting-garden swift-plum).

---
status: proposed
date: 2026-11-10
---

# The capability catalog contract: what an enforcer presents to ajour

## Abstract

ajour (cutting-garden FDR 0028) decides whether a tool call is admitted
under a session's stack of capability profiles, and it matches only what the
enforcer presents: a path of **subjects** from a plugin down to the
dispatching node, one value path per input, and one resource path per object
the call touches. This RFC is the vocabulary those subjects are drawn from,
which clown owns (FDR 0028, "Asks to the other lanes", item 2): every node
type and its layer, the fields and id of each subject, which input fields
are path-typed or tree references, the verbs, when a subject is `New`, how a
JSON value is spelled, and where the harness built-ins and moxy's overflow
blobs hang. Every enforcer (moxy for its tools, the clown PreToolUse hook for
the harness's built-ins, spinclass for its verbs) builds `ajour.Call` from
this document and nothing else; every profile writer writes against it.

The machine-readable copy is `internal/*/catalogcontract`
(`catalog.json`, embedded; facade `pkgs/catalogcontract`). Where the two
disagree, this RFC is normative and the table is a bug.

Tracks clown#347. Companion: RFC 0019 (the profile ruleset).

## Conventions

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are as in RFC 2119.
"Subject" and the `Subject` fields (`Type`, `ID`, `New`, `Digest`,
`Fields`), "Path", "Values", "Resources" and "Anchor" are FDR 0028's
`ajour.Call`. A **stencil** is a capability profile body; a **writer** is
whoever authors one. Evidence labels as in FDR 0028: **R** an operator
ruling, dated; **P** a writing-probe finding (cutting-garden's ajour probe,
runs 1 to 4, 2026-11-09/10), with its convergence count.

## 1. Node types

| Type | Layer | Presented as | Parent | Id | Fields |
|---|---|---|---|---|---|
| `clown-plugin-v1` | catalog | `Path[0]` | root | the clown plugin name (`moxy`, `just-us`, `spinclass`, `cutting-garden`, `clown-builtin-jobs`, `clown-builtin-harness`) | none |
| `mcp-server-v1` | catalog | `Path[1]` | plugin | the MCP server name inside the plugin; for moxy, the child server (`folio`, `rg`, `smith`, …), and `moxy` for moxy's own resources | none |
| `mcp-tool-v1` | catalog, lockable | `Path[2]` | server | the tool name as its server's manifest spells it, verbatim (`read`, `run_recipe`, `issue-list`, `Read`); never the harness's prefixed form (`mcp__plugin_moxy_moxy__folio_read`) | `kind`, `perm`, `async`, `impure` (§3) |
| `just-recipe-v1` | catalog, lockable | the first step of a `run_recipe` call's recipe value path | `run_recipe` | the recipe namepath verbatim (`test-go`, `explore::debug-io-probe`, `zz-tests/run`) | `group` (every group the recipe is in) |
| `recipe-arg-v1` | instance | a value path's step under the recipe | recipe | the recipe's declared parameter name at that position (a variadic parameter names every position it absorbs) | `position` (1-based), `value` |
| `tool-input-v1` | instance | a value path's only step | the last `Path` node | the input field's name | `field`, `value` |
| `fs-path-v1` | instance | a value path's only step | the last `Path` node | a `file:` id (§4.1) | `field` |
| `mcp-resources-v1` | catalog | a resource path's first step | server (`Path[Anchor]`) | `resources` | `op` |
| `mcp-resource-v1` | instance | a resource path's step under the surface | surface, or `mcp-resource-v1` | the canonical URI (§5.2) | `op` |
| `cg-root-v1` | instance | a resource path's step under the surface | surface | the root as cutting-garden's roots listing spells it (`ynab:main`, `caldav:fastmail`) | `op`, `scheme` |
| the plugin's own type | instance | every step below a cg root | `cg-root-v1` or a plugin type | the node's id relative to its parent, as `cg organize` prints it | `op`, and on a `New` step the proposed object's fields |
| `cg-field-v1` | instance | the last step of a patch or put path | a plugin type | the field's name | `field`, `value`, `op` |

Retired (R 2026-11-09): `cg-roots-v1` and `cg-node-v1`. A cg object carries
the plugin's own node type at every step; "any node type" is a type-less
node. `tool-arg-v1` is retired too (§9 item 1): an enumerable subcommand is
`[-> _ !tool-input-v1 field=subcommand value=[…]]`.

A tool's `Path` is exactly three steps (`[plugin, server, tool]`), a
`run_recipe` call included. `Anchor` is always `1`, the server.

**`run_recipe`** (cutting-garden/swift-plum, 2026-11-10; this replaces FDR
0028's "the path continues into the recipe"). The recipe is a **value
path** under the tool step, as a resource path is under the surface:
`[{just-recipe-v1 release, group…}, {recipe-arg-v1 verb, position 1,
value status}, …]`, one value path per argument, each starting at the
recipe step, plus one value path of the recipe step alone, so a recipe
called with no arguments is still checked. A lock on the recipe
(`release@<digest>`) applies to the value step (FDR 0028 rule 3).
`run_recipe`'s other inputs (`timeout`, `async`, `impure`) are ordinary
`tool-input-v1` values under the tool, siblings of the recipe, so
`[-> run_recipe !mcp-tool-v1 [-> _ !just-recipe-v1 group=[test]] [-> _
!tool-input-v1 open]]` means what it says. The call's `async: true` is
such an input value, never the tool subject's `async` field (§3).

## 2. Ids

- **Catalog ids are verbatim names.** No case folding, no underscore or
  hyphen normalization (FDR 0028 "Idioms": `run_recipe`, `issue-list`).
- **A moxy tool's server and tool come from its advertised name.** moxy's
  tools/list names a child tool `<server>.<tool>` (`folio.read`,
  `smith.issue-list`): the server id is the text before the first `.`, the
  tool id the rest. A name with no `.` is moxy's own tool, under server
  `moxy`. The `_`-joined spelling (`folio_read`, inside
  `mcp__plugin_moxy_moxy__folio_read`) is Claude Code's mangling of that
  name and is never an id. `[-> folio_read !mcp-tool-v1]` matches nothing;
  RFC 0019 warns on it (W-5). Limit: the rule assumes moxy's default name
  template; a non-default `--name-template` defeats it (§9 item 7).
- **Every instance subject has an id**, so a writer may name it or use `_`:
  a `tool-input-v1` and a `cg-field-v1` subject's id is the field name, a
  `recipe-arg-v1`'s is the parameter name, a `fs-path-v1`'s is the `file:`
  id, a resource step's is its own id within its parent. This makes
  `[-> repo !tool-input-v1 value=[a]]` and
  `[-> _ !tool-input-v1 field=repo value=[a]]` equivalent; the second is the
  documented idiom (P ×7 wrote it).
- **Locks.** `Subject.Digest` on a tool is the blake2b256 markl id
  (blech32, `blake2b256-…`) of the canonical JSON (sorted keys, no
  insignificant whitespace, UTF-8) of the tool's MCP `inputSchema`; on a
  recipe, of the recipe's entry in `just --dump --dump-format json`. The
  enforcer presents the digest it computed from what it is about to
  dispatch; an enforcer that cannot compute one presents none (and every
  lock under it refuses, FDR 0028 rule 3). The values a writer locks to are
  to be published by the live listing (§10). The listing does not report
  them yet (clown#354).

## 3. Tool subject fields

| Field | Values | Source |
|---|---|---|
| `kind` | `read`, `write`, `exec` | the moxin manifest's `kind` (moxin(7) TOOL KIND); for a non-moxy server, the server's own `_meta.kind`; for a harness built-in, §7's table. A tool with no declared kind presents no `kind` field (an atom on it then never holds). |
| `perm` | `allow`, `ask`, `dynamic` | the manifest's `perms-request`: `always-allow` is `allow`, `each-use` and `delegate-to-client` are `ask`, `dynamic` is `dynamic` (the tier is resolved per call by path, so it is not a pin). Harness built-ins present none. |
| `async` | `yes`, `no` | the tool's manifest declaration, when its manifest has one |
| `impure` | `yes`, `no` | the tool's manifest declaration, when its manifest has one |

All four are facts about the TOOL, from its manifest, and are pins (D2
V-D2-11: a profile's `perm=allow` against a manifest's `ask` refuses).
No manifest declares `async` or `impure` today, so no tool subject
presents them and an atom on them never holds (§9 item 3). A call's own
`async: true` or `impure: true` argument is an input value (§4), never
one of these fields.

**Wrappers present the wrapped call.** moxy's `async` tool presents the
call named by its `tool` argument with `args` as its inputs; `batch`
presents one call per sub-call, and the batch is admitted only when every
sub-call is. The wrapper itself presents no subject.

## 4. Inputs: one kind per field

Every top-level input field of a call is presented as exactly one kind,
the kind this contract declares for that tool's field (FDR 0028 "One kind
per field"):

| Kind | Presented as | Example fields |
|---|---|---|
| **path** | one `fs-path-v1` subject per path, `field=<name>`, no verb (the tool is the verb); a value that is a URI with a scheme other than `file:` is a resource read instead (§4.2) | `Read.file_path`, `folio cp.source`, `rg search.path` |
| **reference** | one resource path per object named, under the surface of the server that owns the URI's scheme (§5) | `list_nodes.uri`, `mutate_nodes.groups`, `chrest read-resource.uri`, `ReadMcpResourceTool.uri` |
| **content** | a value that is, whole and trimmed, a `madder://blobs/<digest>` URI is a resource read under moxy's surface; any other value is a `tool-input-v1` subject | `jq.stdin`, `toolbox run.stdin` |
| **recipe** | the recipe value path (§1); `args` adds one `recipe-arg-v1` step per argument | `run_recipe.recipe`, `run_recipe.args` |
| **route** | nothing: the enforcer consumes it to build the call (which server, which wrapped call) | `ReadMcpResourceTool.server`, moxy `async.tool`/`args` |
| **input** (default) | one `tool-input-v1` subject, `field=<name>`, `value=<the value, §4.3>` | everything not declared otherwise |

A URI-carrying field MUST NOT be left at the default: the table in
`catalog.json` declares every path, reference and content field this
contract knows. An undeclared field of a known tool, or any field of a tool
the table does not list, is **input**. That default is a known hole for a
path field the table misses (`[-> _ !tool-input-v1 open]` would admit it);
the remedy is the table, and §9 item 4 asks moxy to carry the kind in its
manifests.

### 4.1 `file:` ids

The enforcer canonicalizes a path before presenting it: relative paths
are resolved against the call's working directory (for grit's `paths`,
against the resolved `repo_path`), then made absolute, `.` and `..`
removed, symlinks resolved, no trailing slash. Then:

- a path at or under the user's home directory is presented as `file:`
  followed by the path **relative to home**, with no leading `/` and no
  `~` (`file:eng/repos/circus/.worktrees/swift-plum/src/a.go`); home itself
  is `file:.`;
- any other path is `file:` followed by the absolute path
  (`file:/nix/store/abc-x/bin/x`, `file:/etc/hosts`).

So a leading `/` after `file:` always means "outside home". This is the
spelling every D2 and FDR 0028 sample and every probe writer used (P ×7),
and it keeps a profile portable across users. The catalog listing prints
the canonical id, and a writer copies it from there: a worktree known by a
symlinked path is presented by its resolved path, so a profile that spells
the symlink never matches. A path the enforcer cannot
canonicalize (a dangling symlink in a write's parent chain is fine; an
unreadable intermediate is not) refuses the call before ajour is asked.

### 4.2 A URI in a path field

"One door" (R 2026-11-09): a path field holding a URI with a scheme other
than `file:` is a resource read under the surface of the server that owns
the scheme (`folio read` of `madder://blobs/<d>` is a read under
`moxy/moxy`'s surface). A scheme no catalog server owns (`https:`, `ssh:`,
`git@host:` in `grit_clone.source`) is a resource of the server that
performs the fetch: it hangs under the **called** server's own surface as a
single `mcp-resource-v1` step whose id is the URI (closed by default, like
every resource).

### 4.3 Values

- A JSON string presents its text verbatim, unquoted.
- Every other JSON value presents its canonical JSON text: `true`, `false`,
  `null`, numbers as the shortest JSON spelling that round-trips (`5`,
  `1.5`, never `5.0`), objects with sorted keys and no whitespace.
  `value=[false]` matches `false`. A string `"true"` and the boolean `true`
  present alike; a writer who must tell them apart cannot (stated, not
  fixed).
- **A top-level array presents one subject per element**, each with the
  same `field`, each admitted independently; an empty array presents one
  `tool-input-v1` subject whose value is `[]`. (Presenting `[a, evil]` as
  one multi-valued subject would let a positive `value=[a]` admit it, FDR
  0028 rule 4 being existential.) The same holds for path and reference
  arrays (`jq.files`, `folio rm.paths` split on whitespace, `grit_stage.paths`).
- **An omitted field that the tool resolves from its environment is
  presented with the resolved value** (FDR 0028 ask 3): smith's `repo` and
  `host` from the working directory's remotes, grit's `repo_path` and the
  directory fields of `folio ls|glob|tree|df`, `rg search`, `Glob`, `Grep`
  from the working directory, spinclass `grit_*`'s `repo_path` from the
  session worktree. The table marks these `resolve = "cwd"` or
  `"cwd-remote"`. An enforcer that cannot resolve such a field refuses the
  call; it MUST NOT present nothing, since a value-list allowance would then
  be bypassed by omission. An omitted field with a schema default the tool
  applies itself presents nothing.

## 5. Resources

### 5.1 The surface

Every server that has resources has one surface, `resources
!mcp-resources-v1`, a child of the server beside its tools (placement
option A, R 2026-11-09). A plugin's own resources hang under a server node
named for the plugin: moxy's `madder://blobs/…` under `moxy/moxy`, clown's
diagnostics under `clown-builtin-diagnostics/disconnects`. Tool nodes carry
no resource children.

**Scheme ownership.** Each surface owns URI schemes, and a reference is
routed by its scheme, never by which tool or `server` argument carried it:

| Scheme | Owner (plugin/server) |
|---|---|
| `madder:` | `moxy/moxy` |
| `chrest:` | `moxy/chrest` |
| `nebulous:` | `moxy/nebulous` |
| `clown:` | `clown-builtin-diagnostics/disconnects` (session records) |
| every scheme cutting-garden's roots listing reports (`caldav:`, `ynab:`, `smith:`, `org:`, `newsblur:`, `fastmail:`, …) | `cutting-garden/cutting-garden` |

This table is the seed for the scheme set. The live listing (§10) becomes
authoritative once it lists resource surfaces, which its first cut does
not.

### 5.2 Resource paths

- **A cutting-garden resource** is `[resources, <root> !cg-root-v1, <step>
  …, <object>]`: one step per level of containment, each with its own id
  relative to its parent and the plugin's own node type, exactly as
  `list_nodes` descends to it. The enforcer obtains the levels and types
  from cutting-garden (the object's ancestry), not by splitting the URI.
- **Any other MCP resource** is `[resources, <uri> !mcp-resource-v1]`: one
  step whose id is the canonical URI (lowercase scheme, `scheme://`
  spelling where the scheme uses it; `madder://blobs/<digest>`, never
  `madder:blobs/…`). Containment is segment-prefix on that id, so
  `[-> madder://blobs !mcp-resource-v1 subtree op=[read]]` covers every
  blob.
- **A patch or put** presents the object's path once per top-level field
  of the proposed body, each ending in a `cg-field-v1` step
  (`field=<name>`, `value=<§4.3>`), every one admitted independently (FDR
  0028; an allowlist by omission).
- **A creation** presents `[…, <container>, <New object>]`: the last step
  is a `New` subject with the proposed object's type, an empty id, and its
  proposed fields (`summary`, …) as `Fields`.
- **A sweep** (`mutate_nodes` with `sweep`) presents one resource path per
  node the filter matches, resolved by the enforcer with a read before the
  write; an enforcer that cannot resolve the set refuses.

### 5.3 Verbs

The verb is the field `op`, on **every** step of a resource path (the
surface step included). One subject carries exactly one verb.

| Node types | Verbs |
|---|---|
| `mcp-resources-v1`, `mcp-resource-v1`, `cg-root-v1`, plugin types, `cg-field-v1` | `read`, `patch`, `put`, `delete`, `create` |
| catalog types, `fs-path-v1`, `tool-input-v1`, `recipe-arg-v1` | none |

- **Reads.** A `list_nodes`, `read_facets`, `read_node`,
  `resources/read`, `ReadMcpResourceTool`, `ListMcpResourcesTool` presents
  `op=read` on every step of the path it reads. There is no `list` verb:
  listing a container is a read of it.
- **Creation** (R 2026-11-10). A creation presents `op=create` on every
  step, the container steps included, and a `New` subject at the last step.
  The `op=` atom ignores a `create` value, so the container steps match by
  type and id alone and the `+` node alone admits or refuses the creation
  (FDR 0028 rule 2). `op=create` never appears in a profile (RFC 0019 rule
  R-6).
- **`New` is a birth, not a write.** `New` is set only on the last step of
  a creation under a container (`mutate_nodes` `create`, organize's `+`
  box). A write to a path that does not exist yet is `op=write` on that
  path's `file:` id, never `New` (FDR 0028 bug 1).
- **Paths carry no verb: the tool is the verb.** `Read` reads its
  `file_path`, `folio write` writes its. `folio cp`'s `source` and
  `destination` are told apart by `field=`, not by a verb.

## 6. The verb on catalog nodes and paths

Catalog nodes and `fs-path-v1` subjects carry no verb (FDR 0028). "Read
anywhere, write here" is spelled by tool, the way FDR 0028's complete
document and every probe writer spelled it: `[-> _ !mcp-tool-v1 kind=read
[-> file:eng/repos !fs-path-v1 subtree] …]` beside `[-> write !mcp-tool-v1
[-> file:<worktree> !fs-path-v1 subtree] …]`. See §9 item 6 for why a
path verb is deferred.

## 7. The harness built-ins

The tools the harness itself implements (claude-code's `Read`, `Edit`, …),
governed by clown's PreToolUse hook, hang under a plugin and server clown
declares for them:

    [clown-builtin-harness !clown-plugin-v1
      [-> harness !mcp-server-v1
        [-> Read !mcp-tool-v1 [-> file:… !fs-path-v1 subtree] [-> _ !tool-input-v1 open]]]]

This is the shape the run-4 engineer writer invented unprompted (P ×1, and
the "where do built-ins hang" gap was ×15 across runs); it follows clown's
own `clown-builtin-*` plugin naming, so it is adopted as guessed.

| Tool | `kind` | Path fields (`op`) | Reference fields | Notes |
|---|---|---|---|---|
| `Read` | read | `file_path` (read) | | |
| `Edit` | write | `file_path` (write) | | |
| `Write` | write | `file_path` (write) | | not `New`, ever |
| `NotebookEdit` | write | `notebook_path` (write) | | |
| `Glob` | read | `path` (read, resolve cwd) | | `pattern` is input; see below |
| `Grep` | read | `path` (read, resolve cwd) | | |
| `Bash` | exec | | | `command` is input, never parsed |
| `Agent` | exec | | | |
| `WebFetch` | read | | | `url` is input: no catalog server owns `http(s):` |
| `WebSearch` | read | | | |
| `Skill` | exec | | | |
| `ReadMcpResourceTool` | read | | `uri` | see below |
| `ListMcpResourcesTool` | read | | | `server` is route; see below |

Any other built-in (`TaskCreate`, `AskUserQuestion`, …) presents its fields
as input and no `kind`.

**A glob pattern stays inside its `path`.** `Glob.pattern`, `folio
glob.pattern` and `Grep.glob` are input values, so the `path` field is
the only scope a stencil sees. The enforcer therefore refuses, before
ajour is asked, a pattern that is absolute or has a `..` segment: either
would reach outside the `path` it is presented with.

**`Bash` and `toolbox run` are not scoped by this contract.** Its `command` is one opaque
`tool-input-v1` value (as is `toolbox run`'s, whose command line names
the files toolbox reads); a stencil that admits either with `command`
open admits every path the command can name, and for `Bash` every network
reach the shell has. RFC 0019 warns on both (W-1). Narrowing Bash is the confinement layer's (FDR 0020), not
ajour's.

**`ReadMcpResourceTool` and `ListMcpResourcesTool` present no harness tool
step.** ajour's `Anchor` indexes into the call's own `Path`, so a harness
tool cannot hang a resource under another server. The enforcer presents
the call as the resource read it is: `Path = [owner plugin, owner server]`,
`Anchor = 1`, `Resources = [[resources, …]]` with `op=read`, the owner
chosen by the URI's scheme (§5.1). `ListMcpResourcesTool` presents the
surface step alone (`[[resources]]`) of the named server, or one such call
per server when `server` is omitted. A `server` argument that names a
server other than the scheme's owner refuses before ajour is asked. A
direct MCP `resources/read` presents the same call. A read of an overflow
digest moxy returned to this session presents no call at all (§8). A
`[-> ReadMcpResourceTool !mcp-tool-v1]` node in a stencil is therefore
inert; RFC 0019 warns on it (W-2).

## 8. moxy's overflow blobs

moxy writes a large tool output to the madder cache and returns
`madder://blobs/<digest>`; no profile can forbid that write (FDR 0028
Limitations). A read of a blob is a resource read under `moxy/moxy`'s
surface, whichever tool carried the URI (`folio read`, `jq.files`,
`jq.stdin`, `ReadMcpResourceTool`). moxy keeps a per-session ledger of the
digests it returned; a read of one of those is the admitted call's
continuation and presents **no call at all**. A read of any other digest
needs `[moxy !clown-plugin-v1 [-> moxy !mcp-server-v1 [-> resources
!mcp-resources-v1 [-> madder://blobs !mcp-resource-v1 subtree
op=[read]]]]]`.

## 9. Open items

1. **Retire `tool-arg-v1`** (agreed with cutting-garden/swift-plum
   2026-11-10; D2's table and sample A follow, eager-holly). D2 sample A spells an enumerable subcommand as
   `[-> status !tool-arg-v1 arg=subcommand]`; every probe writer who scoped
   a subcommand (P ×5) wrote `[-> _ !tool-input-v1 field=subcommand
   value=[…]]` instead, and "one kind per field" forbids presenting
   `subcommand` both ways. Proposal: no field is presented as
   `tool-arg-v1`; RFC 0019 refuses the type with a pointer to the
   `tool-input-v1` spelling. Needs circus D2 §2's table to follow
   (eager-holly).
2. (Decided 2026-11-10, §1: the recipe is a value path under
   `run_recipe`.)
3. **`async` and `impure` pins have no source.** D2 §2 lists them with
   `kind` and `perm` as manifest pins, but no manifest declares them, so
   no tool subject presents them today. A manifest key is needed before
   D2 sample A's `async=no impure=no` can hold.
4. **moxy manifests declaring field kinds.** moxin(7)'s `substitute-args`
   names the fields a tool reads as files, which is most of the path kind;
   asking moxy for a `path-args` (with verb) and `reference-args` key would
   make the manifest the source and this table the fallback. Ask: moxy
   (moxy#489's lane).
5. **Segment wildcards in `file:` ids** (`file:eng/repos/*/.worktrees/x`).
   Not in the stencil fragment; `_` inside an id is a literal character.
6. **A verb on paths** (deferred, cutting-garden/swift-plum 2026-11-10).
   `folio mv`'s `source` is read AND delete: presenting `op=[read, delete]`
   lets `[-> file:/wt !fs-path-v1 subtree op=[read]]` admit the move by
   FDR 0028's existential rule, and picking one verb depends on a per-tool
   "most destructive verb" convention no writer can see. Revisit if a tool
   with path fields appears whose verbs `kind=` cannot split.
7. **moxy's canonical server, tool, perm and kind** (transitional in the
   live listing, §10). moxy's tools/list carries none of them in `_meta`
   (moxy/live-aspen, 2026-11-10). The listing therefore:
   - splits moxy's advertised name at the first `.` (§2), which holds
     only under the default `{server}.{tool}` template;
   - reads `perm` from the `[perms: X]` suffix moxy appends to each
     description (prose moxy may reword);
   - presents `kind` only when a server's `_meta.kind` is `read`, `write`
     or `exec`. moxy's existing `_meta.kind` is moxin(7)'s result label
     (`list`, `issue`), a different vocabulary, and no moxin declares it
     today.

   The most consequential effect (the first ajour dry run, 2026-11-10;
   clown#356): FDR 0028's `[-> _ !mcp-tool-v1 kind=read …]` idiom never
   admits a moxy call, and every sample and probe writer reached for it.
   Until moxy presents a kind, a profile that admits moxy reads must name
   each tool. RFC 0019 W-6 warns on the idiom.

   Proposed keys: `_meta` keys for the canonical server, tool and
   perms-request, with `kind`'s values settled separately so the two
   meanings do not share a name. This is a moxy issue, routed via circus
   eager-holly; the spellings are moxy's to choose.

## 10. Publication

- **This RFC** is the normative text for writers and enforcers.
- **`catalog.json`** in `internal/*/catalogcontract` (facade
  `pkgs/catalogcontract`) carries §1's node types, §5.1's scheme owners and
  §4 to §7's per-tool field kinds, verbs and resolution rules for every
  tool this contract names. It is the one hand-written source (nothing is
  generated from it, so nothing can drift from it); `Load` parses it,
  `Field(plugin, server, tool, field)` answers "how is this input
  presented" (an undeclared field is input), `SchemeOwner` routes a URI,
  and `Raw` returns the file's bytes so another enforcer's table (moxy's
  hand-typed shadow-mode table) is a copy of this data rather than a
  second source. Its tests pin §1's type list, one kind per field, and
  that every kind, verb, resolution and presentation it names is one this
  RFC declares. Field entries beyond the RFC's prose: `split = whitespace`
  (one value per word: `folio rm.paths`), `relative_to` (a relative path
  resolves against another path field, or `repo-root`).
- **The live listing** (D2 §6, clown#350) is
  `cg list -format espalier clown-catalog://<principal>/…`. It is served by
  `clown catalog traversal-serve`, a cutting-garden traversal plugin
  (cutting-garden RFC 0013, `traversal-plugin/v1`). The protocol is
  hand-rolled, as smith's is, so it needs no cutting-garden flake input.
  - **Snapshot.** Each launch records, in the background after its servers
    start and after `--cheap-context`'s exclusions, what every bound
    server's tools/list reported. The record holds names, descriptions,
    input schemas and `_meta` only, never URLs, commands, args or env
    (#292). It is written to `$XDG_STATE_HOME/clown/catalog/<sha256(principal)>.json`
    (0600).
  - **Roots.** One root per running session: the snapshot records its
    frontend's pid and start ticks, and a root is served while that pid
    exists with the same ticks (an existing pid is enough where `/proc` is
    absent). A dead session's snapshot is not served. Each launch deletes
    the snapshots of dead sessions written more than 7 days earlier
    (`catalogsnap.Retention`); a live session's is never deleted. The age
    is measured from the snapshot's `written_at`, not from its start ticks:
    ticks count from boot, so they decide liveness only.
  - **Shape.** The URI carries no hostname: the plugin serves one host, and
    a hostname in every id is a redaction hazard when a listing is pasted.
    Plugin, server and tool nodes are containers. A tool also has a body,
    `leaf.read` → `{kind?, perm?}`, which never carries the schema.
    Its inputs (`tool-input-v1`, the schema's top-level properties) are
    leaves whose `field_kind` facet is §4's kind. Each type declares a
    `uri_template` (cutting-garden RFC 0018), one variable per segment, so
    the host resolves a tool URI to its body-declaring type. `just
    explore-catalog-conformance <cutting-garden checkout>` runs
    cutting-garden's conformance driver and portable bats cases against it.
  - **Locks are not reported yet.** §2's digest needs blake2b256, which Go's
    standard library lacks, and adding `golang.org/x/crypto` to `go.nix` is
    held for the operator (clown#354). A stopgap spelling was ruled out,
    since profiles would be written against it. The snapshot keeps each
    `inputSchema` verbatim, so the digest can be reported without
    relaunching. An absent live lock is a mismatch (FDR 0028 rule 3), so no
    profile can lean on one meanwhile.
  - **What is listed.** Tool nodes carry `kind` and `perm` facets when
    known (§9 item 7). For a claude launch the harness built-ins are listed
    from §7's table (they have no schema, so no lock). Tools that present no tool step (moxy
    `async` and `batch`, `ReadMcpResourceTool`, `ListMcpResourcesTool`)
    are not listed.
  - **Built-in inputs.** The frontend cannot read the harness's own tool
    schemas, so a built-in lists only the fields `catalog.json` names
    (`Read.file_path`, `Bash.command`, `Grep.pattern`, …), not every
    parameter it takes.
  - **Approximations.** A built-in turned off for the launch is still
    listed. Under `--mcp-collapse` the snapshot records the upstream
    servers, not the aggregator claude binds.
  - **Not listed yet.** Servers reserved for an agent unit (not started by
    the frontend) are absent. `just-recipe-v1` and `recipe-arg-v1` keep
    their type names here only: who serves recipes and their locks, just-us
    or clown, is #352.
  - **Setup.** Registering the plugin's `[[traversal_plugins]]` stanza
    (`command = ["clown", "catalog", "traversal-serve"]`, scheme
    `clown-catalog`) in the host's cutting-garden `config.toml` is a host
    configuration step outside this repo.

## More information

- cutting-garden FDR 0028 (ajour) "The two inputs", "Matching", "Resources
  as subjects", "Asks to the other lanes" item 2; `ajour(7)`.
- circus `docs/plans/2026-10-22-d2-profile-schema.md` §2 (the node-type
  table this RFC refines), §6 (the catalog as a cutting-garden tree).
- RFC 0019 (the profile ruleset); RFC 0008 (plugin tool-permission hooks);
  FDR 0020 (agent-scope confinement); moxin(7) PERMISSIONS, TOOL KIND.
- The ajour writing probes, runs 2 to 4 (cutting-garden swift-plum), whose
  writers' guesses this RFC conforms to where it could.

---
status: draft
date: 2026-04-26
---

# Clown Plugin Sandboxing Posture

## Abstract

Clown takes no position on isolation of plugin-supplied MCP servers. Each
plugin author is responsible for sandboxing the processes their plugin
spawns; clown imposes no host-level confinement and provides no manifest
field for declaring isolation. This specification documents that posture
explicitly so plugin authors and clown consumers know where the trust
boundary sits.

This RFC is scoped to MCP-server plugin code paths. Subagent execution
sandboxing (RFCs 0003 and 0005) is a separate concern, currently paused,
and out of scope here.

## Introduction

Clown loads plugins via `mkCircus` (RFC 0001) and may launch HTTP MCP
servers declared by those plugins via `clown-plugin-host` (RFC 0002). A
parallel mechanism for stdio MCP servers is under design (issue #28).

In all of these cases, clown today launches plugin processes with the
invoking user's full environment and privileges. There is no host-level
sandbox, no namespace isolation, and no per-plugin confinement layer
imposed by clown. This has been the de facto posture since the first
plugin shipped, but it has never been written down. As more plugins
appear (moxy today; bob and spinclass as future candidates), the absence
of a stated posture creates ambiguity about who is responsible for what
and where the trust boundary sits.

This RFC makes the posture explicit and rejects adding a clown-level
sandbox mechanism. The sandboxing clown already provides — disabling
Bash and auto-mode at the Claude Code managed-settings layer — is
sufficient for the current threat model. Per-plugin process-level
isolation, if needed at all, is the plugin author's responsibility.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this
document are to be interpreted as described in RFC 2119.

## Specification

### 1. Scope

This RFC applies to plugin code paths where clown launches or hosts
processes on behalf of a plugin:

- HTTP MCP servers managed by `clown-plugin-host` (RFC 0002)
- stdio MCP servers bootstrapped via the future plugin protocol
  (issue #28), if and when it lands

It does NOT apply to:

- Subagent execution paths and Nix-builder-as-sandbox work (RFCs 0003,
  0005, currently paused). When that work resumes, the relevant RFCs
  MAY revisit clown's posture for that distinct code path.
- Claude Code session-level guardrails (managed settings, Bash
  disablement, auto-mode disablement). Those are configured by clown
  but are orthogonal to plugin process isolation.

### 2. Posture

Clown MUST NOT impose any host-level sandbox, namespace, seccomp
filter, capability drop, filesystem chroot, or other process-level
confinement on plugin-supplied MCP servers.

Plugin processes run as the invoking user, in the user's environment,
with the user's privileges. Clown's responsibility ends at process
spawn and lifecycle management (RFC 0002 §3).

Each plugin is responsible for whatever isolation it deems necessary
for the processes it spawns. Plugins that launch subprocesses, write
to disk, hold credentials, or open network listeners MUST handle
their own isolation. Plugins MAY use host-provided tools (bwrap,
sandbox-exec, container runtimes, language-level sandboxes, etc.);
the choice is the plugin author's.

### 3. Mechanism

This RFC introduces no mechanism. There is no manifest field, no
declaration syntax, and no clown-side handling for sandbox claims.

Specifically, `clown.json` (RFC 0002) MUST NOT gain a `sandbox`,
`isolation`, `confinement`, or similarly named field. The posture
operates at the documentation and convention layer:

- A plugin's documentation SHOULD describe the trust boundary the
  plugin author has chosen, including any isolation the plugin
  applies internally.
- Clown does not surface, validate, or display this information.
- Consumers who care about isolation read plugin documentation
  directly.

### 4. Survey

The current ecosystem of clown plugins is small enough to enumerate.

| Plugin | Status | Posture |
|--------|--------|---------|
| moxy | Existing | Unsandboxed by clown. Runs with user privileges. moxy's own isolation choices (if any) are documented in the moxy repository. |
| bob | Future candidate | N/A until shipped as a clown plugin. Inherits this posture when it does. |
| spinclass | Future candidate | N/A until shipped as a clown plugin. Inherits this posture when it does. |

For each of these, when (or if) they ship as clown plugins, the
plugin author retains full discretion over isolation choices.

### 5. Verification

Clown does not verify any sandbox claims a plugin makes in its own
documentation. Consumers MUST NOT assume the absence of a sandbox
claim means the plugin is, or is not, isolated.

### 6. Migration

No code change is required. This RFC documents existing behavior.

Plugin authors SHOULD add a brief "Trust boundary" or "Isolation"
section to their plugin's README (or equivalent) describing the
isolation posture, if any. This is a convention, not a clown-enforced
requirement.

## Rejected Alternatives

### Host-level uniform sandbox

Clown wrapping every plugin process in a uniform sandbox (e.g.
bwrap-based on linux, sandbox-exec on darwin) was considered and
rejected:

1. Plugins legitimately need broad host access. moxy's tools wrap
   `git`, `just`, the user's filesystem, GitHub credentials, and
   more — a uniform sandbox would either be permissive enough to be
   useless, or strict enough to break valid plugins.
2. Sandbox semantics on linux and darwin differ enough that
   "uniform" would be aspirational rather than real. Maintaining
   parity would consume engineering effort disproportionate to the
   security benefit.
3. Plugins are user-installed code chosen explicitly. The trust
   relationship is between the consumer and the plugin source, not
   between the consumer and clown.

### Opt-in host-side sandbox wrapper

Adding a clown-managed sandbox wrapper that plugins could opt into
via manifest was considered and rejected:

- It would require defining and maintaining a sandbox abstraction
  across linux and darwin.
- Plugin authors who want isolation already have host-native tools
  and are better positioned to choose them than clown is.
- It would shift ownership of sandbox correctness from the plugin
  author (who knows what the plugin needs) to clown (which doesn't).

### Manifest field, documentation-only

A `clown.json` field that clown reads and displays at install or
load time, without enforcing anything, was considered and rejected:

- Without verification, the field becomes a marketing claim. It
  misleads consumers more than it informs them.
- The same information lives more naturally in plugin documentation,
  where it can describe nuance and context that a one-line manifest
  field cannot.
- It adds schema surface for no behavioral change.

## Security Considerations

The trust boundary for clown plugins is the **plugin source**.
Consumers explicitly choose which plugin flakes to load (RFC 0001)
and pin them to specific revisions; that is the primary defense
against malicious plugins.

A plugin runs as the invoking user, with the user's environment, and
inherits the user's privileges. Anything that user can do, a plugin
can do, including:

- Read and write the user's home directory and other accessible
  filesystem locations
- Make arbitrary network connections
- Spawn subprocesses with the user's privileges
- Read environment variables, including secrets

Consumers who require stronger isolation than this MUST NOT install
untrusted plugins, OR MUST run clown itself within an external
isolation boundary (VM, container, user namespace, dedicated user
account, etc.). Per-plugin in-process isolation is not part of
clown's threat model.

This posture is appropriate because clown plugins are user-installed
extensions with the same trust level as any other tool the user
runs. The distinct question of sandboxing model-driven code
execution paths is governed by RFCs 0003 and 0005 (paused) and by
Claude Code's managed-settings layer that clown configures (Bash
disabled, auto-mode disabled by default).

## Compatibility

No interface or behavior change. This RFC documents existing posture.

## References

### Normative

- [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) — Requirement
  keyword definitions
- RFC 0001 — Parameterized Plugin Loading
- RFC 0002 — Clown Plugin Protocol: HTTP MCP Server Lifecycle
  Management

### Informative

- Issue #26 — RFC: clown-plugin sandboxing posture (this document)
- Issue #28 — Support clown-plugin protocol for stdio MCPs to
  bootstrap outside sandbox (when implemented, falls under this
  posture)
- RFC 0003 — Sandboxed Subagent Schema (paused; out of scope)
- RFC 0005 — Egress Broker Protocol (paused; out of scope)

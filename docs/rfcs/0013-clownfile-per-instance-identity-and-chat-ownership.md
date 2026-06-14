---
status: proposed
date: 2026-06-14
---

# Clownfile, Per-Instance Session Identity, and Clown-Owned Chat

## Abstract

This RFC specifies three clown-owned interfaces introduced by the clown⇆spinclass
rescope: (1) the *clownfile* — clown's unified, hierarchically-cascading
per-instance configuration file carrying both the run profile
(provider/backend/model/env) and the multiplexer *attach* templates that
subsume spinclass's `[session-entry]` defaults; (2) *per-instance session
identity* — every clown instance routes on a unique key derived from the resume
identifier clown already mints, while `SPINCLASS_SESSION_ID` is demoted from a
routing key to a group-label *decoration*; and (3) *chat as a pure clown
construct* — the job-wakeup journal is the sole message store, addressing is
performed entirely through *derived channels* (per-instance, group, broadcast)
plus a clown-owned presence index, and spinclass retains no chat surface. It
extends RFC-0012 and is the clown companion to spinclass FDR-0017.

## Introduction

Several capabilities historically straddled the clown/spinclass boundary:
multiplexer (zmx) session-attach, the assumption of one agent per worktree, and
the cross-session chat facility. The rescope (driven jointly by this RFC and
spinclass FDR-0017) redraws the boundary so that clown owns the per-instance
runtime (attach, identity, chat) and spinclass owns only worktree/session
lifecycle and the identity *value* it injects.

This document specifies the clown-owned half. It is normative for clown and for
any consumer (notably spinclass) that addresses a clown instance or its group.
The spinclass consumer view — what spinclass deletes, what it keeps, and how it
sets the decoration — is normative in FDR-0017; this RFC is cited there by
number.

Scope:

- **In scope:** the clownfile format and discovery; per-instance key derivation
  and the `SPINCLASS_SESSION_ID` decoration; the chat construct (journal store,
  derived-channel addressing, presence index) and its relationship to the
  RFC-0011 job-platform tools.
- **Out of scope:** remote (ssh) attach, which remains spinclass's
  `internal/remote` path (FDR-0011); the clownfile `[attach]` table covers
  *local* modes only. Migrating the remote path is a separate decision.

Background: RFC-0009 (job-wakeup channel: journal, `ChannelID`, session-key
precedence §2, disable switch §8), RFC-0011 (job-platform MCP tools, including
`job_message`/`job_read`), RFC-0012 (session-identity & job-channel ownership;
`whoami`), and the existing per-launch resume identifier clown mints in
`cmd/clown/resume_hint.go` (`prepareClaudeSessionID` → `newUUIDv4`). The
keystone of this RFC is §2: once identity is per-instance, grouping (§3.2) and
per-clown chat (§3) follow.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. The clownfile

The clownfile is clown's unified per-instance configuration. It carries the run
*profile* and the multiplexer *attach* templates in one cascading file, in the
same spirit as spinclass's sweatfile.

#### 1.1 Format and discovery

1. A clownfile MUST be a TOML document named `clownfile`.
2. clown MUST discover clownfiles by the same ascent it already uses to collect
   `.circus/` system-prompt directories (`internal/circus`): walking from `$PWD`
   up to `$HOME`. Files MUST be layered shallowest-first, so a clownfile closer
   to `$PWD` overrides one closer to `$HOME` on a per-key basis (table keys
   merge; scalar and array values at the same path replace).
3. The absence of any clownfile MUST be non-fatal: clown MUST fall back to its
   built-in defaults for every field.

#### 1.2 The `[profile]` table

The `[profile]` table is the home of the configuration previously planned as
`--profile` / `profiles.toml`; that work folds into the clownfile rather than
existing as a second config system.

```toml
[profile]
provider = "claude"          # claude | codex | circus | opencode | crush
backend  = "podman"          # tent backend, when applicable: podman | lima
model    = "claude-sonnet-4-6"
[profile.env]                # injected into the provider process environment
ANTHROPIC_LOG = "info"
```

1. `provider` MUST be one of clown's recognized provider names; an unrecognized
   value MUST be rejected with a diagnostic.
2. `backend`, `model`, and `[profile.env]` keys are OPTIONAL; each MUST default
   to clown's existing built-in behavior when absent.
3. A `--profile <name>` flag or `CLOWN_PROFILE` env var, if later introduced,
   MUST resolve a named `[profile.<name>]` sub-table; explicit CLI flags
   (`--provider`, `--model`) MUST override the resolved profile.

#### 1.3 The `[attach]` table

The `[attach]` table subsumes the multiplexer-invocation defaults spinclass
currently ships as `[session-entry]`. When set, clown wraps *itself* in the
configured multiplexer on boot — "implicit per-instance attach" — instead of an
orchestrator invoking the template.

```toml
[attach]
multiplexer = "zmx"          # zmx | none
spawn  = ["zmx", "attach", "{id}", "--detach", "{entry}"]
resume = ["zmx", "attach", "{id}", "{entry}"]
start  = ["{entry}"]
```

1. `multiplexer` MUST be `"zmx"` or `"none"`. `"none"` (the default when
   `[attach]` is absent) MUST run clown inline with no multiplexer wrapping.
2. `spawn`, `resume`, and `start` are argv-template arrays. The placeholder
   `{id}` MUST be substituted with the instance's per-instance id (§2.1) and
   `{entry}` with clown's own entrypoint argv. Unrecognized placeholders MUST be
   rejected with a diagnostic.
3. When `multiplexer != "none"`, clown MUST resolve the template for the active
   mode and re-exec itself under it before launching the provider, and MUST do
   so after minting the per-instance id (§2.1) so `{id}` is available.
4. The `[attach]` table MUST NOT carry a `remote` mode in this revision; remote
   attach is out of scope (see Introduction) and remains spinclass's.

### 2. Per-instance session identity

This is the keystone. Today clown sets `CLOWN_SESSION_ID` only-if-unset to
`jobwake.SessionKey()`, which in a spinclass worktree resolves to
`SPINCLASS_SESSION_ID` — so every clown in one worktree shares a key and a
channel, making them individually unaddressable (the root of the shared-checkout
ambiguity). This RFC makes the routing key per-instance and demotes
`SPINCLASS_SESSION_ID` to a group decoration.

#### 2.1 The per-instance identifier

1. clown MUST mint exactly one per-instance identifier per launch and use it for
   ALL of: (a) the `CLOWN_SESSION_ID` routing key, (b) the provider session id
   (the claude `--session-id` minted in `prepareClaudeSessionID`), and (c) the
   `clown resume clown://<provider>/<id>` hint. The resume identifier and the
   channel routing key are thereby the same id.
2. The minting MUST occur before clown exports the job-wakeup environment to any
   child or plugin process, so every child inherits the per-instance
   `CLOWN_SESSION_ID`.
3. When the user supplies an explicit session id (`--session-id` / `--resume`),
   clown MUST reuse that id as the per-instance identifier rather than minting a
   fresh one, so a resumed instance keeps its channel.

#### 2.2 The `SPINCLASS_SESSION_ID` decoration

1. `SPINCLASS_SESSION_ID` (the worktree label `<repo>/<branch>`, set by
   spinclass per FDR-0017) MUST be treated as a *group decoration*, not a
   routing key. clown MUST preserve it in the environment unchanged and MUST NOT
   derive the per-instance routing key from it.
2. The group channel is `ChannelID(SPINCLASS_SESSION_ID)` (§3.2). clown MUST
   derive the group channel from the decoration independently of the routing
   key.

#### 2.3 Amendment to RFC-0009 §2 precedence

RFC-0009 §2 resolved the session key as `CLOWN_SESSION_ID` >
`SPINCLASS_SESSION_ID` > `CLAUDE_SESSION_ID` > generated. This RFC amends the
routing-key precedence by removing the `SPINCLASS_SESSION_ID` branch (it becomes
the §2.2 decoration):

> Routing key = `CLOWN_SESSION_ID` (explicit) > `CLAUDE_SESSION_ID` >
> generated.

In the normal flow clown sets `CLOWN_SESSION_ID` to the per-instance id (§2.1),
so children resolve the per-instance key directly. `ResolveSessionKey` MUST
report `source` as before (RFC-0012 §1) for `whoami`, dropping the
`SPINCLASS_SESSION_ID` source.

#### 2.4 `whoami` under per-instance identity

`clown job whoami` (RFC-0012 §1) MUST report the per-instance `sessionKey` and
its `channelId`, and SHOULD additionally report the group decoration
(`SPINCLASS_SESSION_ID`) and its derived group channel, so a consumer can read
both the address of one clown and the group it belongs to from a single call.

### 3. Chat as a clown construct

Chat is a pure clown construct. spinclass retains no chat surface: its
`internal/chat` (store, cursor, wake emit), its `chat-send` / `chat-read` /
`chat-list-sessions` tools, and its `chatroom/` store are all removed (FDR-0017,
normative there). clown owns the full construct, including the recipient
listing.

#### 3.1 Store

1. The job-wakeup journal (RFC-0009) MUST be the sole chat message store. A chat
   message MUST be a `message` journal record written to the target channel,
   equivalent to `clown job message` / the RFC-0011 `job_message` tool. There
   MUST NOT be a separate chat store.
2. Chat read MUST be served from the journal (the RFC-0011 `job_read` path
   filtered to `message` records), enriched with presence data (§3.3). A
   per-reader cursor MUST be keyed by the reader's per-instance channel (§2.1),
   so each clown tracks its own read position.

#### 3.2 Derived-channel addressing

Addressing uses channels derived from keys with no registry and no enumeration.
Each clown's monitor MUST watch three channels:

| Channel | Derivation | Purpose |
|---|---|---|
| Per-instance | `ChannelID(CLOWN_SESSION_ID)` | direct message to one clown |
| Group | `ChannelID(SPINCLASS_SESSION_ID)` | message to the whole spinclass session |
| Broadcast | the well-known global channel (RFC-0009) | message to all sessions |

1. A direct message MUST target the recipient's per-instance key; a group
   message MUST target a `SPINCLASS_SESSION_ID` value; a broadcast MUST use the
   global target (`*`). These map directly onto the `job_message` `--target`
   surface (RFC-0011): the target key is hashed to its channel.
2. A group message therefore reaches every clown that derives the same group
   channel from its decoration — fan-out with zero registry and zero
   enumeration. clown MUST NOT require any per-group index or push-registration
   to deliver a group message.
3. The monitor MUST de-duplicate so a message a clown itself sent to a group or
   broadcast channel does not wake the sender (consistent with the existing
   self-echo suppression).

#### 3.3 Presence index (recipient listing)

The readable recipient listing — formerly spinclass `chat-list-sessions` — is
clown-owned.

1. Each clown SHOULD register a presence record `{perInstanceKey,
   spinclassDecoration, description, lastSeen}` on session start, in a
   clown-owned presence index under the job state directory, and SHOULD refresh
   `lastSeen` periodically.
2. clown MUST expose a chat-list operation that reads the presence index and
   groups records by `spinclassDecoration`, so a caller can see both individual
   clowns and the spinclass sessions they belong to.
3. The presence index MUST NOT be authoritative for *addressing* (addressing is
   derivation-based per §3.2); it serves discovery and human-readable display
   only. A stale presence record MUST NOT prevent a derived-channel message from
   being delivered.

#### 3.4 Degraded mode

Because chat is a clown construct riding the job-wakeup channel,
`CLOWN_DISABLE_JOB_WAKEUP=1` (RFC-0009 §8) or the absence of the clown binary
MUST mean no chat — by design, not as a regression. No spinclass-local fallback
store exists. (This RFC introduces no change to RFC-0009 §8.)

## Security Considerations

- **Channel addressing is hash-based, not capability-based.** `ChannelID` is a
  one-way hash of a key (RFC-0009), so a channel id does not reveal its key, but
  any party that *knows* a `SPINCLASS_SESSION_ID` (a predictable
  `<repo>/<branch>` string) can derive the group channel and inject a group
  message. Group membership is therefore as confidential as the worktree label.
  Consumers MUST NOT treat group-channel writability as authentication of the
  sender; the journal records the sender identity, which receivers SHOULD
  display.
- **Decoration leakage couples to identity hygiene.** Because the per-instance
  key (§2.1) is now distinct from `SPINCLASS_SESSION_ID`, a leaked or inherited
  `CLOWN_SESSION_ID` (clown#135) still mis-routes direct messages; the §2.3
  precedence and the divergence warning (RFC-0012, clown#135) remain the
  mitigations. spinclass MUST set `SPINCLASS_SESSION_ID` per-instance-accurately
  and MUST NOT leak a parent's `CLOWN_SESSION_ID` into a child (spinclass#169 /
  FDR-0017).
- **Presence index is information disclosure.** The presence index (§3.3)
  exposes per-instance keys, worktree labels, and descriptions to any local
  reader of the job state directory. It MUST be created with the same
  restrictive permissions (0700) as the rest of the job state tree (RFC-0009)
  and MUST NOT be transmitted off-host.
- **Clownfile is executable surface.** The `[attach]` argv templates (§1.3) are
  executed by clown. A clownfile discovered via the `$PWD`→`$HOME` ascent in an
  untrusted working directory could inject an attack argv. clown SHOULD apply
  the same trust expectations to a discovered clownfile as to a discovered
  `.circus/` directory, and MUST substitute only the defined placeholders
  (`{id}`, `{entry}`) — never shell-interpolate template strings.

## Conformance Testing

Conformance tests for this specification live in `zz-tests_bats/`.

Tests use binary injection via `bats-emo`:

    require_bin CLOWN clown

### Covered Requirements

| Requirement | Test File | Description |
|---|---|---|
| §2.1, MUST mint one per-instance id used for key + resume hint | `chat_identity.bats` | A launch's `CLOWN_SESSION_ID`, `--session-id`, and resume hint share one id |
| §2.3, routing key drops `SPINCLASS_SESSION_ID` | `chat_identity.bats` | With only `SPINCLASS_SESSION_ID` set, the routing key is generated/`CLAUDE_SESSION_ID`, not the spinclass value |
| §2.4, `whoami` reports key + group decoration | `chat_identity.bats` | `clown job whoami --json` carries the per-instance key and the `SPINCLASS_SESSION_ID` group channel |
| §3.2, group fan-out via derived channel | `chat_group.bats` | A message to a `SPINCLASS_SESSION_ID` target lands on `ChannelID(SPINCLASS_SESSION_ID)` and reaches every watcher of it |
| §1.1, clownfile cascade | `clownfile.bats` | A `$PWD`-closer clownfile overrides a `$HOME`-closer one per-key |
| §1.3, placeholder substitution | `clownfile.bats` | `{id}`/`{entry}` substitute; an unknown placeholder is rejected |

## Compatibility

This RFC extends RFC-0012 and amends RFC-0009 §2 (§2.3). Migration:

- **Addressing change for consumers.** Before this RFC, a directed wake/chat to a
  clown in a spinclass worktree addressed `ChannelID(SPINCLASS_SESSION_ID)`.
  After it, direct addressing uses the per-instance key (read via `whoami`),
  and `ChannelID(SPINCLASS_SESSION_ID)` becomes the *group* address. Consumers
  (spinclass, moxy) MUST migrate: use `whoami` for one clown, the decoration for
  the whole session. FDR-0017 specifies the spinclass side.
- **spinclass chat removal.** spinclass deletes `internal/chat`, the `chat-*`
  tools, and the `chatroom/` store; the RFC-0009-era assumption of a
  spinclass-local chat store is removed. FDR-0009 (spinclass) is superseded by
  FDR-0017 for the chat-store role.
- **profiles.toml.** The planned `--profile` / `profiles.toml` design folds into
  the clownfile `[profile]` table (§1.2); no separate profiles config ships.
- **`--profile`/clownfile rollout.** The clownfile is additive: absent
  clownfile ⇒ built-in defaults (§1.1), so existing launches are unaffected
  until a clownfile is introduced.

## References

Normative:

- [RFC 2119] Key words for use in RFCs to Indicate Requirement Levels.
- [RFC-0009] Job-Wakeup Channel (`docs/rfcs/0009-job-wakeup-channel.md`) — journal,
  `ChannelID`, session-key precedence §2, disable switch §8.
- [RFC-0011] Job-Platform MCP Tools
  (`docs/rfcs/0011-job-platform-mcp-tools.md`) — `job_message`, `job_read`.
- [RFC-0012] Session Identity & Job-Channel Ownership
  (`docs/rfcs/0012-session-identity-and-job-channel-ownership.md`) — `whoami`,
  identity resolution; extended by this RFC.
- [spinclass FDR-0017] clown⇆spinclass session-attach, grouping, and chat
  ownership rescope — the spinclass consumer view; cites this RFC by number.

Informative:

- clown#135 (directed-wake divergence + `whoami`), clown#136 (session-identity
  env hygiene), spinclass#169 (no-leak hygiene).
- `cmd/clown/resume_hint.go` — the existing per-launch resume-identifier minting
  reused by §2.1.

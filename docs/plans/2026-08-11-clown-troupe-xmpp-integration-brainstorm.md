# clown#213 — clown-side troupe-over-XMPP integration (design record)

Date: 2026-08-11
Status: **decided** (operator, 2026-08-11) — all forks resolved (incl. DM
addressing via option (d): per-session accounts + native 1:1). Consumer spec
filed as troupe#3; clown-side implementation in progress.
Issue: clown#213 (hub) · depends on troupe#2 (done, master `38fbdc4`) · precedes eng#290
Sibling: clown#215 (chat-record journal leak — resolved here, see §5.2)

This is the clown-side design record for the troupe-over-XMPP effort. troupe#2
built the plain-JID wire client; this record captures **what clown drives, how
wakes are preserved, and what of the old path becomes legacy**, and records the
operator's decisions on the forks the research surfaced.

## 1. The two troupe XMPP surfaces (grounded)

troupe today carries **two distinct XMPP paths**, confirmed against the troupe
git log and code:

### Path A — RFC-0001 transport-abstract (agent + journal)

- Surface: `troupe send/read/list/message`, `troupe agent`, `troupe mcp`,
  gated by `TROUPE_TRANSPORT=xmpp` (`cmd/troupe/transport.go` `selectTransport`).
- Addressing: **3-tier, mechanically derived from identity** (RFC-0001 §3):
  `<clown-name>@personal.<muc>`, `<group-id>@session.<muc>`,
  `broadcast@host.<muc>`. Needs `TROUPE_XMPP_MUC_DOMAIN`.
- Receive/wake: a persistent per-session **`troupe agent`** daemon joins the
  three rooms; on inbound it **writes a durable `TypeChat` journal record** +
  nudges the recipient's own channel so `ringmaster monitor` wakes (RFC-0001
  §6). `troupe read` **always reads the local journal** — transport is invisible
  at the read surface.
- Landed before clown's (former) pinned troupe. **This is what clown's
  `jobmonitor.go` wires today** (dormant — see §2).
- **Cost:** every inbound chat is a durable `TypeChat` record that never
  terminalizes → **this is clown#215.**

### Path B — plain-JID stateless (`troupe muc`)

- Surface: `troupe muc send/read`; library `internal/xmpp/room.go`:
  `ParseRoomJID`, `PublishToRoom`, `ReadRoom`, `RoomConfigFromEnv`.
- Addressing: **full plain-string room JID**, any room, no derivation.
  `RoomConfigFromEnv` reads `TROUPE_XMPP_*` but **not** `MUC_DOMAIN`.
- Stateless: connect → join one room → act → close. **No journal, no agent, no
  wake.** muc_mam (history-on-join) is the store.
- Landed as troupe#2 (`5773fd8`), live-validated against the canary test room
  (troupe#2's live proof).

## 2. clown's state (greenfield activation)

- clown's flake now pins troupe **`38fbdc4`** (bumped from `f824cef`, which
  predated path B). troupe is **binary-only** for clown (`go.mod` requires
  `ringmaster`, not troupe; flake references only `troupe.packages.*.troupe`),
  so the bump is low-risk — it makes path B available, changes nothing at
  runtime.
- The burned-in `default-clownfile` ships **no `[messaging]` table**, so
  `transport` defaults to `local`. clown's XMPP path is **dormant** — no
  shipping config activates path A. So this is **greenfield activation, not a
  live-path migration**; there are no path-A clown users to break.
- The `[messaging]` clownfile table + `TROUPE_XMPP_*` env export already exist
  (`internal/clownfile/clownfile.go`, `cmd/clown/main.go`), written for path A's
  env contract.

## 3. The core tension (and how the decision resolves it)

Path B's headline "no agent" is **fundamentally incompatible with push-wakes** —
a wake requires a persistent joined receiver, which path B removes. You cannot
have both "stateless" and "session woken on inbound chat." A quieter signal
agreed: the operator's **live rooms are plain-JID on one MUC domain**, not path
A's `personal./session./host.<muc>` topology.

**The operator's resolution:** treat path B's stateless client as the **wire
layer**, and make a **persistent per-session client the core deliverable** —
which is **evolving path A's `troupe agent`**: keep its persistent-join shape,
replace its addressing (→ plain JIDs) and its wake mechanism (→ ephemeral +
cursor catch-up). Statelessness was a property of troupe#2's *delivered* wire
client, not the end state.

## 4. Decision (operator, 2026-08-11)

Scope is **1b's architecture** — wakes are a MUST-HAVE. Decided requirements:

1. **Wakes.** DMs always wake; MUC @-mentions always wake; **every-MUC-message
   wake is a per-room policy** (config; default *mentions-only*). All-messages is
   a tuning lever, explicitly not a global decision.
2. **Ephemeral wakes.** Reap-on-delivery; **never a durable record per
   message.** This is the clown#215 structural fix, now on the wake path itself
   (not by absence of wakes).
3. **Catch-up (iOS-push analogy).** muc_mam is the durable store; the client
   keeps a **per-room read cursor** (O(rooms) local state, not O(messages));
   `chat_read` = pull-from-mam-since-cursor + advance. Wakes that fire carry a
   **queue summary** (per-room unread counts since cursors) so one wake tells the
   session everything it is behind on.
4. **Flow-through calls stand.** Old path-A wiring stays dormant/legacy in v1;
   the clownfile room-config surface is clown's to design; the troupe input bump
   is required (done, §2).

## 5. Resolutions of the research forks

### 5.1 Transport model
Native path B as the **wire layer** under a persistent receiver (the deliverable,
§6). Not the RFC-0016 slidge-gateway (option A there) — that RFC's recommended
(A) is superseded by this native path.

### 5.2 clown#215 fate
**Structurally fixed on the new path** (requirement 2: ephemeral reap-on-delivery
wake records; the message body is never journaled — it lives in muc_mam). On the
dormant **path A**, wontfix-with-rationale (superseded transport). clown#215 is
cross-referenced to this record and closes when the receiver lands.

### 5.3 Path A's fate in clown
Left **dormant/legacy** in v1 (already off by default). Not ripped out — troupe
still ships it; removing clown's side is churn. It stops being presented as *the*
XMPP path.

## 6. Persistent receiver interface (clown-side spec — draft)

The deliverable. A persistent per-session XMPP client (a clown-launched plugin),
one per troupe instance, evolving `troupe agent`: keep persistent-join +
auto-reconnect; replace addressing (plain-JID) and wake mechanism. The receiver +
cursor + mam-read live in **troupe** (the plugin binary); clown owns the launch,
the clownfile config surface, and consuming the wake via the existing ringmaster
monitor. **This section is the contract the troupe follow-on builds to.**

What clown needs, across **three interfaces** (filed as troupe#3):

**Interface 1 — Credential mint.** Each session mints its **own** XMPP account at
start via a local privileged (prosodyctl-backed) mint. Consumer contract:
`(session-key) → (JID, password-file)`; the account **localpart is derived from
the session key** so peers resolve DMs registry-free; resume-idempotent; revoked
on session reap. The receiver MAY mint on its own start (clown just launches it
with the key).

**Interface 2 — Persistent receiver (evolved `troupe agent`).**

1. **Launch/env.** clown registers it as a session monitor (as it does
   `troupe agent` today, `cmd/clown/jobmonitor.go`), scoped
   `CLOWN_SESSION_ID=<key>`, with `TROUPE_XMPP_*` + the minted `PASSWORD_FILE` + a
   room set + per-room wake policy (clownfile `[messaging]`). Session-lifetime,
   reconnect with backoff.
2. **Sources.** The configured **plain-JID MUC rooms** + the session's **own
   account 1:1 inbox** (native c2s; no join).
3. **Wake delivery (ephemeral).** On a should-wake inbound, write an **ephemeral**
   (`message`-type, reap-on-delivery) record onto the recipient's **own**
   ringmaster channel + nudge; the existing `ringmaster monitor` emits the wake.
   **No durable per-message record** (§5.2). The wake line carries a **queue
   summary** (per-source unread counts since cursors).
4. **Catch-up.** A per-source **read cursor** (muc_mam for rooms, the account's
   **user mam** for 1:1; O(rooms+1) state). `chat_read` pulls mam-since-cursor
   across sources, returns them, advances cursors; the receiver reads the **same**
   cursors for queue-summary counts. No journal reads.
5. **Wake policy.** DM (1:1) always · MUC @-mention of the session's clown-name
   always · plain MUC message only if that room's policy = `all` (default
   `mentions-only`).

**Interface 3 — Native 1:1 DMs.** A DM targets a session **key**; troupe resolves
key → JID (**derived localpart** + **host/vhost from the presence index**) and
sends `type=chat` over s2s, reusing troupe#2's `<troupe>` envelope; user-mam
archived; always wakes. Group/broadcast stay MUC-based.

**DM addressing — resolved (operator, option d).** The research fork (per-session
room vs MUC-PM vs @mention) is **dissolved** by per-session accounts + native
1:1: a DM is a private XMPP 1:1 between session JIDs — no rooms, **no
instant-room dependency**, no co-occupancy. Instant-room stays deferred. (Interim:
mention-in-a-shared-room is an acceptable pre-mint demo stopgap, but the built
design is (d).)

**session-key → JID resolution (decided 2026-08-11; revised after the worker's
build).** clown does **not** guarantee `CLOWN_SESSION_ID` is a valid XMPP
localpart — `cmd/clown/resume_hint.go decideClaudeSession` mints a UUID on the
normal path but passes an explicit `--session-id`/`--resume` value, or an
inherited non-UUID `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID` (case 5), through
**verbatim**. The resolution:

- **Canonical derivation lives in one place — `troupe derive-jid` (troupe#3).**
  clown never replicates the mint's sanitize path; `derive-jid` handles hostile
  keys, so the unenforced key format is never leaned on.
- **The presence index carries the peer's `host`/vhost** (an additive ringmaster
  presence-schema field — Q2, §8), and the JID is **derived** via
  `troupe derive-jid --session-key K --vhost <presence.vhost>`. Same-host DMs use
  the sender's own vhost (`TROUPE_XMPP_DOMAIN`) with no lookup; cross-host DMs
  read the peer's vhost from presence.
- **The `<troupe>` envelope's `From` carries the sender's full minted JID**
  (`cmd/troupe/dm.go`), so a reply-to-sender never derives.
- **No key-format enforcement in clown** — rejecting currently-accepted
  resume/override keys would be a compat break with no gain.

> **Revision note (operator-confirmed, 2026-08-11).** The earlier decision was
> "presence carries the **full minted JID verbatim**, no derivation." The
> worker's landed code instead **computes** the JID via the canonical
> `derive-jid` from a **vhost**-bearing presence field (`cmd/troupe/mint.go` §15,
> `dm.go`). Adopted: it preserves the decision's intent — never trust an
> unenforced key format; one canonical sanitize (`derive-jid` IS that path) —
> while materializing the JID at resolution rather than storing it.
> Built-and-validated beats decided-but-unbuilt when intent is preserved.

## 7. Plan & sequencing

Per the operator's sequencing (so clown isn't serialized behind troupe):

- **Troupe follow-on filed — troupe#3** ("Persistent per-session XMPP client —
  receiver + credential mint + native 1:1 DMs"), specifying all three §6
  interfaces from the consumer side. The operator spawns a troupe worker against
  it (live-test loop via circus's canary credential, as troupe#2).
- **Land independently (no dependency on the receiver):**
  - troupe input bump `f824cef → 38fbdc4` — **done** (§2).
  - the fixed-coordination-room post/read surface (parley replacement +
    circus's federation-smoke) — path B is now bundled (`troupe muc` on PATH);
    clown-side work is the clownfile room-config + surfacing.
  - the clownfile room-config surface, designed **against** §6.
  - clown#215 cross-referencing.
- **Merge this record** (operator wants design records landed).

## 8. Cross-repo dependencies / scope boundaries

- **Persistent-receiver follow-on** — troupe#3, filed by clown; the core troupe
  slice (receiver + mint + 1:1).
- **troupe input bump** — clown-side, done.
- **Instant-room lock** — **avoided.** Option (d) (native 1:1 DMs) needs no
  per-session rooms; the group rooms that matter pre-exist. Instant-room config
  stays deferred.

Boundaries the operator set with the decision (context, **not** clown's work):

- The per-host prosody **federation nixosModule** (circus's
  `prosody-federated.nix` lineage) eventually moves to troupe as an exported
  module with the mint beside it; circus keeps DNS/certs/placement/firewall. Its
  own later slice with a dual-architecture period — clown does not fold it in and
  does not design against its existence.
- **krone's bridge prosody** (slidge/Snikket/canary) stays circus-owned; krone
  participates in the mesh as-is.
- **circus FDR-0019** is revised for the ownership split by the operator, not here.

## 9. Live-test loop

The canary XMPP credential lives in circus's repo-local piggy store and does
**not** resolve from a clown worktree. Dev loop = unit/bats tests + a **ping to
circus/clear-walnut** to run the live round-trip (`just debug-troupe-muc-live`).
Never post to the operator's live alert room; use the designated canary test
room. (The concrete room/vhost JIDs are held out of this record deliberately —
they are circus/eng infra identifiers; get them from the operator dispatch or
circus config at test time.)

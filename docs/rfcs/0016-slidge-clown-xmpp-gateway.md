---
status: proposed
date: 2026-07-12
---

# slidge-clown: an XMPP gateway for clown sessions (troupe's XMPP backend)

## Abstract

This RFC specifies `slidge-clown`: a [slidge](https://slidge.im)-based XMPP gateway that puppets **clown sessions as XMPP contacts**, backed by the local ringmaster jobwake journal, so an ordinary XMPP account (the operator's `sasha@chat.linenisgreat.com`) can message a session **live** over federation, and so sessions become addressable across hosts. It realizes the XMPP backend that RFC-0015 §4 leaves unspecified, without changing the `troupe` CLI surface. It additionally exposes an **adhoc-command (XEP-0050) agent-management surface** — list/inspect/spawn/message/cancel sessions from a standard XMPP client (Cheogram, Gajim). Deployment/topology is specified in **circus FDR-0019**; this RFC owns the plugin and the backend contract.

## Introduction

troupe's message model is transport-abstract (RFC-0015 §3–§4): a subject (notification payload) + optional body (full payload), addressed by a **logical recipient resolved by the backend** — a session key or group id today; "a JID or MUC address under XMPP" tomorrow. The current backend is the RFC-0009 job channel (a local on-disk journal + UDS nudge), which is per-host and has no network identity. This RFC defines the XMPP backend as a slidge gateway component so the mapping from clown's identity model (RFC-0013 §2–§3) onto XMPP is direct:

| clown (RFC-0013) | slidge-clown / XMPP |
|---|---|
| per-instance key `CLOWN_SESSION_ID` (§2.1) | a **puppet contact JID** (DM one session) |
| group `SPINCLASS_SESSION_ID` / decoration (§3.2) | a **MUC** (the session group) |
| broadcast global channel (§3.2) | a broadcast MUC / gateway JID |
| presence index `{key, decoration, description, lastSeen}` (§3.3) | slidge **roster + presence** |
| `ChannelID(key)` derivation (§3.2) | gateway maps JID → key → channel (no registry) |

Because addressing is derivation-based (hash the key → channel) and presence is "discovery/display only, not authoritative for addressing" (RFC-0013 §3.3), the gateway is a thin, stateless-ish translator: it does not own a registry, it mirrors the journal + presence index it already reads.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. The gateway component

1. `slidge-clown` MUST run as an XEP-0114 external component on a prosody local to the clown host whose journal it serves (circus FDR-0019). Its component domain is `<host>.clown.<zone>` (illustrative; the exact scheme is deployment config).
2. The gateway's "legacy backend" is the **local ringmaster jobwake journal + presence index** under `$XDG_STATE_HOME/ringmaster/` — it logs into no external network. It therefore requires no credentials, no companion/QR linking, and no internet egress (a tighter isolation posture than the messaging bridges).
3. The gateway MUST derive channels itself using the same `ChannelID(key)` function ringmaster/troupe use (RFC-0013 §3.2), so a message it delivers is indistinguishable from a native `job_message` on the same channel.

### 2. Session ↔ JID mapping

1. Each live session in the presence index (RFC-0013 §3.3) MUST be exposed as a puppet **contact** whose JID encodes the per-instance key (`CLOWN_SESSION_ID`), e.g. `<key-or-slug>@<host>.clown.<zone>`. The contact's display name SHOULD be the spinclass `decoration` + `description` (the human-readable session name), so the roster reads like `circus/kind-laurel — "expand bridge to WhatsApp"` rather than a raw hash.
2. Presence: a session present in the index (fresh `lastSeen`) MUST show XMPP presence *available*; a pruned/stale record MUST show *unavailable*. The gateway drives roster presence from the index ticker.
3. Each distinct spinclass group (`SPINCLASS_SESSION_ID` / decoration) SHOULD be exposed as a **MUC** whose occupants are that group's live sessions, giving the operator a room per worktree/session-group.
4. A broadcast target (`*`) MAY be exposed as a dedicated all-sessions MUC or as a message to the gateway JID itself.
5. The mapping MUST be derivation/index-driven only — the gateway MUST NOT introduce a persistent JID↔session registry (consistent with RFC-0013 §3.2's "no registry, no enumeration").

### 3. The jobwake ↔ XMPP bridge (the RFC-0015 §4 backend)

1. **Inbound (XMPP → session):** an XMPP `<message>` to a session's puppet JID MUST be delivered onto that session's per-instance channel (`ChannelID(CLOWN_SESSION_ID)`) as a troupe message; a `<message>` to a group MUC MUST target the group channel (`ChannelID(SPINCLASS_SESSION_ID)`); the broadcast surface MUST target the global channel. The XMPP subject/thread (if any) maps to troupe's subject (the wake), the body to troupe's body — per RFC-0015 §3's transport-abstract unit.
2. **Outbound (session → XMPP):** a session's `chat_send`/`job_message` recorded on a watched channel MUST be emitted by the gateway as an XMPP `<message>` from the appropriate puppet JID / MUC to the addressed recipient (e.g. `sasha@chat.linenisgreat.com`), preserving subject→notification, body→full.
3. Self-echo suppression (RFC-0013 §3.2.3) MUST be preserved: a message the gateway itself injected MUST NOT be re-emitted back to its origin.
4. This bridge IS the RFC-0015 §4 XMPP backend for co-located sessions: from a session's perspective, `chat_send` to a peer that resolves to a puppet/remote JID is carried over XMPP, with the `troupe` CLI surface unchanged.

### 4. Adhoc agent-management commands (XEP-0050)

The gateway MUST expose an adhoc-command surface (rendered natively in Cheogram/Gajim) for fleet management, restricted to authorized JIDs (§Security). Candidate commands (v1 slice in **bold**):

- **`list-sessions`** — refresh/return the roster from the presence index (grouped by decoration, per RFC-0013 §3.3).
- **`session-status <jid>`** — job/liveness for a session (via `clown job status` / `clown presence`).
- **`message <jid> <text>`** — the chat-command form of §3.1 inbound.
- `spawn <repo> <brief>` — start a detached session (spinclass `spawn-session`, FDR-0006). High-authority; see Security.
- `cancel <job-id>` / `job-list` — ringmaster job control (clown#117 surface).
- `broadcast <text>` — message the all-sessions channel.

Command handlers SHOULD shell out to the existing `clown` / `ringmaster` / `spinclass` CLIs rather than reimplement their logic, so the gateway tracks those surfaces as they evolve (clown#117).

### 5. Cross-host — the open transport decision

Within a host, sessions already message each other via the local journal (unchanged). For **cross-host** session↔session and operator reach, two designs:

- **(A) Gateway-bridged (recommended for v1):** each host's gateway federates s2s; a message to a *remote* session's JID routes over federation to that host's gateway, which lands it on the remote journal. troupe stays journal-local per host; the gateway is the only thing that crosses hosts. Reuses the slidge/prosody investment; no change to the troupe substrate.
- **(B) Native troupe XMPP transport:** troupe itself speaks XMPP for non-local recipients (the "pure" RFC-0015 §4 swap). More invasive to `internal/jobwake`; makes the gateway redundant for session↔session (still needed for the operator roster/MUC/commands).

This RFC RECOMMENDS **(A)** for v1: it delivers the operator goal (sasha ↔ sessions, cross-host visibility) and the agent-management surface with the least change to the job substrate, and (B) can be layered later without changing the troupe CLI.

## Security Considerations

1. **Control authority.** The adhoc commands can *act on the fleet* (spawn, cancel, message-as). The gateway MUST authorize only configured admin JIDs (the operator's `sasha@chat.linenisgreat.com`); all other JIDs MUST be refused for command execution and SHOULD be refused for messaging. A federated component accepting control commands is a high-value target; the single-user trust model bounds it but does not remove the need for JID authorization on every command.
2. **Co-tenancy (circus FDR-0019 / FDR-0003 isolation).** The gateway container reads the local journal and dials its prosody component port only; it MUST NOT reach other host services or the tailnets. Because it logs into nothing external, its egress can be *deny-all beyond the prosody component + journal* — tighter than the messaging bridges (no internet :443).
3. **Journal integrity.** The gateway writes derived channels on behalf of an XMPP sender; it MUST NOT let a message forge a different session's *origin* on the journal beyond what troupe itself permits (RFC-0012 channel ownership).
4. **Presence exposure.** The roster reveals live session decorations/descriptions to the operator's federated server (Snikket). Acceptable single-user; note it crosses to Snikket over the (TLS) s2s link.

## Open Questions

- JID slug scheme: raw per-instance key vs a stable human slug derived from the decoration (collisions across resumed instances?).
- MUC lifecycle for ephemeral session groups (create/destroy vs persistent rooms).
- Whether `spawn` (a session that *creates* sessions) belongs in v1 given the control-authority surface, or should be gated behind a stronger check.
- Relationship to clown#117's job-DAG: should `job-list`/`cancel`/`--after` sequencing be exposed as adhoc commands, making Cheogram a fleet scheduler UI?

## References

Normative:

- clown RFC-0015 §3–§4 (troupe message model + transport abstraction).
- clown RFC-0013 §2–§3 (per-instance identity, derived-channel addressing, presence index).
- clown RFC-0009 (job-wakeup channel — the current backend).
- RFC 6120 (XMPP core), XEP-0114 (components), XEP-0045 (MUC), XEP-0050 (adhoc).

Informative:

- circus FDR-0019 (deployment/topology/federation of this gateway).
- circus FDR-0003 (the slidge platform + `slidgePlugins` this reuses).
- clown#117 (clown as the complete job/meta-harness system).

# Handoff, 2026-11-01: clown#333 (policy-broker socket)

Labels: **V** verified, **R** result (what exists), **I** open or unverified.
Written by clown/rare-walnut/pennywise at the coordinator's request.

## What d1ea34a0 changed

- **R** clown names a per-session policy-broker socket
  (`<runtime>/clown/serve/<principal>/broker.sock`, beside the serve socket)
  and passes the path to the spinclass serve bridge in its environment as
  `CLOWN_POLICY_BROKER`. Only the spinclass serve is told. The session
  directory is created 0700 for serve to bind in; clown removes the socket and
  directory at exit. `cmd/clown/brokersocket.go` holds the routing, export and
  cleanup; `BrokerSocketPath` is in `internal/0/peercheck` (facade regenerated),
  `BrokerPort` (`ServePortBase`+1) in `internal/alfa/agentscope`.
- **R** once the serve has started (and the session still binds it) the agent
  gets `CLOWN_POLICY_BROKER`:
  - unconfined: the socket's absolute path;
  - confined: `127.0.0.1:19001`, the in-unit relay. The socket is bound into
    the unit as a single inode (`BindPaths=`) and relayed by `clown
    agent-relay`; every in-unit server (moxy included) gets the same value.
  - Consumers tell the two apart by a leading `/`. The session key is not
    exported; moxy takes it from the broker hello (agreed with moxy#491).
- **R** skips, none fatal: no runtime dir, no spinclass serve, serve did not
  start, or (confined) serve created no socket. The session then launches as
  before and moxy uses its static floor. The tent executor is never given the
  variable (it refuses any Env).
- **R** FDR 0020 has a paragraph on it. No handshake change, no new capability.

## Verified, and not

- **V** unit tests (routing, export both ways, relay row and bind, skips,
  path rules) passed in the merge gate; master d1ea34a0c4ff.
- **I** NOT deployed. Not run in a live confined unit, and not against a real
  spinclass serve; `just verify-agent-scope` was not run for #333.
- **I** Not demonstrated: that serve creates the socket before its health
  check passes (the confined relay row needs the file to exist when the unit
  is launched). If it does not, the confined skip fires silently (a Warn in
  the log) and the agent sees no broker.
- **I** No red-first run was done for the tests.

## What the next lock bump must carry

This is my reading, not something I checked in the consumers' locks:
- clown at d1ea34a0c4ff or later wherever the deployed clown comes from, with
  a rebuild and a redeploy (nothing here is running until then);
- a spinclass whose serve reads `CLOWN_POLICY_BROKER` and listens there
  (spinclass#438, light-cedar's lane), and a moxy that dials it (moxy#491).
  Without those, clown's half is inert: the variable is set and nothing
  listens.

## Left open

- A stale `broker.sock` after a crash is left for serve to replace; clown only
  removes it on a clean exit.
- Live proof of both the unconfined and the confined path, once spinclass and
  moxy land their halves.
- Unrelated carry-overs from the earlier handoff are unchanged: the operator's
  decision on revoking the throwaway XMPP accounts, and the capabilities-list
  design (no ruling).

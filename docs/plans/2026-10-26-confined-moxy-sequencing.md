# Confined workers and moxy: the smallest slice, owners, order (design, 2026-10-26)

Design note, no code. For the operator to discuss. Labels: **R** read in code
or docs, **V** observed this session, **I** inferred. Sources: clown
`cmd/clown/main.go` (frontend-class servers), `internal/agentscope/inunit.go`,
`internal/pluginhost/config.go`, FDR 0020; spinclass `confineprofile.go`;
circus `docs/plans/2026-10-26-local-issue-filer-moxin.md`; clown#325, spinclass
#383 and #301; moxy man pages.

## The question

A confined worker should be able to file an issue (`issue-filer_file`, and
ideally smith `issue-create` / `issue-list` as a stopgap) through moxy. Operator
rulings today: no claude.ai connector in any worker profile; issue-filer goes
through moxy; the call must not put the Cloudflare tunnel token (a file under
`$XDG_STATE_HOME/circus`) where the unit can read it.

## Facts that shape the answer

- **The unit keeps the operator's uid** and is separated by mount namespace
  (FDR 0035, R, via the circus note). Whatever runs in the unit can read
  whatever is bound in. So a tool that needs the token must run **outside** the
  unit, or the token must never be needed there.
- **Two classes of MCP server under `--agent-scope` (R, `main.go:2608-2667`):**
  a server declared `"scope":"frontend"` in a plugin's `clown.json` runs in the
  frontend and is forwarded into the unit (loopback address or unix socket,
  `frontendServerAddrs`); every other server is started inside the unit by
  `reserveInUnitServers`. Ringmaster, troupe and spinclass serve are
  frontend-class. Forwarding needs no spinclass spec change: clown computes the
  forwarded set from the servers it started (I; the #325 text says spinclass
  lists it, which I could not confirm in code).
- **The in-unit moxy is a fixed moxyfile (R, `agentscope.InUnitMoxyfile`):**
  `allow-tools` = folio, grit, rg, jq, man, chix, env, batch, async, status,
  restart; `disable-moxins` = smith, chrest, calendar, car, gmail, gws, piers,
  prison, freud, stats-me. `issue-filer` is on neither list, so `allow-tools`
  keeps it out. It would also run in the unit, where the token is.
- **Spinclass's confined rule (R, `checkConfinedToolSurface`):** the profile
  must drop `moxy/moxy` whole and keep `just-us/just-us`. It names moxy only;
  a profile may keep any other server. The reason it forbids moxy even with
  per-tool exclusions is that exclusions are policy, not a boundary (the unit
  can reach `/clown/exclude-tools` on the serve socket: clown#301, open).
- **`moxy serve-moxin --name N` (R, man page) serves one moxin standalone, but
  looks the name up in the *builtin system* moxins.** issue-filer is a
  workstation moxin on `MOXIN_PATH`, so whether `serve-moxin` finds it is
  unverified (I: probably not). `moxy serve-mcp` with a moxyfile is the
  documented way to expose a chosen set of moxins from `MOXIN_PATH`, and
  `allow-tools` limits it (I: to be confirmed by a run with moxy's session).
- **Permission under dontAsk (R/V):** a confined worker runs dontAsk, so a tool
  call must be allowed by the sweatfile's `claude.allow`. Plugin MCP tools are
  named `mcp__plugin_<plugin>_<server>__<tool>` (V: e.g.
  `mcp__plugin_clown-builtin-jobs_ringmaster__job_cancel` in this session).

## The options

| # | Shape | Token in unit? | Owner | Verdict |
|---|---|---|---|---|
| A | **Frontend-class issue-filer.** clown synthesizes a builtin plugin (like `clown-builtin-jobs`) with one frontend-scope stdio server running a moxy that exposes only the `issue-filer` moxin; forwarded into the unit like ringmaster. The HTTPS call leaves from the frontend. | No | clown (server, tests, profile), spinclass (allow rule, bind probe), circus (moxin, token) | **Recommended.** Matches the circus design (a) and clown#325. |
| B | Allow `issue-filer` in the in-unit moxyfile. | Yes: the unit would need the token file or a header from outside | clown | Rejected: the unit reads the operator's uid's files that are bound in; a bind of `$XDG_STATE_HOME/circus` is exactly what the circus note forbids. |
| B2 | In-unit moxin plus a frontend broker that adds the headers to one fixed request. | No | clown + circus | A second bespoke proxy; only if A is refused. |
| C | spinclass#383 per-tool add-back in the profile (keep moxy, only these tools). | Yes (still the in-unit moxy) | spinclass + clown | Does not solve the token. Useful later for read-only tools, but blocked on #301 and on relaxing the "no moxy" rule. |
| D | claude.ai connector (Krone). | No | none | Ruled out by the operator today. |

## Smith as the stopgap (issue-create / issue-list)

A frontend-class smith would act with **the operator's forge credentials** on
any repo: `issue-create` and, if not removed, `pr-merge` and releases. It has no
reviewer step, unlike the filer graph (a model drafts, a reviewer approves).
Recommendation: do **not** forward smith for writes. If a read path is wanted
(`issue-list`, `issue-get`), do it as a second frontend moxy instance whose
moxyfile allows read tools only, after confirming that moxy's `allow-tools`
works per tool and not only per moxin (I: unverified; FDR 0020 and the
in-unit moxyfile only list moxin names). Until then a worker without a filing
path writes a pickup note, as today.

## Smallest slice that gives a confined worker `issue-filer_file`

1. **circus (done by the coordinator's report, not re-read):** the moxin and the
   provision check merged. The operator provisions the token on the host (card).
   Prerequisite for anything live.
2. **clown (about a day, test-first):** a synthesized builtin plugin
   `clown-builtin-issue-filer` with one `scope: "frontend"` stdio server that
   runs `moxy serve-mcp` over a clown-written frontend moxyfile
   (`allow-tools = ["issue-filer"]`, everything else disabled, `MOXIN_PATH` from
   the operator's environment), started only under `--agent-scope`, when the
   moxin is discoverable and the token file exists (a stat, never a read).
   Acceptance, from clown#325: a fake moxin shows the unit lists and calls the
   tool through the forward, and the token path is not readable from inside.
3. **clown (same change):** add the new server to the `worker` builtin's
   `context_servers`. A saved server a launch does not discover is skipped, so
   the profile still launches on a host without it; the digest of `worker`
   changes, which is harmless today because no session is pinned to it. It
   stays a non-moxy server, so the confined rule still passes.
4. **spinclass (small):** the sweatfile `[claude] allow` names the tool (the
   exact name comes from a live listing: it is
   `mcp__plugin_clown-builtin-issue-filer_issue-filer__<tool>` if the server is
   named `issue-filer`); keep `$XDG_STATE_HOME/circus` out of every unit spec
   and add it to the bind probe (circus flagged this to firm-mulberry); a test
   that a confined spawn with `worker` is accepted after step 3.
5. **Live check (operator or a PTY):** a confined `worker` calls the tool; the
   wake DM arrives; a `cat` of the token path from the unit fails.

Egress: none for A (the call leaves the frontend; `mcp.starbrandshoes.com` is
already on the default unit allowlist for other reasons, R `DefaultEgressAllow`).

## Risks to state to the operator

- The tool takes caller-chosen text, `repo_hint` and `notify` (a JID): a worker
  can file into any repo the filer graph accepts and aim the wake DM at any
  JID. The graph's reviewer is the control; there is no per-session rate limit
  (I). A per-session call cap in the frontend wrapper is a cheap addition.
- The shared `agents` token grants the whole `mcp` app, not only this tool
  (circus note). Holding it frontend-side keeps it out of the unit but the
  frontend moxy, run as the operator, can read it: the frontend moxyfile must
  allow only `issue-filer`.
- Forwarding is a new frontend-class server, so FDR 0020's "what a confined
  worker loses" table and the unit capability list need a line, and
  `RequiredServers` (the library's list of servers a launcher requires) must
  not change.

## Order, owners, rough size

| Step | Owner | Size | Blocked on |
|---|---|---|---|
| Token provisioned on the host | operator at the card | minutes | card |
| Confirm `serve-mcp` + `allow-tools` + `MOXIN_PATH` expose only the moxin; per-tool allow? | moxy's session | an hour | none |
| Frontend-class `issue-filer` plugin + `worker` keep-list + tests | clown | about 1 day | the moxy answer |
| `claude.allow` entry, bind probe, spawn acceptance test | spinclass | a few hours | clown step |
| Live confined run | operator or PTY session | an hour | all above |
| Read-only smith frontend instance | clown + moxy | later, 1 day | per-tool allow in moxy |
| In-unit read-only moxy tools via profile (#383, #301, relaxing the moxy rule) | spinclass + clown | days; low value | native Read/Grep/Glob already cover rg/folio_read/jq |

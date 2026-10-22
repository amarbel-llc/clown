# Audit: profiles docs vs code (grand-hickory @ cf0aefd)

Read-only. The paths in the Evidence column are relative to the worktree. "no test" means I found no unit or bats test for that claim. zz-tests_bats has no test for exclusions, readback or `--profile`. Facts the caller supplied that I could not reproduce here (the live rounds K and P) are marked "caller-reported".

Commit attribution: the change that makes a profile refuse the launch on unconfirmed exclusions is **75fe63f**. 4f9ae5e is the follow-up that stops the in-unit relay from hanging when it refuses (it closes the ready pipe on exec).

## 1. docs/plans/2026-04-23-profiles-design.md

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| D1 | :7-10 | Merge two layers at startup and validate the selected combo | DIVERGES (partly) | `--profile`, clownfile pin and build default are validated (main.go:469-489). A profile chosen in the picker, or created through "+ add profile…", is **not** validated (main.go:491-515). | correctness | Call `profile.Validate(*selectedProfile)` after the picker block |
| D2 | :12-14 | Additive: `--provider` and `CLOWN_PROVIDER` work unchanged | MET | main.go:2937-2944, 2974-2983; test main_test.go:1097 | | |
| D3 | :20-33 | Schema: name, display, provider, backend, model, url, token | MET | profile.go:13-21 (now also env and context_*); test profile_test.go:15 | | |
| D4 | :26-27, :35-46 | Valid matrix has 5 pairs; any other pair is rejected | SUPERSEDED | profile.go:150-155: claude+gateway is valid, crush and openrouter were added, and gateway needs url or model (profile.go:165-199) | doc-only | Point to `profile.Backends` and the openrouter and juggler designs |
| D5 | :45-46, :111 | An invalid combo prints the table of valid combos | DIVERGES | Only the Validate error is printed, naming the providers (profile.go:160-163; main.go:484-486) | doc-only | Edit the doc |
| D6 | :52 | claude/anthropic: `--model` is forwarded | MET | main.go:940-952; test main_test.go:276 | | |
| D7 | :53 | claude/local: env set through the circus handshake | SUPERSEDED | Resolved through juggler: main.go:970-988; test main_test.go:541 | doc-only | Say juggler |
| D8 | :54-56 | opencode: model goes in opencode.json; gateway url and token come from the profile | MET | opencode.go:213, 341-346, 362-363; tests opencode_test.go:182, 334 | | |
| D9 | :56, :113 | opencode/local gets its baseURL from the circus portfile | SUPERSEDED | It reads the juggler portfile `~/.local/state/juggler/llama-server.port` (opencode.go:283-300, 364-372) | doc-only | Edit the doc |
| D10 | :62-64 | `profiles/builtin.toml` is embedded in cmd/clown | DIVERGES | The file is `internal/profile/builtin.toml`, embedded by internal/profile (profile.go:90-91) | doc-only | Edit the path |
| D11 | :65 | Builtins hold no URLs or tokens | MET | builtin.toml:1-257 has no url or token key; no test | doc-only | Add a test that every builtin has empty URL and Token |
| D12 | :66-70 | Minimum set ships with claude-sonnet-4-6; local requires circus | DIVERGES | The model is claude-sonnet-5-5 (builtin.toml:6, 20, 34); local uses juggler | doc-only | Edit the doc |
| D13 | :72-74 | User file is `~/.config/circus/profiles.toml` | SUPERSEDED | `$XDG_CONFIG_HOME/clown/profiles.toml`, with the legacy `~/.config/juggler` path as fallback (userpath.go:29-41; configpath.go:17-18). The circus path is never read. | doc-only | Edit the doc |
| D14 | :75 | A missing user file is not an error | MET | main.go:60-63; test main_test.go:1006 | | |
| D15 | :76 | A user entry with a duplicate name overrides the builtin | MET | profile.go:133-148; test profile_test.go:62 | | |
| D16 | :84-86 | `--profile` selects and skips the picker | MET | main.go:469-489, 491; test main_test.go:1053 | | |
| D17 | :90-92 | `CLOWN_PROFILE` ranks below the flag | MET | Env is read first (main.go:2945), then the flag overwrites it (2984-2991). No test covers env and flag together (only main_test.go:1072). | | Optional test |
| D18 | :95-98 | Picker runs when no profile is set and stdout is a TTY; builtins listed first | DIVERGES | The code checks **stdin** (main.go:491). An explicit `--provider`, a clownfile pin or the build default also suppress the picker (main.go:413, 491). A "+ add profile…" row is appended (main.go:111-118). Builtin-first order holds (profile.go:133-148). | doc-only | Edit the doc |
| D19 | :101-104 | `--provider claude` infers a (claude, anthropic) profile; a bad combo from flags errors | DIVERGES | No profile is inferred: an explicit provider suppresses every profile default (main.go:897-903). There is no API-backend flag; `flags.backend` is the tent backend (main.go:926-927). | doc-only | Edit the doc |
| D20 | :110 | Unknown `--profile` lists the available profiles and exits 1 | MET | main.go:476-482; no test | | |
| D21 | :112 | Gateway with no url or token prints the config path and exits 1 | DIVERGES | The error has no path (profile.go:193-197; main.go:485) | doc-only | Add the path to the error, or edit the doc |

## 2. docs/plans/2026-04-23-profiles.md (claims about resulting behaviour only)

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| P1 | :7 | `runOpencode` takes a profile instead of only opencode.toml | MET | opencode.go:353-401; test opencode_test.go:334 | | |
| P2 | :202-205 | `validCombos` has claude {anthropic, local} | SUPERSEDED | profile.go:150-155 | doc-only | Add a "superseded" banner to the plan |
| P3 | :344 | `//go:embed ../../profiles/builtin.toml` | DIVERGES | profile.go:90 (`go:embed` cannot reference `..` anyway) | doc-only | Banner |
| P4 | :365 | User path is `~/.config/circus` | SUPERSEDED | See D13 | doc-only | Banner |
| P5 | :499-504 | Unknown name prints a list; invalid exits 1; provider is overridden | MET | main.go:476-488 | | |
| P6 | :590-610 | Local uses the circus portfile with token "local"; fallback model is "gpt-4o" | DIVERGES | Juggler portfile (opencode.go:288). Token "local" holds (opencode.go:371). The fallback model is `prof.Model` or empty, not gpt-4o (opencode.go:397-400). | doc-only | Banner |
| P7 | :749-760 | Picker gate is stdin TTY, !version, !naked; quitting returns 0 | MET | main.go:491 adds `!providerExplicit`; quit at 505-507. The picker itself has no test, but its items do (main_test.go:1138). | | |

## 3. docs/plans/2026-07-08-cheap-context-profile-persistence.md

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| C1 | :8-10, :57-69 | `ContextServers` and `ContextExcluded` fields with omitempty TOML tags | MET | profile.go:38-39; tests store_test.go:36, :55 | | |
| C2 | :10-13, :99-107 | A profile with non-nil ContextServers activates replay without `--cheap-context` | MET | cheapcontext.go:159-161; main.go:2032, 2100; test cheapcontext_test.go:91 | | |
| C3 | :15-18 | A profile with no selection behaves as before; the flag alone still shows the picker | MET | cheapcontext.go:298-304; test cheapcontext_test.go:155 | | |
| C4 | :62-66 | ContextServers is the **exact** keep-set; everything else is dropped | DIVERGES | If nothing in the keep-set is discovered or started, `discovered` ends up empty and the launch goes to `runUnbound` (main.go:2111-2113; also main.go:2079-2082, which runs before the selection is applied). `runUnbound` hands claude the **original** plugin dirs (pluginbinding.go:87-90), which by pluginbinding.go:100-106's own account still declare their MCP servers. The profile fails open to the full set. No test. | **security** | Under a non-nil `cheapContextProfile.ContextServers`, refuse instead of `runUnbound`, or bind with every server dir dropped |
| C5 | :64-66 | A saved server or tool absent from the live catalog is silently skipped | SUPERSEDED (tools) / MET (servers) | Servers: cheapcontext.go:184-188; test cheapcontext_test.go:205. Tools under replay: since 75fe63f, zero matches refuse the launch (exclusionreadback.go:60-63) and so does a readback that still lists a tool (:83-85). Partial unmatched names are tolerated (see the top issues). The comment at profile.go:32-37 ("best-effort … not fatal") is stale. | doc-only | Update the doc and the profile.go comment to cite FDR 0020 §578-586 |
| C6 | :73-82 | Validate rejects a non-nil empty slice | MET | profile.go:209-211; test profile_test.go:244 | | |
| C7 | :74-77 | Reason given: BurntSushi omitempty drops empty slices on write | DIVERGES | `Save` now encodes through tommy (store.go:10, 29-35, clown#238). Whether tommy drops them is untested; the guard still holds. | doc-only | Restate the reason in an encoder-neutral way |
| C8 | :78-80 | The save prompt refuses an empty selection before any prompt | MET | cheapcontext.go:631-640; test cheapcontext_test.go:130 | | |
| C9 | :87-90 | The resolved profile travels through `parsedFlags.cheapContextProfile` to `runManaged` | MET | main.go:531, 2102 | | |
| C10 | :92-97 | `selectionFromSavedProfile` filters against live catalogs and needs no TTY | MET | cheapcontext.go:173-212, 297-301; tests cheapcontext_test.go:137, :220. Under replay the frontend actually judges the raw saved lists (cheapcontext.go:494-515), not `result.excludedTools`. | | |
| C11 | :111-119 | The save prompt runs only after a real picker confirmation; a new name creates a claude/anthropic profile; it reuses `saveProfileForm` | MET | cheapcontext.go:298-301 and 364-372 return before it; 397 calls it; 683-691 builds the new profile; 700-703 saves. no test (needs a TTY). | | |
| C12 | :121-130 | `profile edit` preserves env and context fields | MET | profileform.go:24-67; test profileform_test.go:192 | | |
| C13 | :132-136 | `profile list` has a CONTEXT column (`N server(s)` or `-`) | MET | profilecmd.go:62, 71-74; test profilecmd_test.go:47 | | |
| C14 | :162-164 | clown#175 still governs native httpServers per-tool filtering; "unrelated to persistence" | SUPERSEDED (stale) | moxy implements `/clown/exclude-tools` and clown pushes to it (cheapcontext.go:418-421, 579-588). Under a profile the push is read back, and a 404 refuses the launch (exclusionreadback.go:64-66). Persistence now behaves *differently* from the live picker, which is best-effort (cheapcontext.go:516-535). clown#175 is still Open on the forge. | doc-only | Edit the doc. Re-scope #175 to "native servers without the route", or close it citing moxy#399. |
| C15 | :165-167 | clown#178 (re-reading profiles.toml) is tracked, not fixed | SUPERSEDED | Fixed: `saveCtx` carries the data loaded once (main.go:462-466; cheapcontext.go:627-629, 656) | doc-only | Edit the doc |

## 4. docs/plans/2026-10-22-context-floor.md

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| F1 | :11 | `CLOWN_DISABLE_CLAUDEAI_CONNECTORS=1` sets `disableClaudeAiConnectors` and `ENABLE_CLAUDEAI_MCP_SERVERS=false`; an explicit operator value wins | MET | claudesettings.go:125-130; test claudesettings_test.go:69-95 | | |
| F2 | :12 | `CLOWN_DISALLOWED_TOOLS` entries come after the global set, deduplicated | MET | claudesettings.go:45-53; provider/claude.go:64-71; main.go:1301, 2437; tests args_test.go:25, claudesettings_test.go:39 | | |
| F3 | :14-15 | The digest already covers env, so no protocol bump is needed | MET | lookup.go:345, 364-366; test lookup_test.go:236 | | |
| F4 | :19-22 | reviewer, porter, canary and canary-grit are claude/anthropic/claude-sonnet-5-5 with a saved selection and both knobs on | MET | builtin.toml:52-257; test builtin_templates_test.go:47-119 | | |
| F5 | :20-21 | Spinclass exclusions "for porter" | DIVERGES (incomplete) | canary-grit also carries spinclass and just-us exclusions (builtin.toml:218-223) | doc-only | Edit the doc |
| F6 | :21-23 | Lists come from moxy's 156-tool catalog; a test checks keep and exclude partition it | UNTESTED | The test only checks the two sets are disjoint and that their sizes add up to a hard-coded 156 (builtin_templates_test.go:16, 86-100). Nothing compares the names with a real moxy catalog. Caller-reported round P: the readback showed moxy saved=152 matched=63, so 89 canary-grit names matched nothing live. | correctness | Check in a moxy `tools/list` fixture and assert keep ∪ exclude equals it, comparing claude-safe forms |
| F7 | :27-30 | The hook judges the first response: the gauge always, and the counter plus a systemMessage when over the threshold | MET | toolcost.go:504-511, 187-202; hook main.go:191-193; tests floor_test.go:29, 54, 114. `CLOWN_DISABLE_STATSD=1` turns everything off, the gauge included (hook main.go:111-113). | doc-only (nit) | Mention the statsd off switch |
| F8 | :30 | `CLOWN_CONTEXT_FLOOR_PCT` defaults to 60 | MET | toolcost.go:171-180. The tests pass 60 explicitly (floor_test.go:27), so the zero-to-60 default has no test. | | Optional test |
| F9 | :32-38 | No default window; an unknown window judges nothing; the gauge still goes out | MET | toolcost.go:176-181; test floor_test.go:89 | | |
| F10 | :35-36 | clown sets `CLOWN_CONTEXT_WINDOW=1000000` when `--model` ends in `[1m]`; the operator's value wins | MET (unit) / UNTESTED (e2e) | claudesettings.go:20-25, 110-114; main.go:1304; test claudesettings_test.go:120. That claude passes the settings env to hooks is claimed (claudesettings.go:94-98) but not tested end to end. | correctness (low) | Add a bats test with a stub claude that runs the hook |
| F11 | :39-41 | `CLOWN_DISABLE_CONTEXT_FLOOR_WARNING=1` silences the counter and the warning | MET | hook main.go:176; toolcost.go:181; test floor_test.go:90 | | |
| F12 | :41 | A model set through `ANTHROPIC_MODEL` or settings is not seen | MET | Only `--model` is read (main.go:1304, 2440); no test | | |
| F13 | :43-44 | No profile disables auto-compact by default | MET | Neither builtin.toml nor any non-doc code mentions `DISABLE_AUTO_COMPACT` (rg); no test | | |
| F14 | :50-51 | `just explore-claude-connector-switches` exists | MET | justfile:2179-2182 | | |
| F15 | :52-55 | First-class keys need a protocol bump; an older library fails closed | MET for the library (lookup.go:276-284). **The clown binary does not check skew**: it loads through `profile.Load`, which drops the stamp and unknown keys (main.go:45-71; profile.go:103-109). An older clown binary would silently ignore a future `disable_connectors`. | security (latent) | Before shipping protocol 2, make the binary refuse, or at least warn, when `Undecoded` is non-empty or the protocol differs |

## 5. pkg/profilelookup/lookup.go package doc

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| L1 | :2-5 | Resolves `--profile NAME` the way the binary does (builtins under the user file) | MET | lookup.go:223-227 matches main.go:461, 470-475; tests lookup_test.go:77, :96. The binary does not check skew (F15), so it launches registries the library refuses; that failure is in the safe direction. | | |
| L2 | :10-12 | Resolved carries identity and never the token | MET | lookup.go:186-203, 241-252; test lookup_test.go:289 | | |
| L3 | :13-15 | `RequiredServers` and `Selection.Missing` | MET | lookup.go:84-86, 154-164; test lookup_test.go:149 | | |
| L4 | :16 | A registry the build cannot vouch for fails closed with `*SkewError` | MET | lookup.go:276-284; tests lookup_test.go:178, 194, 212 | | |
| L5 | :20-22 | ProtocolVersion covers schema, stamp and canonical form | MET (constant) / UNTESTED (process) | profile.go:42-46; lookup.go:74. Nothing ties new `Profile` fields to the `canonical` struct. | correctness | Add a reflection test: every `profile.Profile` toml tag maps to a `canonical` json key |
| L6 | :29-30 | Fixed key order | MET | lookup.go:336-348; golden test lookup_test.go:236 | | |
| L7 | :31-32 | Every key present; unset string is ""; protocol is an integer | MET | test lookup_test.go:247 | | |
| L8 | :33-35 | Token never present, only `token_set`; rotating it keeps the digest | MET | lookup.go:360; tests lookup_test.go:236, 247, 253 | | |
| L9 | :36-37 | env and context_excluded have bytewise-sorted keys and render `{}` when unset; tool lists sorted and deduplicated | MET | lookup.go:361-366, 317-331; tests :236, :247. Undocumented: a server whose tool list is empty is **dropped** from context_excluded (lookup.go:320-322). | doc-only | Document it |
| L10 | :38-40 | context_servers is sorted and deduplicated, or null, never `[]` | DIVERGES (edge) | `Resolve` is protected by Validate (lookup.go:232). The exported `Canonicalize` is not: for a non-nil empty slice, `slices.Clone` keeps it non-nil and the output is `[]` (lookup.go:367-369). no test | correctness (low) | In `Canonicalize`, turn a zero-length slice into nil, or call Validate |
| L11 | :41-42 | `<`, `>`, `&` written literally; no whitespace; no trailing newline | MET | lookup.go:370-376; test lookup_test.go:236 | | |
| L12 | :43-46 | `${ENV}` is not expanded; a literal secret in env or url does appear | MET | lookup.go:351-366; test :236 (`${HOME}`) | | |
| L13 | :48 | Digest is lowercase hex sha256 | MET | lookup.go:240, 250; test :284 | | |
| L14 | :51-53 | Known limit: builtins drift only when the launcher re-links | MET | profile.go:90-91 (embedded) | | |
| L15 | :123-129 | Zero Options reads the same file as the binary | MET | lookup.go:259-267, and configpath.go:17-18: both call `userpath.ConfigFile`; no test | | |

## 6. AGENTS.md bullet on pkg/profilelookup (AGENTS.md:31-47)

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| A1 | :31-32 | It is clown's one public Go API | MET | pkg/ contains only profilelookup | | |
| A2 | :32-36 | Resolves builtin plus user, returns the keep-set and digest, fails closed on another protocol | MET | See L1, L3, L4, L13 | | |
| A3 | :36-39 | Changing the JSON, stamp or schema means bumping ProtocolVersion | UNTESTED | Only the golden test protects the JSON (lookup_test.go:227-240); nothing protects the schema | correctness | Same test as L5 |
| A4 | :40-41 | Every `profile.Save` stamps `[registry]` | MET | store.go:34; test store_test.go:115 | | |
| A5 | :41-42 | A new profile field needs a field in `canonical` | MET today (all 10 fields covered: profile.go:13-39 against lookup.go:336-348) / UNTESTED going forward | | correctness | Same test as L5 |
| A6 | :42-45 | `go-pkgs` published through mkGoPkgs; builtin.toml rides in extras | MET | flake.nix:322-332 | | |
| A7 | :45-47 | `RequiredServers` names servers the jobs plugin declares; a test pins it | MET | test jobmonitor_test.go:341-352 | | |

## 7. docs/features/0005-hats.md (status: exploring)

| # | doc:line | Statement (short) | Status | Evidence | Severity | Minimal fix |
|---|---|---|---|---|---|---|
| H1 | :6-14 | Hats: named narrowing of plugins and fragments | NOT BUILT as "hats" | rg finds no `--hat` and no `hats/` outside docs | | |
| H2 | :38-44 | Provisional: runtime selection from a build-time superset, user config, one active hat, plugins plus fragments | PARTLY BUILT, under another name | Named profiles with `context_servers` and `context_excluded` do runtime plugin and tool narrowing from user config, one at a time (profile.go:38-39; cheapcontext.go:173-212). **Fragments are not narrowed:** `Profile` has no prompt-fragment field (profile.go:13-40). | doc-only | Revise FDR 0005: profiles' saved selection covers the plugin half; the fragment half is still open |
| H3 | :54-58 | Options (a) hide, (b) suppress, (c) flag | Partly answered by code | Whole-server: servers start, then are stopped and their dir dropped (cheapcontext.go:479-485; pluginbinding.go:107-121), and in-unit servers are never started (cheapcontext.go:563-577). That is (a). Per-tool: hidden through `/clown/exclude-tools` and refused at call time by moxy and the bridge (FDR 0020:589-599), which is close to (b). | doc-only | Record it in the FDR |
| H4 | :71 | Tracked in #37 | MET | Issue #37 is Open, but its body links `docs/fdrs/0005-hats.md`, which does not exist (the file is `docs/features/`) | doc-only | Fix the link in #37 |

## Known facts the caller asked me to check

- **Exclusion names for moxy (clown#291, 6a4112e)** are confirmed. `resolveExcludedTools` matches a saved name against the live name or its claude-safe form (cheapcontext.go:220-261). The frontend uses it (cheapcontext.go:207; exclusionreadback.go:53) and so does the in-unit relay (agentscope.go:667-671). Tests: cheapcontext_inunit_test.go:100 and :127, the regression test over every builtin. Before this fix only names without a dot matched; the commit message says so.
- **Refusal under a saved profile (75fe63f)** is confirmed for three cases: zero matches, a failed push, and a tool still listed after the push (exclusionreadback.go:60-85, 104-115; cheapcontext.go:543-547; agentscope.go:688-697). Tests: cheapcontext_inunit_test.go:233, :268. The frontend fails at main.go:2104-2108. **A partial match does not refuse.**
- **Is the clown#175 statement stale?** Yes, see C14.
- **Round K** (caller-reported: moxy `grit_branch` ran in a confined canary-grit worker at 955f872). 955f872 comes before 6a4112e, so the exact-match bug explains it: canary-grit excludes `grit_branch` (builtin.toml:244), but moxy renders `grit.branch`. After 6a4112e clown pushes `grit.branch`, **but only if moxy lists it when clown fetches the catalog**. Refusing the call is moxy's job (FDR 0020:589-590). No clown test or bats test proves that call is now refused: UNTESTED.
- **Round P** (caller-reported: moxy saved=152 matched=63) is consistent with the code tolerating a partial match. 89 canary-grit names were neither pushed nor refused (see F6 and issue 2 below).

## Top issues by severity

1. **security: a saved selection fails open when its keep-set is empty or unstarted** (C4). main.go:2111-2113 and main.go:2079-2082 go to `runUnbound`, which hands claude the original plugin dirs (pluginbinding.go:87-90). Fix: when `flags.cheapContextProfile.ContextServers != nil`, refuse the launch, or bind with every server dir dropped. Add a test.
2. **security/correctness: partially matched exclusions launch anyway, and the unmatched names are never pushed.** Only zero matches refuse (exclusionreadback.go:60-63). agentscope.go:675-680 admits that a tool registering after the fetch "stays visible". Round P left 89 of 152 unmatched. FDR 0020:579-581 reads as stricter than the code. Fix options:
   - under a profile, also push the derived rendered form of each unmatched name (`server_tool` becomes `server.tool`);
   - or refuse when `Unmatched` is non-empty;
   - or re-fetch after the moxy children settle.
   Then align FDR 0020.
3. **correctness: the template lists are checked against nothing real** (F6). Add a catalog fixture test.
4. **security (latent): the binary ignores the registry stamp and unknown keys** (F15). It must close before protocol 2 introduces restriction-bearing keys.
5. **correctness: a profile chosen in the picker is not validated** (D1).
6. **correctness (low):** `Canonicalize` can emit `[]` (L10), and no test ties `Profile` fields to `canonical` (L5, A3, A5).
7. **doc-only drift:**
   - the persistence plan (C5, C7, C14, C15, plus the stale profile.go:32-37 comment);
   - the design doc and plan still name the circus paths and models and the old combo matrix (D4-D21, P2-P6); add a "superseded" banner;
   - FDR 0005 does not mention that profiles' saved selections already realize the plugin half (H2-H3).

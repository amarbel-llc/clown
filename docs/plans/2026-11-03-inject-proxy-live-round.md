# InjectProxy live round at the card (clown#336)

Status: package ready 2026-11-03; the round itself waits for the `--all-4_pair`
deploy and the operator. Nothing here reads a real token except where marked
"you": those steps run at the operator's terminal.

InjectProxy landed at origin/master 31f31ef38989. Design: `2026-11-03-oauth-credential-confined.md`;
build plan: `2026-11-03-inject-proxy-build-plan.md`; FDR 0020 "Model credential injection".

## 1. The manifest line (circus side, HELD)

`2026-11-03-inject-proxy-circus-manifest.patch` is a patch against circus's
`infra/hosts/home/operator-secrets.nix`: one new `services.piggy-secrets.files`
entry, source `infra/secrets/anthropic/claude-code-oauth-token.ebox`, target
`/run/user/<uid>/clown/credentials/anthropic-oauth`. Not applied; the operator
applies and merges it in circus.

What was read to write it (not run):

- `piggy-secrets.nix` (piggy's home-manager module): `absoluteTarget` keeps a
  target that starts with `/` and prefixes the home directory otherwise, so an
  absolute runtime-dir target is accepted by the module.
- `piggy secrets reconcile` (piggy `secrets.rs`, man page): writes are temp file,
  fchmod, fsync, rename; default mode 0600; missing parent directories are
  created 0700 (`DirBuilder` mode 0700). Freshness is the ciphertext hash plus
  the recorded stat fingerprint, so a target that vanished at reboot reads as
  missing and is rewritten at the next card-present reconcile. That last point
  is from the man page and the reading of `secrets.rs`, not from a run on a
  tmpfs target: **unverified until the first reconcile after a reboot.**
- The ebox sits in the repo-scoped store (`infra/secrets`), not in the
  operator's `./piggy-store`, hence the `../../secrets/...` source path.
  Whether the flake source and the activation's `--check` see it is circus's to
  confirm when applying.
- The uid in the target (1001, from flic's `/run/user/1001`) is hard-coded;
  confirm it on every host that takes the entry.

## 2. Recipes (all in `zz-explore/justfile`, run as `just explore <recipe>`)

| Step | Recipe | Starts / reads |
|---|---|---|
| 1 | `explore-model-live-check` | prints path, mode, owner, pass/fail; never opens the file |
| 2 | `explore-model-live-run` | builds clown, one confined unit through `clown agent-scope-run` (the claude launcher), `claude -p 'say ok'` inside it; frontend reads the token file per request |
| 3 | `explore-model-live-report` | result fields, proxy request lines, placeholder and real-token greps (names only) |

Step 2 is not a spinclass spawn: no profile applies and the unit sees only
`.tmp/model-live` in the clown checkout. It uses the same launcher, proxy and
credential path as `--agent-scope`, with `CLOWN_MODEL_CREDENTIAL=proxy-only` so
a missing token fails loudly instead of falling back. A real spawn on the
`worker` profile (through spinclass) is the follow-up check: the spawn's result
file must say `model_credential: proxy`.

## 3. What the operator checks

1. `piggy secrets reconcile` (one PIN) after the manifest patch is deployed;
   then `just explore explore-model-live-check` says PASS.
2. `just explore explore-model-live-run`:
   - `base=http://127.0.0.1:19002`, `oauth_is_placeholder=yes`,
     `api_key_empty=yes`, `auth_token_unset=yes`;
   - `credentials_file_bytes=0`;
   - `environ_has_sk_ant=no` (a real OAuth token starts `sk-ant-` as far as I
     recall; the real check is step 3's token grep);
   - claude answers `ok`, `claude_exit=0`; the stderr banner says "model
     credential: injected by the frontend".
3. `just explore explore-model-live-report`:
   - `model_credential: proxy` and the `model-inject-proxy` capability;
   - proxy lines: `HEAD /api/hello` then `POST /v1/messages ... status=200`
     (a 401 means the entry is wrong or revoked: the proxy prints the rotate
     notice once);
   - the placeholder may appear in the run directory (the unit printed it) and
     in transcripts only if the model quoted the environment; that is harmless
     by design;
   - the REAL token greps print `none`. A file name there is a leak: stop and
     report the name, not the content.

## 4. Rollback

Delete the token file (`rm "$XDG_RUNTIME_DIR/clown/credentials/anthropic-oauth"`):
the proxy reads it per request, so a running unit's next model call gets a 503
from the proxy at once, and the next confined launch under the default
`CLOWN_MODEL_CREDENTIAL=proxy-or-login` falls back to the read-only login-file
bind (one stderr line; result says `login-file`). To remove the entry for good,
revert the circus manifest patch and rotate the token (`claude setup-token`).

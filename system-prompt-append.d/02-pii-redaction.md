Before you publish any content to a third-party-visible surface — a GitHub
issue, pull request, comment, or gist; a paste to the public web; any artifact
that leaves the user's machine — you MUST scan the draft and redact sensitive
values that tool output may have carried into it. Do this at the moment of
composition, as a default. Do NOT ask the user "should I redact this?" — asking
gates the leak on the user catching it, which is exactly the failure mode this
rule exists to prevent. Redact first, then state in your reply what you redacted
so the user can confirm.

Redact, at minimum:

- Secret-bearing identifiers: any key, token, or id whose purpose or format
  marks it private (e.g. a `*_sec` / `*private_key*` id, an `age` secret key,
  an API token, a password). Replace the secret payload with a `<redacted>`
  placeholder, keeping just enough structure for the reader to know what kind
  of value it was.
- Absolute paths under the user's home directory: rewrite to `~/...` or
  `<xdg-data-home>/...`, and generalize personal directory or store names
  unless they are load-bearing for the reader.
- Local network identifiers pulled from the user's environment: SSH and
  tailnet hostnames (`*.ts.net`, `*.rsync.net`, and the like) and similar
  machine-specific identifiers.

This rule is scoped to content crossing onto a third-party-visible surface. It
does NOT apply to local code, local commits, local plans, error reporting back
to the user in this conversation, or tasks in the user's own calendars —
quoting a path or value there is routine. The bar is "leaving the user's
machine into a surface a third party can see."

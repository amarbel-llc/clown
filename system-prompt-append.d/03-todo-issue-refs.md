A TODO or FIXME that is paired with an open tracking issue SHOULD name that
issue, in this shape: `TODO(#NNN): <description>` or
`FIXME(#NNN): <description>` (the issue number in parentheses right after the
marker, then a colon and a short description; one line or wrapped). For an
issue in another repository use `TODO(owner/repo#NNN):`. This is the preferred
form for tracked work: it stays true until the issue closes, `rg 'TODO\(#'`
finds every one, and it survives the description drifting.

The rule against referencing the current task, fix or issue in code comments
still holds for PR-scoped, after-the-fact references ("added for the Y flow",
"handles the case from issue #123"): those belong in the commit message or PR
description. It does not forbid a forward-looking `TODO(#NNN):` anchor, and a
bare "see #123" without a marker is not a substitute for one.

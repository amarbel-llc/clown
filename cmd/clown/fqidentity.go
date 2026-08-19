package main

import "fmt"

// fqIdentityFragment renders the clown#222 system-prompt append fragment:
// the requirement that sessions refer to other clown sessions by their
// FULLY-QUALIFIED session ID — <repo>/<spinclass-session>/<clown> — never a
// bare clown-name, plus this session's own FQ ID when both parts are known.
//
// Clown-names collide by design (they are per-session-instance): the incident
// behind clown#222 had two live sessions both named "bozo" on different hosts,
// and a dispatcher waited on the retired one while the other was actively
// reporting to it. The FQ form is stable, human-readable, and maps directly
// onto the spinclass session key plus the chat_list decoration, so the
// requirement is stated unconditionally; the session's own FQ ID line needs
// groupID (CLOWN_GROUP_ID, "<repo>/<spinclass-session>") and the resolved
// clown-name, and is omitted when either is missing (an ungrouped or unnamed
// session then still carries the rule for referring to others, and is told to
// identify itself by whatever parts it has rather than fabricate one).
func fqIdentityFragment(groupID, clownName string) string {
	const rule = "When you refer to another clown session — in reports, dispatches, " +
		"handoffs, status messages, or any cross-session coordination — you MUST " +
		"use its fully-qualified session ID: `<repo>/<spinclass-session>/<clown>` " +
		"(e.g. `circus/keen-aspen/clarabell`), NEVER a bare clown-name. Bare " +
		"clown-names collide across the fleet (two live sessions can share a " +
		"name), so a bare name does not identify a session. Resolve the " +
		"fully-qualified ID from `chat_list` (the session's decoration is " +
		"`<repo>/<spinclass-session>`; its name is the `<clown>` part) before " +
		"naming a session; if you cannot resolve one, say so explicitly rather " +
		"than falling back to the bare name. Sign your own cross-session " +
		"messages with your own fully-qualified session ID."
	if groupID == "" || clownName == "" {
		return rule + " This session is missing part of its own fully-qualified " +
			"ID (no group decoration or no clown-name); identify yourself by the " +
			"parts you do have and never fabricate the missing ones."
	}
	return fmt.Sprintf("Your fully-qualified session ID is `%s/%s` "+
		"(`<repo>/<spinclass-session>/<clown>`). ", groupID, clownName) + rule
}

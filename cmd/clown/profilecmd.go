package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

const profileCmdUsage = "usage: clown profile <list|add|edit <name>|remove <name>>"

// runProfileCmd dispatches `clown profile <list|add|edit|remove>` — the
// management surface for the named-profile registry (user profiles.toml).
func runProfileCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, profileCmdUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runProfileList()
	case "add":
		return runProfileAdd()
	case "edit":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, profileCmdUsage)
			return 2
		}
		return runProfileEdit(args[1])
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, profileCmdUsage)
			return 2
		}
		return runProfileRemove(args[1])
	default:
		fmt.Fprintln(os.Stderr, profileCmdUsage)
		return 2
	}
}

// formatProfileList renders one row per merged profile with its source:
// `builtin`, `user`, or `user override` when a user name shadows a builtin
// (the merge-by-name override mechanic).
func formatProfileList(builtin, user []profile.Profile) string {
	builtinNames := make(map[string]bool, len(builtin))
	for _, p := range builtin {
		builtinNames[p.Name] = true
	}
	userNames := make(map[string]bool, len(user))
	for _, p := range user {
		userNames[p.Name] = true
	}
	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDISPLAY\tPROVIDER / BACKEND\tSOURCE\tCONTEXT")
	for _, p := range profile.Merge(builtin, user) {
		source := "builtin"
		switch {
		case userNames[p.Name] && builtinNames[p.Name]:
			source = "user override"
		case userNames[p.Name]:
			source = "user"
		}
		context := "-"
		if p.ContextServers != nil {
			context = fmt.Sprintf("%d server(s)", len(p.ContextServers))
		}
		fmt.Fprintf(w, "%s\t%s\t%s / %s\t%s\t%s\n", p.Name, p.Display, p.Provider, p.Backend, source, context)
	}
	_ = w.Flush()
	return buf.String()
}

// openrouterOpencodeUpgradeHint returns a note for each merged profile that
// still uses the Phase A shape (provider=opencode, backend=gateway, URL
// pointed at OpenRouter) rather than the Phase B first-class openrouter
// provider (smith#196, docs/plans/2026-07-24-openrouter-non-anthropic-design.md
// Section 3). Phase A profiles keep working unchanged — this is discovery
// only, not a behavior change.
func openrouterOpencodeUpgradeHint(builtin, user []profile.Profile) string {
	var buf strings.Builder
	for _, p := range profile.Merge(builtin, user) {
		if p.Provider == "opencode" && p.Backend == "gateway" && strings.HasPrefix(p.URL, "https://openrouter.ai/") {
			fmt.Fprintf(&buf, "note: profile %q routes to OpenRouter via opencode+gateway; `clown profile edit %s` to switch provider to \"openrouter\" (drops the redundant url field)\n", p.Name, p.Name)
		}
	}
	return buf.String()
}

func runProfileList() int {
	builtin, user, _, err := loadProfileSets("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	fmt.Print(formatProfileList(builtin, user))
	fmt.Print(openrouterOpencodeUpgradeHint(builtin, user))
	return 0
}

func runProfileAdd() int {
	if _, err := profileAddInteractive(); err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	return 0
}

func runProfileEdit(name string) int {
	if _, err := profileEditInteractive(name); err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	return 0
}

func runProfileRemove(name string) int {
	builtin, user, _, err := loadProfileSets("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	remaining, found := profile.Remove(user, name)
	if !found {
		for _, p := range builtin {
			if p.Name == name {
				fmt.Fprintf(os.Stderr, "clown: %q is a builtin profile; builtin profiles cannot be removed (user overrides can)\n", name)
				return 1
			}
		}
		fmt.Fprintf(os.Stderr, "clown: no user profile named %q\n", name)
		return 1
	}
	writePath, err := userConfigWritePath("profiles.toml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if !pluginhost.IsInteractive() {
		fmt.Fprintln(os.Stderr, "clown: profile remove needs an interactive TTY for confirmation")
		return 1
	}
	var confirm bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Remove profile %q from %s?", name, writePath)).
			Affirmative("Remove").
			Negative("Cancel").
			Value(&confirm),
	))
	if err := form.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "clown: prompt: %v\n", err)
		return 1
	}
	if !confirm {
		fmt.Fprintf(os.Stderr, "clown: aborted by user; %s not modified\n", writePath)
		return 1
	}
	if err := profile.Save(writePath, remaining); err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	fmt.Printf("removed profile %q from %s\n", name, writePath)
	return 0
}

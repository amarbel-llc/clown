package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

// profileFormValues holds the huh field bindings for the add/edit form;
// toProfile trims every field into the stored profile. Env, ContextServers,
// and ContextExcluded are not editable through this form (no field prompts
// for them) but are carried through as opaque passthrough values so that
// editing a profile's name/provider/model/etc. via `clown profile edit`
// does not silently wipe a --cheap-context selection (or an Env map)
// someone set another way (--cheap-context's save prompt, or by hand).
type profileFormValues struct {
	Name     string
	Display  string
	Provider string
	Backend  string
	Model    string
	URL      string
	Token    string
	Confirm  bool

	env             map[string]string
	contextServers  []string
	contextExcluded map[string][]string
}

func (v profileFormValues) toProfile() profile.Profile {
	return profile.Profile{
		Name:            strings.TrimSpace(v.Name),
		Display:         strings.TrimSpace(v.Display),
		Provider:        strings.TrimSpace(v.Provider),
		Backend:         strings.TrimSpace(v.Backend),
		Model:           strings.TrimSpace(v.Model),
		URL:             strings.TrimSpace(v.URL),
		Token:           strings.TrimSpace(v.Token),
		Env:             v.env,
		ContextServers:  v.contextServers,
		ContextExcluded: v.contextExcluded,
	}
}

func valuesFromProfile(p profile.Profile) profileFormValues {
	return profileFormValues{
		Name:            p.Name,
		Display:         p.Display,
		Provider:        p.Provider,
		Backend:         p.Backend,
		Model:           p.Model,
		URL:             p.URL,
		Token:           p.Token,
		env:             p.Env,
		contextServers:  p.ContextServers,
		contextExcluded: p.ContextExcluded,
	}
}

type profileTemplate struct {
	key     string // select value
	display string // select label
	p       profile.Profile
	note    string // shown post-save
}

var profileTemplates = []profileTemplate{
	{
		// Renamed from "openrouter" for Phase B (design doc's explicit
		// collision warning): "openrouter" now names the first-class
		// openrouter provider template below. templateByKey returns the
		// first match, so the old key would have silently shadowed it.
		key: "claude-openrouter", display: "Claude (OpenRouter)", p: profile.Profile{
			Name:    "claude-openrouter",
			Display: "Claude (OpenRouter)", Provider: "claude", Backend: "gateway",
			URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}",
		},
		// Model deliberately empty: claude's own defaults flow through and
		// OpenRouter's Anthropic Skin maps them (tuning lever, design doc).
		note: "If claude was previously logged in with an Anthropic account, run /logout once inside claude (cached-login conflict).",
	},
	{
		// Phase B (docs/plans/2026-07-24-openrouter-non-anthropic-design.md):
		// first-class openrouter provider. URL is hardcoded by
		// cmd/clown/opencode.go's runOpencode, not stored on the profile.
		key: "openrouter", display: "OpenRouter", p: profile.Profile{
			Name: "openrouter", Display: "OpenRouter",
			Provider: "openrouter", Backend: "gateway",
			Token: "${OPENROUTER_API_KEY}",
			Model: "openai/gpt-4o",
		},
		note: "Model is an OpenRouter slug (e.g. openai/gpt-4o, google/gemini-2.0-flash-001). Browse at https://openrouter.ai/models",
	},
	{
		key: "openrouter-opencode", display: "OpenRouter (opencode)", p: profile.Profile{
			Name: "opencode-openrouter", Display: "OpenRouter (opencode)",
			Provider: "opencode", Backend: "gateway",
			URL:   "https://openrouter.ai/api/v1",
			Token: "${OPENROUTER_API_KEY}",
			Model: "openai/gpt-4o",
		},
		note: "Model is an OpenRouter slug (e.g. openai/gpt-4o, google/gemini-2.0-flash-001). Browse at https://openrouter.ai/models",
	},
	{key: "gateway", display: "Custom gateway", p: profile.Profile{Provider: "claude", Backend: "gateway"}},
	{key: "anthropic", display: "Anthropic", p: profile.Profile{Provider: "claude", Backend: "anthropic"}},
	{key: "local", display: "Local (juggler)", p: profile.Profile{Provider: "claude", Backend: "local"}},
}

func templateByKey(key string) profileTemplate {
	for _, tpl := range profileTemplates {
		if tpl.key == key {
			return tpl
		}
	}
	return profileTemplate{}
}

var profileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// validateProfileName returns the name-field validator. existing holds USER
// profile names only — duplicating a builtin name is the override mechanic
// and must stay allowed. In edit mode, editOriginal may keep its own name.
func validateProfileName(existing map[string]bool, editOriginal string) func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("name is required")
		}
		if !profileNameRe.MatchString(s) {
			return fmt.Errorf("name must match %s", profileNameRe)
		}
		if existing[s] && s != editOriginal {
			return fmt.Errorf("user profile %q already exists", s)
		}
		return nil
	}
}

// buildProfileForm is the shared add/edit form. The backend select recomputes
// its options from the provider select (OptionsFunc bound to &v.Provider), so
// only valid (provider, backend) combos are offered. URL/token are required
// only for the gateway backend.
func buildProfileForm(v *profileFormValues, existingUserNames map[string]bool, editOriginal, destPath string) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("registry key, e.g. claude-openrouter").
				Value(&v.Name).
				Validate(validateProfileName(existingUserNames, editOriginal)),
			huh.NewInput().
				Title("Display").
				Description("human-readable picker label").
				Value(&v.Display),
			huh.NewSelect[string]().
				Title("Provider").
				Options(huh.NewOptions(profile.Providers()...)...).
				Value(&v.Provider),
			huh.NewSelect[string]().
				Title("Backend").
				OptionsFunc(func() []huh.Option[string] {
					return huh.NewOptions(profile.Backends(v.Provider)...)
				}, &v.Provider).
				Value(&v.Backend),
			huh.NewInput().
				Title("Model").
				Description("empty = provider default").
				Value(&v.Model),
			huh.NewInput().
				Title("URL").
				Description("gateway base URL (required for the gateway backend; for claude, a Model that resolves via juggler is an alternative)").
				Value(&v.URL).
				Validate(func(s string) error {
					if v.Backend != "gateway" {
						return nil
					}
					url := strings.TrimSpace(s)
					switch v.Provider {
					case "claude":
						// claude has the juggler-fallback branch (applyNamedProfile):
						// url-or-model satisfies the gateway backend.
						if url == "" && strings.TrimSpace(v.Model) == "" {
							return fmt.Errorf("url is required for the gateway backend (or set Model to resolve via juggler)")
						}
						return nil
					case "openrouter":
						// URL is hardcoded by runOpencode's openrouter branch —
						// not user-entered.
						return nil
					default:
						// opencode/crush have no juggler-resolution fallback yet
						// (runOpencode/runCrush read URL/Token directly) — url is
						// unconditionally required.
						if url == "" {
							return fmt.Errorf("url is required for the gateway backend")
						}
						return nil
					}
				}),
			huh.NewInput().
				Title("Token").
				Description("literal or ${VAR} reference (required for the gateway backend when URL is set)").
				EchoMode(huh.EchoModePassword).
				Value(&v.Token).
				Validate(func(s string) error {
					if v.Backend != "gateway" {
						return nil
					}
					token := strings.TrimSpace(s)
					if v.Provider == "claude" {
						if strings.TrimSpace(v.URL) != "" && token == "" {
							return fmt.Errorf("token is required for the gateway backend when url is set")
						}
						return nil
					}
					if token == "" {
						return fmt.Errorf("token is required for the gateway backend")
					}
					return nil
				}),
			huh.NewConfirm().
				Title(fmt.Sprintf("Save to %s?", destPath)).
				Affirmative("Save").
				Negative("Cancel").
				Value(&v.Confirm),
		),
	)
}

// saveProfileForm validates the form result and upserts it into the user
// profiles.toml. Abort semantics mirror promptOpencodeLocalConfig: cancel or
// Ctrl-C writes nothing and returns an error naming the untouched file.
func saveProfileForm(v profileFormValues, user []profile.Profile, editOriginal, destPath string) (*profile.Profile, error) {
	if !v.Confirm {
		return nil, fmt.Errorf("aborted by user; %s not written", destPath)
	}
	p := v.toProfile()
	if err := profile.Validate(p); err != nil {
		return nil, err
	}
	// An edit that renames a user entry replaces it rather than leaving the
	// old-named entry behind.
	if editOriginal != "" && editOriginal != p.Name {
		user, _ = profile.Remove(user, editOriginal)
	}
	if err := profile.Save(destPath, profile.Upsert(user, p)); err != nil {
		return nil, err
	}
	fmt.Printf("saved profile %q to %s\n", p.Name, destPath)
	return &p, nil
}

// profileAddInteractive runs the add flow (template select, form, save) and
// returns the saved profile. Factored from runProfileAdd so the startup
// picker's `+ add profile…` hook can call the core and continue the launch
// with the result instead of going through exit codes.
func profileAddInteractive() (*profile.Profile, error) {
	if !pluginhost.IsInteractive() {
		return nil, fmt.Errorf("profile add needs an interactive TTY")
	}
	_, user, _, err := loadProfileSets("")
	if err != nil {
		return nil, err
	}
	destPath, err := userConfigWritePath("profiles.toml")
	if err != nil {
		return nil, err
	}

	tplOptions := make([]huh.Option[string], len(profileTemplates))
	for i, tpl := range profileTemplates {
		tplOptions[i] = huh.NewOption(tpl.display, tpl.key)
	}
	var tplKey string
	tplForm := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Template").
			Options(tplOptions...).
			Value(&tplKey),
	))
	if err := tplForm.Run(); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	tpl := templateByKey(tplKey)

	v := valuesFromProfile(tpl.p)
	existing := userNameSet(user)
	if err := buildProfileForm(&v, existing, "", destPath).Run(); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	p, err := saveProfileForm(v, user, "", destPath)
	if err != nil {
		return nil, err
	}
	if tpl.note != "" {
		fmt.Println(tpl.note)
	}
	return p, nil
}

// profileEditInteractive prefills the form from the merged entry. Editing a
// builtin creates a user override (editOriginal stays empty — the name is
// not in the user set, so the uniqueness check passes and Upsert appends).
func profileEditInteractive(name string) (*profile.Profile, error) {
	builtin, user, _, err := loadProfileSets("")
	if err != nil {
		return nil, err
	}
	merged := profile.Merge(builtin, user)
	var found *profile.Profile
	for i := range merged {
		if merged[i].Name == name {
			found = &merged[i]
			break
		}
	}
	if found == nil {
		var names []string
		for _, p := range merged {
			names = append(names, p.Name)
		}
		return nil, fmt.Errorf("no profile named %q (available: %s)", name, strings.Join(names, ", "))
	}
	// The name-lookup error above is useful without a TTY (e.g. a typo check
	// from a script); the TTY gate only matters once we know we'd actually
	// render the form.
	if !pluginhost.IsInteractive() {
		return nil, fmt.Errorf("profile edit needs an interactive TTY")
	}
	destPath, err := userConfigWritePath("profiles.toml")
	if err != nil {
		return nil, err
	}

	existing := userNameSet(user)
	editOriginal := ""
	if existing[name] {
		editOriginal = name
	}
	v := valuesFromProfile(*found)
	if err := buildProfileForm(&v, existing, editOriginal, destPath).Run(); err != nil {
		return nil, fmt.Errorf("prompt: %w", err)
	}
	return saveProfileForm(v, user, editOriginal, destPath)
}

func userNameSet(user []profile.Profile) map[string]bool {
	set := make(map[string]bool, len(user))
	for _, p := range user {
		set[p.Name] = true
	}
	return set
}

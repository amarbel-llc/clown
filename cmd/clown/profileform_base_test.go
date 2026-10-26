package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
)

// fullyPopulatedProfile sets every field of profile.Profile to a non-zero
// value, by reflection, so a field added later is covered without editing
// this test, and a field of a kind it cannot fill fails it loudly.
func fullyPopulatedProfile(t *testing.T) profile.Profile {
	t.Helper()
	var p profile.Profile
	v := reflect.ValueOf(&p).Elem()
	for i := range v.NumField() {
		f, name := v.Field(i), v.Type().Field(i).Name
		switch f.Interface().(type) {
		case string:
			f.SetString("value-of-" + name)
		case []string:
			f.Set(reflect.ValueOf([]string{"item-of-" + name}))
		case map[string]string:
			f.Set(reflect.ValueOf(map[string]string{"KEY_" + name: "value"}))
		case map[string][]string:
			f.Set(reflect.ValueOf(map[string][]string{"key-" + name: {"item"}}))
		default:
			t.Fatalf("profile.Profile.%s has kind %s: teach fullyPopulatedProfile to fill it", name, f.Type())
		}
	}
	return p
}

// Every profile field survives the form's values round trip, shown in the
// form or not (clown#303 dropped base).
func TestProfileFormValuesRoundTripEveryField(t *testing.T) {
	original := fullyPopulatedProfile(t)
	if got := valuesFromProfile(original).toProfile(); !reflect.DeepEqual(got, original) {
		t.Errorf("round trip lost fields:\n got  %#v\n want %#v", got, original)
	}
}

func saveRegistry(t *testing.T, profiles ...profile.Profile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := profile.Save(path, profiles, "test"); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadRegistry(t *testing.T, path string) profile.Registry {
	t.Helper()
	r, err := profile.LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findProfile(t *testing.T, r profile.Registry, name string) profile.Profile {
	t.Helper()
	for _, p := range r.Profiles {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no profile %q in %+v", name, r.Profiles)
	return profile.Profile{}
}

var narrowingProfile = profile.Profile{
	Name: "narrow", Provider: "claude", Backend: "anthropic", Base: "claude",
}

// Editing another field of a profile with a base keeps the base, and the
// registry stays stamped protocol 2.
func TestProfileEditKeepsBase(t *testing.T) {
	path := saveRegistry(t, narrowingProfile)
	user := loadRegistry(t, path).Profiles

	v := valuesFromProfile(findProfile(t, profile.Registry{Profiles: user}, "narrow"))
	v.Display = "Narrow (edited)"
	v.Confirm = true
	if _, err := saveProfileForm(v, user, "narrow", path); err != nil {
		t.Fatal(err)
	}

	r := loadRegistry(t, path)
	got := findProfile(t, r, "narrow")
	if got.Base != "claude" || got.Display != "Narrow (edited)" {
		t.Errorf("edited profile %+v, want base kept and display edited", got)
	}
	if r.Stamp.Protocol != 2 {
		t.Errorf("registry protocol %d after the edit, want 2 (a profile has base)", r.Stamp.Protocol)
	}
}

// Saving a --cheap-context selection into a profile with a base keeps the
// base.
func TestCheapContextSaveKeepsBase(t *testing.T) {
	path := saveRegistry(t, narrowingProfile)
	user := loadRegistry(t, path).Profiles
	saveCtx := cheapContextSaveContext{merged: user, user: user, path: path}

	excluded := map[string][]string{"moxy/moxy": {"grit.status"}}
	if err := saveSelectionToProfile("narrow", []string{"moxy/moxy"}, excluded, saveCtx); err != nil {
		t.Fatal(err)
	}

	r := loadRegistry(t, path)
	got := findProfile(t, r, "narrow")
	if got.Base != "claude" {
		t.Errorf("base %q after the cheap-context save, want claude", got.Base)
	}
	if !reflect.DeepEqual(got.ContextServers, []string{"moxy/moxy"}) || !reflect.DeepEqual(got.ContextExcluded, excluded) {
		t.Errorf("saved selection %v / %v", got.ContextServers, got.ContextExcluded)
	}
	if r.Stamp.Protocol != 2 {
		t.Errorf("registry protocol %d after the save, want 2", r.Stamp.Protocol)
	}
}

// A profile without a base is unchanged by an edit, and the registry stays
// protocol 1.
func TestProfileEditWithoutBase(t *testing.T) {
	plain := profile.Profile{Name: "plain", Provider: "claude", Backend: "anthropic"}
	path := saveRegistry(t, plain)
	user := loadRegistry(t, path).Profiles

	v := valuesFromProfile(user[0])
	v.Confirm = true
	if _, err := saveProfileForm(v, user, "plain", path); err != nil {
		t.Fatal(err)
	}

	r := loadRegistry(t, path)
	if got := findProfile(t, r, "plain"); !reflect.DeepEqual(got, plain) {
		t.Errorf("profile %+v after a no-op edit, want %+v", got, plain)
	}
	if r.Stamp.Protocol != 1 {
		t.Errorf("registry protocol %d, want 1 (no profile has base)", r.Stamp.Protocol)
	}
}

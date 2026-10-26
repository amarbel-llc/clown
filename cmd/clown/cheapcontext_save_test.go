package main

import (
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

// Only a server whose catalog the picker showed tool by tool is "shown":
// not one whose catalog fetch failed, an in-unit one, or a single-group one.
func TestSelectionFromPickerRecordsWhichServersItShowedToolByTool(t *testing.T) {
	moxy := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}
	fetchFailed := pluginhost.DiscoveredServer{PluginName: "bob", ServerName: "caldav"}
	inUnit := pluginhost.DiscoveredServer{PluginName: "unit", ServerName: "confined"}
	single := pluginhost.DiscoveredServer{PluginName: "one", ServerName: "group"}
	catalogs := []serverCatalog{
		{server: moxy, groups: []toolGroup{
			{name: "folio", tools: []string{"folio.read", "folio.glob"}},
			{name: "grit", tools: []string{"grit.status"}},
		}},
		{server: fetchFailed},
		{server: inUnit, inUnit: true},
		{server: single, groups: []toolGroup{{name: "only", tools: []string{"only.a"}}}},
	}
	chosen := map[string]bool{}
	for _, c := range catalogs {
		chosen[rowKey(c.server.Name(), allRowSuffix)] = true
	}
	chosen[rowKey(moxy.Name(), "folio.read")] = true
	chosen[rowKey(moxy.Name(), "grit.status")] = true

	result := selectionFromPicker(catalogs, chosen)
	if want := map[string]bool{moxy.Name(): true}; !reflect.DeepEqual(result.toolsShown, want) {
		t.Errorf("toolsShown %v, want %v", result.toolsShown, want)
	}
	if want := map[string][]string{moxy.Name(): {"folio.glob"}}; !reflect.DeepEqual(result.excludedTools, want) {
		t.Errorf("excludedTools %v, want %v", result.excludedTools, want)
	}
	if len(result.kept) != len(catalogs) {
		t.Errorf("kept %d servers, want %d", len(result.kept), len(catalogs))
	}
	wantOffered := map[string]bool{}
	for _, c := range catalogs {
		wantOffered[c.server.Name()] = true
	}
	if !reflect.DeepEqual(result.offered, wantOffered) {
		t.Errorf("offered %v, want every catalog server %v", result.offered, wantOffered)
	}
}

// An offered server the operator unchecks is not kept, and is still
// offered: its removal from context_servers is the operator's choice.
func TestSelectionFromPickerOffersAnUncheckedServer(t *testing.T) {
	moxy := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}
	caldav := pluginhost.DiscoveredServer{PluginName: "bob", ServerName: "caldav"}
	result := selectionFromPicker([]serverCatalog{{server: moxy}, {server: caldav}},
		map[string]bool{rowKey(moxy.Name(), allRowSuffix): true})
	if len(result.kept) != 1 || result.kept[0].Name() != moxy.Name() || !result.offered[caldav.Name()] {
		t.Errorf("kept %v, offered %v; want only moxy kept and caldav offered", result.kept, result.offered)
	}
}

// thisLaunchOffered is what the picker offered in the save tests: every
// saved server but absent/server, which was not discovered this launch.
var thisLaunchOffered = map[string]bool{"moxy/moxy": true, "unit/confined": true, "bob/caldav": true}

// savedNarrowing has exclusions for a server the picker shows tool by tool
// (moxy), an in-unit one, one whose catalog fetch fails, and one not
// started this launch.
var savedNarrowing = profile.Profile{
	Name: "narrow", Provider: "claude", Backend: "anthropic", Base: "claude",
	ContextServers: []string{"moxy/moxy", "unit/confined", "bob/caldav", "absent/server"},
	ContextExcluded: map[string][]string{
		"moxy/moxy":     {"grit.status"},
		"unit/confined": {"confined.write"},
		"bob/caldav":    {"caldav.delete"},
		"absent/server": {"absent.tool"},
	},
}

func saveSelectionInto(t *testing.T, existing profile.Profile, name string, servers []string, excluded map[string][]string, shown map[string]bool) profile.Registry {
	t.Helper()
	path := saveRegistry(t, existing)
	user := loadRegistry(t, path).Profiles
	if err := saveSelectionToProfile(name, servers, excluded, shown, thisLaunchOffered, cheapContextSaveContext{merged: user, user: user, path: path}); err != nil {
		t.Fatal(err)
	}
	return loadRegistry(t, path)
}

// Saving into an existing profile replaces the exclusions of the servers the
// picker showed tool by tool, and keeps every other server's saved ones
// (clown#305).
func TestCheapContextSaveKeepsExclusionsThePickerDidNotShow(t *testing.T) {
	r := saveSelectionInto(t, savedNarrowing, "narrow",
		[]string{"moxy/moxy", "unit/confined", "bob/caldav"},
		map[string][]string{"moxy/moxy": {"folio.glob"}},
		map[string]bool{"moxy/moxy": true})

	got := findProfile(t, r, "narrow")
	want := map[string][]string{
		"moxy/moxy":     {"folio.glob"},
		"unit/confined": {"confined.write"},
		"bob/caldav":    {"caldav.delete"},
		"absent/server": {"absent.tool"},
	}
	if !reflect.DeepEqual(got.ContextExcluded, want) {
		t.Errorf("saved exclusions %v, want %v", got.ContextExcluded, want)
	}
	if want := []string{"moxy/moxy", "unit/confined", "bob/caldav", "absent/server"}; !reflect.DeepEqual(got.ContextServers, want) {
		t.Errorf("saved servers %v, want this launch's kept set plus the undiscovered saved one %v", got.ContextServers, want)
	}
	if got.Base != "claude" || r.Stamp.Protocol != 2 {
		t.Errorf("base %q, protocol %d; want claude and 2", got.Base, r.Stamp.Protocol)
	}
}

// A server the picker offered and the operator unchecked leaves
// context_servers; a saved server not discovered this launch stays
// (clown#316).
func TestCheapContextSaveKeepsUndiscoveredServersOnly(t *testing.T) {
	r := saveSelectionInto(t, savedNarrowing, "narrow",
		[]string{"moxy/moxy", "unit/confined"},
		map[string][]string{"moxy/moxy": {"folio.glob"}},
		map[string]bool{"moxy/moxy": true})

	got := findProfile(t, r, "narrow")
	if want := []string{"moxy/moxy", "unit/confined", "absent/server"}; !reflect.DeepEqual(got.ContextServers, want) {
		t.Errorf("saved servers %v, want %v (bob/caldav unchecked, absent/server undiscovered)", got.ContextServers, want)
	}
	if got.Base != "claude" || r.Stamp.Protocol != 2 {
		t.Errorf("base %q, protocol %d; want claude and 2", got.Base, r.Stamp.Protocol)
	}
}

// A shown server whose tools were all left checked loses its saved
// exclusions; the others keep theirs.
func TestCheapContextSaveClearsExclusionsOfAShownServer(t *testing.T) {
	r := saveSelectionInto(t, savedNarrowing, "narrow",
		[]string{"moxy/moxy", "unit/confined", "bob/caldav"},
		map[string][]string{},
		map[string]bool{"moxy/moxy": true})

	got := findProfile(t, r, "narrow")
	if _, ok := got.ContextExcluded["moxy/moxy"]; ok {
		t.Errorf("moxy/moxy kept exclusions %v, want none (all its tools were checked)", got.ContextExcluded["moxy/moxy"])
	}
	if !reflect.DeepEqual(got.ContextExcluded["unit/confined"], []string{"confined.write"}) {
		t.Errorf("in-unit exclusions %v, want kept", got.ContextExcluded["unit/confined"])
	}
}

// A new profile records exactly this launch's exclusions.
func TestCheapContextSaveIntoANewProfile(t *testing.T) {
	excluded := map[string][]string{"moxy/moxy": {"folio.glob"}}
	r := saveSelectionInto(t, savedNarrowing, "fresh", []string{"moxy/moxy"}, excluded, map[string]bool{"moxy/moxy": true})

	got := findProfile(t, r, "fresh")
	if !reflect.DeepEqual(got.ContextExcluded, excluded) || got.Base != "" || !reflect.DeepEqual(got.ContextServers, []string{"moxy/moxy"}) {
		t.Errorf("new profile %+v, want only this launch's selection and no base", got)
	}
	if old := findProfile(t, r, "narrow"); !reflect.DeepEqual(old, savedNarrowing) {
		t.Errorf("saving a new profile changed %q: %+v", old.Name, old)
	}
}

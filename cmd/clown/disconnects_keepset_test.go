package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/profile"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// The worker's keep-set names clown-builtin-diagnostics/disconnects, a server
// a launch does not synthesize with CLOWN_DISABLE_DISCONNECT_RECORDS=1, with
// --naked, or for a provider without plugin dirs. Neither profilelookup (which
// only requires the job-control servers) nor the launcher's selection
// (a saved server that was not discovered is dropped) rejects that: the worker
// launches with the servers it does have.
func TestWorkerKeepSetNamingAnAbsentDiagnosticsServerStillLaunches(t *testing.T) {
	const diagnostics = "clown-builtin-diagnostics/disconnects"
	absent := filepath.Join(t.TempDir(), "profiles.toml")

	r, err := profilelookup.Resolve("worker", profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatalf("worker does not resolve: %v", err)
	}
	if !slices.Contains(r.Selection.Kept, diagnostics) {
		t.Fatalf("worker's keep-set %v no longer names %s: this test pins the absent-server case", r.Selection.Kept, diagnostics)
	}
	if missing := r.Selection.Missing(profilelookup.RequiredServers()); len(missing) != 0 {
		t.Errorf("worker misses required servers %v", missing)
	}

	var worker *profile.Profile
	builtins, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range builtins {
		if builtins[i].Name == "worker" {
			worker = &builtins[i]
		}
	}
	if worker == nil {
		t.Fatal("no builtin worker")
	}

	var catalogs []serverCatalog
	for _, name := range worker.ContextServers {
		if name == diagnostics {
			continue // not synthesized in this launch
		}
		plugin, server, _ := strings.Cut(name, "/")
		catalogs = append(catalogs, serverCatalog{server: pluginhost.DiscoveredServer{PluginName: plugin, ServerName: server}})
	}
	result, ok := selectionFromSavedProfile(worker, catalogs)
	if !ok {
		t.Fatal("the worker's saved selection was not applied")
	}
	if len(result.kept) != len(worker.ContextServers)-1 {
		t.Errorf("kept %d servers, want the %d the launch has", len(result.kept), len(worker.ContextServers)-1)
	}
	for _, s := range result.kept {
		if s.Name() == diagnostics {
			t.Errorf("kept %s, which this launch did not discover", s.Name())
		}
	}
}

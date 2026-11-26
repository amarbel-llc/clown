package agentscope

import (
	"slices"
	"strings"
	"testing"
)

// documentedProperties is the list man/man7/clown-agent-scope.7 prints under
// PROPERTIES. Adding or removing a systemd property in Launch.properties() makes
// TestPropertyNamesAreTheDocumentedSet fail until this list AND the page are
// updated: the page cannot drift silently from the code (principle 45).
var documentedProperties = []string{
	"BindPaths", "BindReadOnlyPaths", "CPUQuota", "IOWeight", "InaccessiblePaths",
	"MemoryHigh", "MemoryMax", "MemorySwapMax", "NoNewPrivileges", "OOMPolicy",
	"PrivateNetwork", "PrivatePIDs", "PrivateTmp", "PrivateUsers", "ProtectControlGroups",
	"ProtectHome", "ProtectSystem", "ReadWritePaths", "Restart", "RuntimeMaxSec",
	"TasksMax", "TemporaryFileSystem", "WorkingDirectory",
}

func TestPropertyNamesAreTheDocumentedSet(t *testing.T) {
	l := testLaunch()
	l.Unattended = true // RuntimeMaxSec
	l.BridgeDir = testBridgeDir
	l.Spec.DenyPaths = []string{"/home/op/.ssh"}
	l.Spec.BindsRO = []string{"/home/op/ro"}
	var names []string
	for _, p := range l.properties() {
		k, _, _ := strings.Cut(p, "=")
		if !slices.Contains(names, k) {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	want := slices.Clone(documentedProperties)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("properties() emits %v, the page documents %v: update man/man7/clown-agent-scope.7 and documentedProperties together", names, want)
	}
}

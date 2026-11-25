package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

func serveFixture(env map[string]string) []pluginhost.DiscoveredServer {
	return []pluginhost.DiscoveredServer{{
		PluginName: servePlugin,
		ServerName: "serve",
		Def:        pluginhost.ServerDef{Command: "/bin/bridge", Scope: pluginhost.ScopeFrontend, Env: env},
	}}
}

func TestRouteBridgeOffByDefault(t *testing.T) {
	rt := t.TempDir()
	in := serveFixture(nil)
	out, b, err := routeBridge(in, "/bin/bridge", rt, "k1", "u1", false)
	if err != nil || b.Dir != "" || out[0].Def.Env != nil {
		t.Fatalf("switch off: %+v %v env %v", b, err, out[0].Def.Env)
	}
	if _, err := os.Stat(filepath.Join(rt, "clown")); !os.IsNotExist(err) {
		t.Errorf("switch off created something under the runtime dir: %v", err)
	}
}

func TestRouteBridgeNamesDirAndUnitAndLeavesOthersAlone(t *testing.T) {
	rt := t.TempDir()
	in := serveFixture(map[string]string{"A": "1"})
	other := pluginhost.DiscoveredServer{PluginName: "ringmaster", ServerName: "r", Def: pluginhost.ServerDef{Command: "/bin/other", Scope: pluginhost.ScopeFrontend}}
	in = append(in, other)
	out, b, err := routeBridge(in, "/bin/bridge", rt, "k1", "clown-agent-x", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(rt, "clown", "launch", "k1", "bridge")
	if b.Dir != dir || b.Socket != dir+"/bridge.sock" {
		t.Errorf("bridge = %+v", b)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", fi, err)
	}
	env := out[0].Def.Env
	if env["CLOWN_BRIDGE_DIR"] != dir || env["CLOWN_BRIDGE_UNIT"] != "clown-agent-x.service" || env["A"] != "1" {
		t.Errorf("env = %v", env)
	}
	if in[0].Def.Env["CLOWN_BRIDGE_DIR"] != "" {
		t.Error("the caller's map was changed")
	}
	if out[1].Def.Env != nil {
		t.Errorf("another server's env changed: %v", out[1].Def.Env)
	}
	// An already-suffixed unit is not doubled.
	out, _, _ = routeBridge(in, "/bin/bridge", rt, "k1", "u.service", true)
	if out[0].Def.Env["CLOWN_BRIDGE_UNIT"] != "u.service" {
		t.Errorf("unit = %q", out[0].Def.Env["CLOWN_BRIDGE_UNIT"])
	}
	b.cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left the dir: %v", err)
	}
}

func TestRouteBridgeRefusesTheBridgeNotTheLaunch(t *testing.T) {
	rt := t.TempDir()
	in := serveFixture(nil)
	for name, c := range map[string]struct{ rt, key, unit string }{
		"relative runtime": {"rel", "k", "u"},
		"slash in key":     {rt, "a/b", "u"},
		"dotdot key":       {rt, "..", "u"},
		"no unit":          {rt, "k", ""},
		"path too long":    {rt, strings.Repeat("k", 120), "u"},
	} {
		out, b, err := routeBridge(in, "/bin/bridge", c.rt, c.key, c.unit, true)
		if err == nil || b.Dir != "" || out[0].Def.Env != nil {
			t.Errorf("%s: err %v bridge %+v env %v", name, err, b, out[0].Def.Env)
		}
	}
	// No serve in the session: nothing, no error, nothing created.
	out, b, err := routeBridge(nil, "/bin/bridge", rt, "k", "u", true)
	if err != nil || b.Dir != "" || len(out) != 0 {
		t.Errorf("no serve: %v %+v", err, b)
	}
	if _, err := os.Stat(filepath.Join(rt, "clown")); !os.IsNotExist(err) {
		t.Errorf("a refused bridge created %v", err)
	}
}

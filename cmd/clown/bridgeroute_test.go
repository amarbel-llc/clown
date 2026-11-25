package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

func serveFixture(env map[string]string) []pluginhost.DiscoveredServer {
	return []pluginhost.DiscoveredServer{{
		PluginName: servePlugin,
		ServerName: "serve",
		Def:        pluginhost.ServerDef{Command: "/bin/bridge", Scope: pluginhost.ScopeFrontend, Env: env},
	}}
}

// bridgeEnvIsOff is whether env says "off" in writing: the three bridge
// variables are present and empty.
func bridgeEnvIsOff(env map[string]string) bool {
	for _, k := range bridgeEnvKeys {
		if v, ok := env[k]; !ok || v != "" {
			return false
		}
	}
	return true
}

func TestRouteBridgeOffByDefault(t *testing.T) {
	rt := t.TempDir()
	in := serveFixture(nil)
	out, b, err := routeBridge(in, "/bin/bridge", rt, "k1", "u1", false)
	if err != nil || b.Dir != "" || !bridgeEnvIsOff(out[0].Def.Env) {
		t.Fatalf("switch off: %+v %v env %v", b, err, out[0].Def.Env)
	}
	if in[0].Def.Env != nil {
		t.Error("the caller's servers were changed")
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
	// The cgroup file is beside the bridge directory, never inside it (the
	// directory is bound read-only into the unit; the file is not).
	if want := filepath.Join(rt, "clown", "launch", "k1", "bridge.cgroup"); env["CLOWN_BRIDGE_CGROUP_FILE"] != want || b.CgroupFile != want {
		t.Errorf("cgroup file = %q / %q, want %q", env["CLOWN_BRIDGE_CGROUP_FILE"], b.CgroupFile, want)
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

func TestWriteCgroupIsAtomicPrivateAndExact(t *testing.T) {
	rt := t.TempDir()
	_, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	cg := "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/u.service"
	if err := b.writeCgroup(cg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(b.CgroupFile)
	if err != nil || string(got) != cg {
		t.Fatalf("file = %q, %v; want exactly %q", got, err, cg)
	}
	fi, err := os.Lstat(b.CgroupFile)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", fi.Mode(), err)
	}
	if _, err := os.Stat(b.CgroupFile + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a temporary file was left: %v", err)
	}
	// A relaunch with a new unit rewrites it; a symlink in the way is replaced,
	// not followed.
	victim := filepath.Join(rt, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b.CgroupFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, b.CgroupFile); err != nil {
		t.Fatal(err)
	}
	if err := b.writeCgroup(cg + "2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := os.ReadFile(victim); string(v) != "keep" {
		t.Errorf("the write followed a symlink: %q", v)
	}
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != cg+"2" {
		t.Errorf("rewrite = %q", got)
	}
	for _, bad := range []string{"", "relative", "/a/b/", "/a\nb"} {
		if err := b.writeCgroup(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// A zero bridge writes nothing and is not an error.
	if err := (sessionBridge{}).writeCgroup(cg); err != nil {
		t.Errorf("zero bridge: %v", err)
	}
	b.cleanup()
	if _, err := os.Lstat(b.CgroupFile); !os.IsNotExist(err) {
		t.Errorf("cleanup left the cgroup file: %v", err)
	}
}

func TestWireBridgeBindsTheDirAndRecordsTheCgroup(t *testing.T) {
	var l agentscope.Launch
	wireBridge(&l, sessionBridge{}, nil)
	if l.BridgeDir != "" || l.OnCgroup != nil {
		t.Fatalf("a zero bridge changed the launch: %+v", l)
	}
	rt := t.TempDir()
	_, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	wireBridge(&l, b, nil)
	if l.BridgeDir != b.Dir || l.OnCgroup == nil {
		t.Fatalf("launch = %+v", l)
	}
	l.OnCgroup("/a/b/u.service")
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != "/a/b/u.service" {
		t.Errorf("cgroup file = %q", got)
	}
	// A bad cgroup is logged, not fatal, and leaves the old file alone.
	l.OnCgroup("not-absolute")
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != "/a/b/u.service" {
		t.Errorf("a bad cgroup replaced the file: %q", got)
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
		if err == nil || b.Dir != "" || !bridgeEnvIsOff(out[0].Def.Env) {
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

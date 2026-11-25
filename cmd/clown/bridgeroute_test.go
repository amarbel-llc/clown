package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/alfa/agentscope"
	"code.linenisgreat.com/clown/internal/alfa/bridgeshim"
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
	cg2 := "/user.slice/user-2.slice/clown.slice/clown-agent.slice/u.service"
	if err := b.writeCgroup(cg2); err != nil {
		t.Fatal(err)
	}
	if v, _ := os.ReadFile(victim); string(v) != "keep" {
		t.Errorf("the write followed a symlink: %q", v)
	}
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != cg2 {
		t.Errorf("rewrite = %q", got)
	}
	// Round 1, A-L5: identity, not just shape: another unit's cgroup, the user
	// manager's own, and a look-alike without the slice are refused.
	for _, bad := range []string{
		"", "relative", "/a/b/", "/a\nb", "/user.slice/user-1001.slice/user@1001.service",
		"/user.slice/clown.slice/clown-agent.slice/other.service", "/x/u.service",
	} {
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
	const cg = "/user.slice/clown.slice/clown-agent.slice/u.service"
	if l.BridgeReady == nil || l.BridgeReady.Load() {
		t.Fatalf("ready before any write: %v", l.BridgeReady)
	}
	l.OnCgroup(cg)
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != cg {
		t.Errorf("cgroup file = %q", got)
	}
	if !l.BridgeReady.Load() {
		t.Error("not ready after a successful write")
	}
	// A bad cgroup is logged, not fatal, leaves the old file alone and is not "ready".
	l.OnCgroup("not-absolute")
	if got, _ := os.ReadFile(b.CgroupFile); string(got) != cg {
		t.Errorf("a bad cgroup replaced the file: %q", got)
	}
	if l.BridgeReady.Load() {
		t.Error("ready after a failed write (bridge-v1 would be claimed for a bridge that admits nobody)")
	}
}

// Round 1, A-M3 / B-M3: nothing an earlier launch of the key left is trusted:
// a stale socket, cgroup file and temp file are removed before the serve starts,
// a symlinked bridge dir is refused (never followed), and the directory is
// recreated 0700. Red without prepareBridgeDir.
func TestRouteBridgeStartsFromACleanDirectory(t *testing.T) {
	rt := t.TempDir()
	launch := filepath.Join(rt, "clown", "launch", "k1")
	dir := filepath.Join(launch, "bridge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := []string{filepath.Join(dir, "bridge.sock"), filepath.Join(launch, "bridge.cgroup"), filepath.Join(launch, "bridge.cgroup.tmp")}
	for _, f := range stale {
		if err := os.WriteFile(f, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true); err != nil || b.Dir != dir {
		t.Fatalf("route: %+v %v", b, err)
	}
	for _, f := range stale {
		if _, err := os.Lstat(f); !os.IsNotExist(err) {
			t.Errorf("the stale %s survived: %v", filepath.Base(f), err)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode: %v %v", fi, err)
	}

	// A symlink where the bridge dir should be: refused, the target untouched.
	rt2 := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "bridge.sock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch2 := filepath.Join(rt2, "clown", "launch", "k2")
	if err := os.MkdirAll(launch2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(launch2, "bridge")); err != nil {
		t.Fatal(err)
	}
	out, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt2, "k2", "u", true)
	if err == nil || b.Dir != "" || !bridgeEnvIsOff(out[0].Def.Env) {
		t.Errorf("a symlinked bridge dir was accepted: %+v %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(target, "bridge.sock")); err != nil {
		t.Errorf("the symlink's target was touched: %v", err)
	}
}

// Round 1, B-L1: cleanup removes the cgroup file only if it still holds what
// this process wrote; a frontend that took the key over keeps its own.
func TestCleanupKeepsAnotherFrontendsCgroupFile(t *testing.T) {
	rt := t.TempDir()
	_, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	const cg = "/user.slice/clown.slice/clown-agent.slice/u.service"
	if err := b.writeCgroup(cg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.CgroupFile, []byte("/someone/elses"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.cleanup()
	if got, err := os.ReadFile(b.CgroupFile); err != nil || string(got) != "/someone/elses" {
		t.Errorf("cleanup removed a file it did not write: %q %v", got, err)
	}
}

// Round 1, A-M1: dropping the bridge after the serve started removes its
// socket and the cgroup file (the serve then admits nobody and nothing can
// connect), and does not touch a drop of "no bridge".
func TestOrphanRemovesTheSocketAndTheCgroupFile(t *testing.T) {
	rt := t.TempDir()
	_, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{b.Socket, b.CgroupFile, b.CgroupFile + ".tmp"} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b.orphan(nil)
	for _, f := range []string{b.Socket, b.CgroupFile, b.CgroupFile + ".tmp"} {
		if _, err := os.Lstat(f); !os.IsNotExist(err) {
			t.Errorf("%s survived the drop: %v", filepath.Base(f), err)
		}
	}
	(sessionBridge{}).orphan(nil) // a zero bridge: no-op, no panic
}

// Round 1, M7: the verb that the in-unit start path runs, against a fake
// server that answers like the reviewed listener (refused until it can admit
// the unit), end to end: ready line, control held after the retries, and the
// fixed down text when control ends.
func TestBridgeListenHoldsControlAndSaysWhenItEnds(t *testing.T) {
	oldW, oldD := bridgeshim.ControlRetryWindow, bridgeshim.ControlRetryDelay
	bridgeshim.ControlRetryWindow, bridgeshim.ControlRetryDelay = 3*time.Second, 10*time.Millisecond
	t.Cleanup(func() { bridgeshim.ControlRetryWindow, bridgeshim.ControlRetryDelay = oldW, oldD })
	dir, err := os.MkdirTemp("/tmp", "cbl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "bridge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	hellos := 0
	endControl := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if _, err := bridgewire.ReadHello(c); err != nil {
					return
				}
				mu.Lock()
				hellos++
				n := hellos
				mu.Unlock()
				if n < 3 { // the cgroup file is not there yet
					_, _ = c.Write(bridgewire.FormatErr(bridgewire.CodeRefused, "not admitted yet"))
					return
				}
				_, _ = c.Write(bridgewire.OK())
				<-endControl // the stream ends when the test says so
			}()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errb safeBuffer
	done := make(chan int, 1)
	go func() { done <- runBridgeListenCtx(ctx, []string{"--socket", sock}, &out, &errb) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "bridge-listen: ready") {
		if time.Now().After(deadline) {
			t.Fatalf("no ready line: out %q err %q", out.String(), errb.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for { // control is held once the third hello was answered OK
		mu.Lock()
		n := hellos
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("control never held: %d hellos, err %q", n, errb.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(errb.String(), "the bridge is down") || strings.Contains(errb.String(), "no control stream") {
		t.Fatalf("said down or no-control while control was held: %q", errb.String())
	}
	close(endControl) // the server ends control: the bridge is down
	for !strings.Contains(errb.String(), "clown: the bridge is down (EOF on control)") {
		if time.Now().After(deadline) {
			t.Fatalf("the down text did not appear: %q", errb.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit %d", code)
	}
}

// safeBuffer is a bytes.Buffer safe for a writer goroutine and a polling test.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestAwaitSocketSeesTheServesSocketAndGivesUp(t *testing.T) {
	if (sessionBridge{}).awaitSocket(time.Millisecond) {
		t.Error("a zero bridge is never ready")
	}
	dir, err := os.MkdirTemp("/tmp", "cbw")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	b := sessionBridge{Dir: dir, Socket: filepath.Join(dir, "bridge.sock")}
	start := time.Now()
	if b.awaitSocket(120 * time.Millisecond) {
		t.Error("no socket yet, but ready")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Error("gave up before the timeout")
	}
	// A regular file there is not a socket.
	if err := os.WriteFile(b.Socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if b.awaitSocket(60 * time.Millisecond) {
		t.Error("a regular file counted as the socket")
	}
	_ = os.Remove(b.Socket)
	// The serve creating it a moment later is seen.
	go func() {
		time.Sleep(100 * time.Millisecond)
		if ln, err := net.Listen("unix", b.Socket); err == nil {
			t.Cleanup(func() { _ = ln.Close() })
		}
	}()
	if !b.awaitSocket(3 * time.Second) {
		t.Error("the socket appeared but was not seen")
	}
}

func TestABridgeUnderAWritableMountIsDroppedNotTheLaunch(t *testing.T) {
	rt := t.TempDir()
	_, b, err := routeBridge(serveFixture(nil), "/bin/bridge", rt, "k1", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	var l agentscope.Launch
	wireBridge(&l, b, nil)
	dropBridgeIfWritable(&l, b, nil)
	if l.BridgeDir != b.Dir || l.OnCgroup == nil {
		t.Fatalf("a stock launch lost its bridge: %+v", l)
	}
	if err := os.WriteFile(b.Socket, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.Spec.BindsRW = []string{rt} // the runtime directory, read-write: covers the bridge dir
	dropBridgeIfWritable(&l, b, nil)
	if l.BridgeDir != "" || l.OnCgroup != nil || l.BridgeReady != nil {
		t.Errorf("the bridge survived a writable mount over it: %+v", l)
	}
	if _, err := os.Lstat(b.Socket); !os.IsNotExist(err) {
		t.Errorf("the dropped bridge's socket was left for the orphaned listener: %v", err)
	}

	// A writable mount of ONLY the cgroup file (the trust anchor) drops it too
	// (round 1, A-M2).
	var l2 agentscope.Launch
	wireBridge(&l2, b, nil)
	l2.Spec.BindsRW = []string{b.CgroupFile}
	dropBridgeIfWritable(&l2, b, nil)
	if l2.BridgeDir != "" {
		t.Errorf("the bridge survived a writable bind of its cgroup file: %+v", l2)
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

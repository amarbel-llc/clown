package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain lets a test run this binary's main() as the hook, to check what a
// real invocation prints and how it exits.
func TestMain(m *testing.M) {
	if os.Getenv("CLOWN_HOOK_TOOLCOST_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func runHook(t *testing.T, stdin string, env ...string) (stdout string, code int) {
	t.Helper()
	return runHookWithArgs(t, nil, stdin, env...)
}

func runHookWithArgs(t *testing.T, args []string, stdin string, env ...string) (stdout string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), append([]string{"CLOWN_HOOK_TOOLCOST_RUN_MAIN=1"}, env...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

// Bad arguments must not exit 2 (which blocks a Stop) or print anything: in
// hook mode they are ignored.
func TestBadArgumentsStillExitZero(t *testing.T) {
	in := `{"hook_event_name":"PermissionRequest","session_id":"s-1","tool_name":"Bash","tool_input":{"command":"touch X"}}`
	out, code := runHookWithArgs(t, []string{"-bogus"}, in, "XDG_RUNTIME_DIR="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(), "STATSD_HOST=127.0.0.1", "STATSD_PORT=9")
	if out != "" || code != 0 {
		t.Fatalf("stdout %q, exit %d; want nothing and 0", out, code)
	}
}

// clown#302: the hook runs for PermissionRequest, where any stdout would be
// read as the permission decision. It must print nothing and exit 0, on good
// input, on malformed input, and with statsd unreachable.
func TestPermissionRequestHookPrintsNothing(t *testing.T) {
	runtime := t.TempDir()
	for name, in := range map[string]string{
		"valid":      `{"hook_event_name":"PermissionRequest","session_id":"s-1","tool_name":"Bash","tool_input":{"command":"touch X"}}`,
		"malformed":  `{"hook_event_name":"PermissionRequest",`,
		"empty":      ``,
		"no session": `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			out, code := runHook(t, in, "XDG_RUNTIME_DIR="+runtime, "XDG_STATE_HOME="+runtime, "STATSD_HOST=127.0.0.1", "STATSD_PORT=9")
			if out != "" || code != 0 {
				t.Fatalf("stdout %q, exit %d; want nothing and 0", out, code)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("took %v", took)
			}
		})
	}
}

// The counters reach statsd: a prompt then its PostToolUse.
func TestPermissionCountersAreSent(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	env := []string{"XDG_RUNTIME_DIR=" + t.TempDir(), "XDG_STATE_HOME=" + t.TempDir(), "STATSD_HOST=127.0.0.1", "STATSD_PORT=" + itoa(port)}
	tool := `"tool_name":"mcp__plugin_moxy_moxy__rg_search","tool_input":{"pattern":"x"}`
	for _, ev := range []string{"PermissionRequest", "PostToolUse"} {
		if out, code := runHook(t, `{"hook_event_name":"`+ev+`","session_id":"s-2",`+tool+`}`, env...); out != "" || code != 0 {
			t.Fatalf("%s: stdout %q, exit %d", ev, out, code)
		}
	}
	var got []string
	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(got) < 2 {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got = append(got, strings.Split(string(buf[:n]), "\n")...)
	}
	want := []string{"clown.permission.moxy.rg_search.prompted:1|c", "clown.permission.moxy.rg_search.approved:1|c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

// clown#302: as inside an --agent-scope unit, XDG_RUNTIME_DIR is read-only
// (a tmpfs under ProtectHome=tmpfs) while XDG_STATE_HOME/clown/hook-toolcost
// is writable (bound in): all three counters reach statsd, nothing is
// printed, and every hook exits 0.
func TestReadOnlyRuntimeDirStillCountsAllThree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("needs a non-root user: root can create the dir under a read-only parent")
	}
	runtime := t.TempDir()
	if err := os.Chmod(runtime, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(runtime, 0o700) })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	env := []string{"XDG_RUNTIME_DIR=" + runtime, "XDG_STATE_HOME=" + t.TempDir(), "STATSD_HOST=127.0.0.1", "STATSD_PORT=" + itoa(port)}
	tool := `"tool_name":"Bash","tool_input":{"command":"touch X"}`
	for _, ev := range []string{"PermissionRequest", "PostToolUse", "PermissionRequest", "UserPromptSubmit"} {
		if out, code := runHook(t, `{"hook_event_name":"`+ev+`","session_id":"s-3",`+tool+`}`, env...); out != "" || code != 0 {
			t.Fatalf("%s: stdout %q, exit %d; want nothing and 0", ev, out, code)
		}
	}
	var got []string
	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			break // the deadline: nothing more was sent
		}
		got = append(got, strings.Split(string(buf[:n]), "\n")...)
	}
	want := []string{
		"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c",
		"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.declined:1|c",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func itoa(n int) string {
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

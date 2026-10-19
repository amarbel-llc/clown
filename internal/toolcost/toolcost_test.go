package toolcost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixture builders --------------------------------------------------------

type fixture struct{ lines []string }

func (f *fixture) add(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	f.lines = append(f.lines, string(b))
}

func (f *fixture) String() string { return strings.Join(f.lines, "\n") + "\n" }

type u struct{ in, out, cc5m, cc1h, cr, think int64 }

func usageJSON(x u) map[string]any {
	return map[string]any{
		"input_tokens":                x.in,
		"output_tokens":               x.out,
		"cache_creation_input_tokens": x.cc5m + x.cc1h,
		"cache_read_input_tokens":     x.cr,
		"cache_creation": map[string]any{
			"ephemeral_5m_input_tokens": x.cc5m,
			"ephemeral_1h_input_tokens": x.cc1h,
		},
		"output_tokens_details": map[string]any{"thinking_tokens": x.think},
	}
}

// block is one content block of an assistant record.
func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func toolUse(id, name string, input any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

// assistant writes ONE record (one content block) of response `msgID`.
func (f *fixture) assistant(msgID string, x u, stop string, block map[string]any) {
	var sr any
	if stop != "" {
		sr = stop
	}
	f.add(map[string]any{
		"type": "assistant", "requestId": "req_" + msgID,
		"message": map[string]any{
			"id": msgID, "model": "claude-opus-5-5", "stop_reason": sr,
			"usage": usageJSON(x), "content": []any{block},
		},
	})
}

func (f *fixture) result(toolUseID, content string, isError bool) {
	f.add(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{
			map[string]any{
				"type": "tool_result", "tool_use_id": toolUseID, "is_error": isError,
				"content": []any{map[string]any{"type": "text", "text": content}},
			},
		}},
	})
}

func (f *fixture) prompt(s string) {
	f.add(map[string]any{"type": "user", "message": map[string]any{"content": s}})
}

func (f *fixture) system(subtype string) {
	f.add(map[string]any{"type": "system", "subtype": subtype})
}

func writeFixture(t *testing.T, f *fixture) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(f.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var cfg = Config{Host: "h", Repo: "r"}

// sum returns the total value of metrics named exactly name.
func sum(ms []Metric, name string) (total int64, n int) {
	for _, m := range ms {
		if m.Name == name {
			total += m.Value
			n++
		}
	}
	return
}

func mustRun(t *testing.T, path string, st State, c Config) (State, []Metric) {
	t.Helper()
	st, ms, err := Run(path, st, c)
	if err != nil {
		t.Fatal(err)
	}
	return st, ms
}

const (
	rgKey      = "moxy.rg_search"
	rgToolName = "mcp__plugin_moxy_moxy__rg_search"
	lsToolName = "mcp__plugin_moxy_moxy__folio_ls"
)

// --- tests -------------------------------------------------------------------

// Several records sharing one message.id are one response: usage counts once,
// and the final (stop_reason set) record's group closes only when the next
// message opens.
func TestMultiRecordGroupCountsUsageOnce(t *testing.T) {
	var f fixture
	f.prompt("hi")
	x := u{in: 2, out: 700, cc5m: 100, cc1h: 50, cr: 1000, think: 40}
	f.assistant("m1", x, "end_turn", map[string]any{"type": "thinking", "thinking": "x"})
	f.assistant("m1", x, "end_turn", text("one"))
	f.assistant("m1", x, "end_turn", text("two"))
	f.assistant("m2", u{in: 1, out: 5, cr: 2000}, "end_turn", text("next"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	for name, want := range map[string]int64{
		"clown.tokens.h.r.opus-5-5.input":          2,
		"clown.tokens.h.r.opus-5-5.cache_read":     1000,
		"clown.tokens.h.r.opus-5-5.cache_write_5m": 100,
		"clown.tokens.h.r.opus-5-5.cache_write_1h": 50,
		"clown.tokens.h.r.opus-5-5.output":         700,
		"clown.tokens.h.r.opus-5-5.thinking":       40,
		"clown.responses.h.r.opus-5-5.end_turn":    1,
	} {
		if got, _ := sum(ms, name); got != want {
			t.Errorf("%s = %d, want %d (m1 counted once, m2 still open)", name, got, want)
		}
	}
	if got, _ := sum(ms, "clown.context.h.r.opus-5-5"); got != 2+1000+150 {
		t.Errorf("context gauge = %d, want input+cache_read+cache_write = 1152", got)
	}
}

// Parallel tool_use blocks in one response: results interleave with the
// response's own records and are all consumed by the NEXT request, whose cache
// write is split proportionally to result bytes.
func TestParallelToolUseAndProportionalCacheWriteSplit(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1, out: 10}
	f.assistant("m1", x, "tool_use", toolUse("t1", rgToolName, map[string]any{"pattern": "a"}))
	f.result("t1", strings.Repeat("a", 300), false)
	f.assistant("m1", x, "tool_use", toolUse("t2", lsToolName, map[string]any{}))
	f.result("t2", strings.Repeat("b", 100), false)
	f.assistant("m2", u{in: 1, out: 3, cc5m: 400, cr: 5000}, "end_turn", text("done"))
	f.assistant("m3", u{in: 1, out: 3}, "end_turn", text("tail"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.tool_result.h.moxy.rg_search.cache_write"); got != 300 {
		t.Errorf("rg cache_write = %d, want 300 (400 * 300/400)", got)
	}
	if got, _ := sum(ms, "clown.tool_result.h.moxy.folio_ls.cache_write"); got != 100 {
		t.Errorf("ls cache_write = %d, want 100", got)
	}
	if got, n := sum(ms, "clown.tool_result.h.moxy.rg_search.result_bytes"); got != 300 || n != 1 {
		t.Errorf("rg result_bytes = %d over %d samples, want one 300 timer sample", got, n)
	}
	if got, _ := sum(ms, "clown.tool_result.h.moxy.rg_search.ok"); got != 1 {
		t.Errorf("rg ok = %d, want 1", got)
	}
}

// A full cache miss (cache_read == 0) rewrote the whole context; none of that
// write is attributed to the results the request consumed.
func TestFullCacheMissIsNotAttributedToResults(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1}
	f.assistant("m1", x, "tool_use", toolUse("t1", rgToolName, map[string]any{}))
	f.result("t1", "some output", false)
	f.assistant("m2", u{in: 1, cc5m: 230000, cr: 0}, "end_turn", text("rebuilt"))
	f.assistant("m3", x, "end_turn", text("tail"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	if got, n := sum(ms, "clown.tool_result.h.moxy.rg_search.cache_write"); got != 0 || n != 0 {
		t.Errorf("cache_write = %d (%d samples), want none on a full-miss request", got, n)
	}
	if got, _ := sum(ms, "clown.tokens.h.r.opus-5-5.cache_write_5m"); got != 230000 {
		t.Errorf("the request-level cache_write_5m must still count: got %d", got)
	}
}

func TestIsErrorAndSpilledResults(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1}
	f.assistant("m1", x, "tool_use", toolUse("t1", rgToolName, map[string]any{}))
	f.assistant("m1", x, "tool_use", toolUse("t2", lsToolName, map[string]any{}))
	f.result("t1", "boom", true)
	f.result("t2", "Error: result (99 characters) exceeds maximum allowed tokens. Output has been saved to /x/tool-results/y.txt", true)
	f.assistant("m2", x, "end_turn", text("a"))
	f.assistant("m3", x, "end_turn", text("b"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.tool_result.h.moxy.rg_search.error"); got != 1 {
		t.Errorf("rg error = %d, want 1", got)
	}
	// The truncation notice is spilled even though the harness flags it
	// is_error: the more specific classification wins.
	if got, _ := sum(ms, "clown.tool_result.h.moxy.folio_ls.spilled"); got != 1 {
		t.Errorf("ls spilled = %d, want 1", got)
	}
	if got, _ := sum(ms, "clown.tool_result.h.moxy.folio_ls.error"); got != 0 {
		t.Errorf("ls error = %d, want 0 (spilled wins)", got)
	}
}

// Calling the same tool in the immediately following response is a retry;
// a different tool in between is not.
func TestRetry(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1}
	f.assistant("m1", x, "tool_use", toolUse("t1", rgToolName, map[string]any{}))
	f.result("t1", "x", false)
	f.assistant("m2", x, "tool_use", toolUse("t2", rgToolName, map[string]any{}))
	f.result("t2", "x", false)
	f.assistant("m3", x, "tool_use", toolUse("t3", lsToolName, map[string]any{}))
	f.result("t3", "x", false)
	f.assistant("m4", x, "tool_use", toolUse("t4", rgToolName, map[string]any{}))
	f.result("t4", "x", false)
	f.assistant("m5", x, "end_turn", text("end"))
	f.assistant("m6", x, "end_turn", text("end"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.tool_result.h.moxy.rg_search.retry"); got != 1 {
		t.Errorf("rg retry = %d, want 1 (m2 follows m1; m4 follows ls, not rg)", got)
	}
	if got, _ := sum(ms, "clown.tool_result.h.moxy.folio_ls.retry"); got != 0 {
		t.Errorf("ls retry = %d, want 0", got)
	}
}

// carry_tokens: a result of 400 bytes (100 tokens) consumed by m2 is
// re-read by every later request, until a compact_boundary drops it.
func TestCarryTokensAndCompaction(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1}
	f.assistant("m1", x, "tool_use", toolUse("t1", rgToolName, map[string]any{}))
	f.result("t1", strings.Repeat("a", 400), false)
	f.assistant("m2", x, "end_turn", text("consumed"))
	f.assistant("m3", x, "end_turn", text("carry 1"))
	f.assistant("m4", x, "end_turn", text("carry 2"))
	f.system("compact_boundary")
	f.assistant("m5", x, "end_turn", text("after compaction"))
	f.assistant("m6", x, "end_turn", text("tail"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.tool_result.h.moxy.rg_search.carry_tokens"); got != 200 {
		t.Errorf("carry_tokens = %d, want 200 (100 tokens x m3,m4; m2 consumed it; m5 is post-compaction)", got)
	}
	if got, _ := sum(ms, "clown.compaction.h.r.opus-5-5"); got != 1 {
		t.Errorf("compaction = %d, want 1", got)
	}
}

// Re-running on an already-processed transcript emits nothing new; appending
// emits only the new groups; a run that ends mid-group counts that group
// exactly once when it later completes.
func TestIdempotentAndIncremental(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1, out: 10}
	f.assistant("m1", x, "end_turn", text("a"))
	f.assistant("m2", x, "tool_use", toolUse("t1", rgToolName, map[string]any{}))
	p := writeFixture(t, &f)

	st, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.end_turn"); got != 1 {
		t.Fatalf("first run end_turn = %d, want 1 (m1 closed, m2 open)", got)
	}
	st2, ms2 := mustRun(t, p, st, cfg)
	if len(ms2) != 0 || st2.Offset != st.Offset {
		t.Fatalf("re-run emitted %v (offset %d -> %d), want nothing", ms2, st.Offset, st2.Offset)
	}

	// m2's remaining records and result land, then m3 opens.
	f.assistant("m2", x, "tool_use", text("more of m2"))
	f.result("t1", strings.Repeat("a", 40), false)
	f.assistant("m3", x, "end_turn", text("c"))
	if err := os.WriteFile(p, []byte(f.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	st3, ms3 := mustRun(t, p, st2, cfg)
	if got, _ := sum(ms3, "clown.responses.h.r.opus-5-5.tool_use"); got != 1 {
		t.Errorf("m2 tool_use responses = %d, want exactly 1", got)
	}
	if got, _ := sum(ms3, "clown.responses.h.r.opus-5-5.end_turn"); got != 0 {
		t.Errorf("m1 re-counted: end_turn = %d, want 0 in the second delta", got)
	}
	// m3 consumed t1's result.
	if got, _ := sum(ms3, "clown.tool_result.h.moxy.rg_search.ok"); got != 0 {
		t.Errorf("t1 ok = %d before m3 closes, want 0", got)
	}
	f.assistant("m4", x, "end_turn", text("d"))
	if err := os.WriteFile(p, []byte(f.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ms4 := mustRun(t, p, st3, cfg)
	if got, _ := sum(ms4, "clown.tool_result.h.moxy.rg_search.ok"); got != 1 {
		t.Errorf("t1 ok = %d after m3 closes, want 1 (consumed exactly once)", got)
	}
}

// A torn trailing line (no newline, invalid JSON) is left for the next run.
func TestTornTrailingLineIsLeftUnconsumed(t *testing.T) {
	var f fixture
	f.prompt("go")
	x := u{in: 1}
	f.assistant("m1", x, "end_turn", text("a"))
	f.assistant("m2", x, "end_turn", text("b"))
	p := writeFixture(t, &f)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(fh, `{"type":"assistant","message":{"id":"m3","mod`)
	fh.Close()

	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.end_turn"); got != 1 {
		t.Errorf("end_turn = %d, want 1 (m1 only; m2 open, torn m3 ignored)", got)
	}
}

// A Final flush still closes the trailing group when the file ends in a torn
// line: the torn tail is the effective end of the transcript.
func TestFinalFlushesBeforeTornTrailingLine(t *testing.T) {
	var f fixture
	f.prompt("go")
	f.assistant("m1", u{in: 1, out: 4}, "end_turn", text("only"))
	p := writeFixture(t, &f)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(fh, `{"type":"assistant","mess`)
	fh.Close()

	c := cfg
	c.Final = true
	st, ms := mustRun(t, p, State{}, c)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.end_turn"); got != 1 {
		t.Errorf("end_turn = %d, want 1 (final flush despite the torn tail)", got)
	}
	// The torn line stays unconsumed for a later run.
	info, _ := os.Stat(p)
	if st.Offset >= info.Size() {
		t.Errorf("offset %d consumed the torn tail (size %d)", st.Offset, info.Size())
	}
}

// Final (SessionEnd/SubagentStop) closes the trailing group.
func TestFinalClosesTrailingGroup(t *testing.T) {
	var f fixture
	f.prompt("go")
	f.assistant("m1", u{in: 1, out: 9}, "end_turn", text("only"))
	p := writeFixture(t, &f)

	_, open := mustRun(t, p, State{}, cfg)
	if len(open) != 0 {
		t.Fatalf("non-final run emitted %v for a lone open group", open)
	}
	c := cfg
	c.Final = true
	st, ms := mustRun(t, p, State{}, c)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.end_turn"); got != 1 {
		t.Errorf("final run end_turn = %d, want 1", got)
	}
	if _, again := mustRun(t, p, st, c); len(again) != 0 {
		t.Errorf("re-run after final emitted %v", again)
	}
}

// A subagent transcript's opening-only usage (null stop_reason) still counts,
// under stop_reason "none".
func TestNullStopReasonIsNone(t *testing.T) {
	var f fixture
	f.prompt("go")
	f.assistant("m1", u{in: 3, out: 2}, "", text("a"))
	f.assistant("m2", u{in: 3, out: 2}, "", text("b"))
	p := writeFixture(t, &f)
	_, ms := mustRun(t, p, State{}, cfg)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.none"); got != 1 {
		t.Errorf("none responses = %d, want 1", got)
	}
}

func TestStaleOffsetRestartsFromZero(t *testing.T) {
	var f fixture
	f.prompt("go")
	f.assistant("m1", u{in: 1}, "end_turn", text("a"))
	f.assistant("m2", u{in: 1}, "end_turn", text("b"))
	p := writeFixture(t, &f)
	_, ms := mustRun(t, p, State{Version: stateVersion, Offset: 1 << 30}, cfg)
	if got, _ := sum(ms, "clown.responses.h.r.opus-5-5.end_turn"); got != 1 {
		t.Errorf("end_turn = %d, want 1 after restart from zero", got)
	}
}

func TestToolKey(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"mcp__plugin_moxy_moxy__rg_search", `{}`, "moxy.rg_search"},
		{"mcp__plugin_moxy_moxy__jq_jq", `{"filter":"."}`, "moxy.jq_jq"},
		{"mcp__plugin_moxy_moxy__folio_read", `{"file_path":"/a/b.go"}`, "moxy.folio_read"},
		{"mcp__plugin_moxy_moxy__chix_develop-run", `{"commands":[{"command":"/usr/bin/go","args":["test"]}]}`, "moxy.chix_develop-run.go"},
		{"mcp__plugin_moxy_moxy__chix_develop-run", `{"commands":[]}`, "moxy.chix_develop-run"},
		{"mcp__plugin_just-us_just-us__run_recipe", `{"recipe":"dir/test.unit"}`, "just-us.run_recipe.dir_test_unit"},
		{"mcp__plugin_just-us_just-us__run_recipe", `{}`, "just-us.run_recipe"},
		{"mcp__plugin_just-us_just-us__list_recipes", `{}`, "just-us.list_recipes"},
		{"mcp__claude_ai_Krone__cutting-garden_list_nodes", `{}`, "claude_ai_Krone.cutting-garden_list_nodes"},
		{"Read", `{"file_path":"/x"}`, "builtin.Read"},
		{"Agent", `{}`, "builtin.Agent"},
	} {
		if got := ToolKey(tc.name, json.RawMessage(tc.input)); got != tc.want {
			t.Errorf("ToolKey(%q, %s) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestSplitProportionalSumsExactly(t *testing.T) {
	for _, tc := range []struct {
		total   int64
		weights []int64
	}{
		{100, []int64{1, 1, 1}},
		{7, []int64{5, 0, 3}},
		{10, []int64{0, 0}},
		{0, []int64{4, 4}},
		{1000003, []int64{17, 29, 4093}},
	} {
		var got int64
		for _, s := range splitProportional(tc.total, tc.weights) {
			got += s
		}
		want := tc.total
		if got != want {
			t.Errorf("split(%d, %v) sums to %d", tc.total, tc.weights, got)
		}
	}
}

func TestEncodeAggregatesAndChunks(t *testing.T) {
	lines := Encode([]Metric{
		{"a.c", KindCounter, 2},
		{"a.c", KindCounter, 3},
		{"a.g", KindGauge, 1},
		{"a.g", KindGauge, 9},
		{"a.t", KindTimer, 5},
		{"a.t", KindTimer, 7},
	})
	want := []string{"a.c:5|c", "a.g:9|g", "a.t:5|ms", "a.t:7|ms"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Errorf("Encode = %v, want %v", lines, want)
	}
}

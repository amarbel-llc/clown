// Package toolcost turns a Claude Code transcript JSONL into statsd metrics
// answering "which MCP tools inject the most context, and what does that cost
// in cache writes and carried tokens" (clown-hook-toolcost is its hook-side
// driver).
//
// The transcript is the only per-tool source: hook payloads carry no usage.
// One API response is written as several records (one per content block) that
// all share message.id and repeat the same message.usage, so records are
// GROUPED by message.id and usage is counted once per group. Tool results
// land as `user` records carrying tool_result blocks keyed by tool_use_id —
// interleaved INSIDE a still-streaming group's records, so they are matched by
// id, never by position. The request that first consumes a result is the first
// group that opens after the result was written; that is where the result's
// size, outcome and share of the request's cache write are attributed.
// (attributionMcpTool is deliberately NOT used: it lags one call behind.)
//
// Incremental, idempotent processing: Claude Code flushes the transcript
// lazily, so the trailing group at any Stop may be incomplete. Processing
// therefore COMMITS only at group boundaries — State.Offset points at the
// first record of the not-yet-closed trailing group, and the State saved
// alongside it is the state as of that point. The next run re-reads the
// trailing group from its start, so a group is counted exactly once, and
// re-running on an unchanged transcript emits nothing.
//
// Metrics emitted (statsd; dimensions are name segments, no session id;
// <host> = hostname, <repo> = worktree-aware repo basename, <model> = model id
// without its "claude-" prefix; <key> = "<server>.<tool>[.<subkey>]", see
// ToolKey and its subkey allowlist):
//
//	clown.tool_result.<host>.<key>.result_bytes:N|ms      per consumed result
//	clown.tool_result.<host>.<key>.cache_write:N|c        share of the consuming request's cache write
//	clown.tool_result.<host>.<key>.carry_tokens:N|c       result tokens x later requests in context
//	clown.tool_result.<host>.<key>.{ok,error,spilled}:1|c outcome
//	clown.tool_result.<host>.<key>.retry:1|c              same tool again in the very next response
//	clown.tokens.<host>.<repo>.<model>.{input,cache_read,cache_write_5m,cache_write_1h,output,thinking}:N|c
//	clown.responses.<host>.<repo>.<model>.<stop_reason>:1|c
//	clown.context.<host>.<repo>.<model>:N|g               input+cache_read+cache_write of the latest response
//	clown.compaction.<host>.<repo>.<model>:1|c
//
// Where they land in stats-me: the statsd type decides the stored prefix, so
// one family spans several prefixes. Counters (|c) appear twice, as
// stats_counts.<name> (per-flush count) and stats.<name> (per-second rate);
// timers (|ms) as stats.timers.<name>.{count,mean,upper,lower,sum,...};
// gauges (|g) as stats.gauges.<name>. result_bytes is a timer — query
// stats.timers.clown.tool_result.*.result_bytes.*, not stats_counts (clown#282).
//
// retry is NOT an error-retry count: it counts every tool_use whose key was
// also called by the immediately previous response, whatever that call's
// outcome (repeated Reads/greps across turns count). It is expected to exceed
// error; compare it against ok+error+spilled, not against error alone.
//
// Approximations (documented in the metric docs, not hidden):
//   - result tokens are estimated as bytes/4 (the transcript offers no
//     per-result token count);
//   - a request's cache write (5m+1h) is split across the results it consumed
//     proportionally to result bytes (equally if all are empty); requests that
//     consumed no results (plain user prompts) attribute their cache write to
//     nobody, and so do full-miss requests (cache_read == 0: a cache-expiry
//     or first-request rebuild of the whole context is not the results' cost);
//   - carry_tokens accrues result tokens once per request AFTER the one that
//     consumed the result, and stops at a compact_boundary (the result is no
//     longer in context).
package toolcost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const stateVersion = 1

// Statuses of a consumed tool result.
const (
	statusOK      = "ok"
	statusError   = "error"
	statusSpilled = "spilled"
)

// Metric kinds, spelled as the statsd type suffix.
const (
	KindCounter = "c"
	KindTimer   = "ms"
	KindGauge   = "g"
)

// Metric is one statsd datum before wire encoding.
type Metric struct {
	Name  string
	Kind  string
	Value int64
}

// State is everything carried between runs for one transcript.
type State struct {
	Version int `json:"version"`
	// Offset is the byte offset of the first record NOT yet committed: the
	// start of the trailing open group (or EOF after a final flush).
	Offset int64 `json:"offset"`
	// LastModel is the model of the last closed group (compaction metrics
	// carry it; a compact_boundary record has no model of its own).
	LastModel string `json:"last_model,omitempty"`
	// PrevCalls are the tool keys called by the last closed group — the
	// "immediately previous response" a retry is judged against.
	PrevCalls []string `json:"prev_calls,omitempty"`
	// PendingUses maps tool_use id -> tool key for calls whose result has not
	// been seen yet.
	PendingUses map[string]string `json:"pending_uses,omitempty"`
	// Unconsumed are results written but not yet consumed by a request.
	Unconsumed []Result `json:"unconsumed,omitempty"`
	// Live maps tool key -> summed tokens of its consumed results still in
	// context (reset on compaction); every further request adds the sum to
	// that key's carry_tokens. Bounded by the number of distinct tool keys.
	Live map[string]int64 `json:"live,omitempty"`
	// FloorDone records that the transcript's context floor (the first
	// response's total input) has been judged. A state saved before the guard
	// existed has Offset > 0 and no FloorDone; Run marks it done rather than
	// judge a later, larger context as the floor.
	FloorDone bool `json:"floor_done,omitempty"`
}

// Result is one completed tool result awaiting its consuming request.
type Result struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	Status string `json:"status"`
}

func (s State) clone() State {
	c := s
	c.PrevCalls = append([]string(nil), s.PrevCalls...)
	c.Unconsumed = append([]Result(nil), s.Unconsumed...)
	c.Live = make(map[string]int64, len(s.Live))
	for k, v := range s.Live {
		c.Live[k] = v
	}
	c.PendingUses = make(map[string]string, len(s.PendingUses))
	for k, v := range s.PendingUses {
		c.PendingUses[k] = v
	}
	return c
}

// Config parameterises one run.
type Config struct {
	// Host and Repo are the (already sanitized) name segments.
	Host, Repo string
	// Final closes the trailing group too (SessionEnd/SubagentStop: nothing
	// more will be appended, so the group is complete).
	Final bool
	// Deadline bounds the run; zero means unbounded. Hitting it commits what
	// was processed so far — the next run resumes from there.
	Deadline time.Time
	// Window is the model's context window in tokens and FloorPct the share of
	// it the first response may already occupy before auto-compact can no
	// longer get below it (it would loop): the floor guard. Zero values use
	// DefaultWindow and DefaultFloorPct.
	Window   int64
	FloorPct int
}

// DefaultWindow and DefaultFloorPct are the floor guard's defaults: a 200K
// window, warn above 60% of it.
const (
	DefaultWindow   int64 = 200000
	DefaultFloorPct       = 60
)

func (c Config) floorLimit() (window int64, pct int) {
	window, pct = c.Window, c.FloorPct
	if window <= 0 {
		window = DefaultWindow
	}
	if pct <= 0 {
		pct = DefaultFloorPct
	}
	return window, pct
}

// FloorWarning returns the one-line operator warning for a run whose metrics
// include the floor_over counter (the first response's input exceeded the
// threshold), or false when the floor is fine.
func FloorWarning(ms []Metric, cfg Config) (string, bool) {
	var floor int64
	over := false
	for _, m := range ms {
		switch {
		case strings.HasPrefix(m.Name, "clown.context_floor_over."):
			over = true
		case strings.HasPrefix(m.Name, "clown.context_floor."):
			floor = m.Value
		}
	}
	if !over {
		return "", false
	}
	window, pct := cfg.floorLimit()
	return fmt.Sprintf("clown: this session's context floor is %d tokens, over %d%% of its %d-token window; auto-compact cannot get below the floor and will loop. Trim the tool set (a leaner profile) or start a fresh session.", floor, pct, window), true
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputDetails *struct {
		Thinking int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// cacheWrite splits the request's cache creation into 5m and 1h parts; a
// usage block without the breakdown counts all of it as 5m.
func (u usage) cacheWrite() (w5m, w1h int64) {
	if u.CacheCreation != nil {
		return u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	}
	return u.CacheCreationInputTokens, 0
}

type record struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		StopReason *string         `json:"stop_reason"`
		Usage      *usage          `json:"usage"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

type group struct {
	id         string
	model      string
	stopReason string
	usage      *usage
	calls      []string // tool keys, one per tool_use block
	consumes   []Result // results this request is the first to read
}

type runner struct {
	cfg  Config
	w    State // working state
	open *group

	committed       State
	committedOffset int64
	pending         []Metric // emitted since the last commit
	committedOut    []Metric
}

// Run processes transcript `path` from st.Offset and returns the committed
// state and the metrics that state's advance represents. The caller persists
// the state, then emits the metrics. A State whose Offset lies beyond EOF
// (transcript replaced/truncated) or whose Version is stale restarts from zero.
func Run(path string, st State, cfg Config) (State, []Metric, error) {
	f, err := os.Open(path)
	if err != nil {
		return st, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return st, nil, err
	}
	if st.Version != stateVersion || st.Offset > info.Size() {
		st = State{Version: stateVersion}
	}
	if st.Offset > 0 {
		st.FloorDone = true // saved before the guard, or past the first response
	}
	if _, err := f.Seek(st.Offset, io.SeekStart); err != nil {
		return st, nil, err
	}

	r := &runner{cfg: cfg, w: st.clone(), committed: st.clone(), committedOffset: st.Offset}
	br := bufio.NewReaderSize(f, 256*1024)
	pos := st.Offset
	reachedEOF := false
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] != '\n' && !json.Valid(line) {
				// Torn in-flight append: leave it for the next run. For a
				// Final flush it is the effective end of the transcript.
				reachedEOF = true
				break
			}
			r.handle(line, pos)
			pos += int64(len(line))
		}
		if readErr == io.EOF {
			reachedEOF = true
			break
		}
		if readErr != nil {
			return st, nil, readErr
		}
		if !cfg.Deadline.IsZero() && time.Now().After(cfg.Deadline) {
			break
		}
	}
	if cfg.Final && reachedEOF {
		if r.open != nil {
			r.closeOpen()
		}
		r.commit(pos)
	}
	out := r.committed
	out.Version = stateVersion
	out.Offset = r.committedOffset
	return out, r.committedOut, nil
}

func (r *runner) commit(offset int64) {
	r.committed = r.w.clone()
	r.committedOffset = offset
	r.committedOut = append(r.committedOut, r.pending...)
	r.pending = nil
}

func (r *runner) emit(name, kind string, v int64) {
	r.pending = append(r.pending, Metric{Name: name, Kind: kind, Value: v})
}

func (r *runner) handle(line []byte, pos int64) {
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	switch rec.Type {
	case "assistant":
		r.assistant(&rec, pos)
	case "user":
		r.user(&rec)
	case "system":
		if rec.Subtype == "compact_boundary" {
			// The boundary ends the open request (it was the last to see the
			// pre-compaction context): close and commit it BEFORE dropping
			// the live results, or its carry would be lost.
			if r.open != nil {
				r.closeOpen()
				r.commit(pos)
			}
			r.w.Live = map[string]int64{}
			if r.w.LastModel != "" {
				r.emit("clown.compaction."+r.cfg.Host+"."+r.cfg.Repo+"."+r.w.LastModel, KindCounter, 1)
			}
		}
	}
}

func (r *runner) assistant(rec *record, pos int64) {
	id := rec.Message.ID
	if id == "" {
		return
	}
	if r.open == nil || r.open.id != id {
		if r.open != nil {
			r.closeOpen()
		}
		// Commit BEFORE opening: the snapshot must predate the capture of
		// Unconsumed below, so a replay of this group sees them again.
		r.commit(pos)
		r.open = &group{id: id, consumes: r.w.Unconsumed}
		r.w.Unconsumed = nil
	}
	g := r.open
	if rec.Message.Model != "" {
		g.model = rec.Message.Model
	}
	if rec.Message.StopReason != nil {
		g.stopReason = *rec.Message.StopReason
	}
	if rec.Message.Usage != nil {
		g.usage = rec.Message.Usage // every record repeats it; the last is the most complete
	}
	var blocks []struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" || b.ID == "" {
			continue
		}
		key := ToolKey(b.Name, b.Input)
		r.w.PendingUses[b.ID] = key
		g.calls = append(g.calls, key)
	}
}

func (r *runner) user(rec *record) {
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return // a plain-string prompt
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		key, ok := r.w.PendingUses[b.ToolUseID]
		if !ok {
			continue
		}
		delete(r.w.PendingUses, b.ToolUseID)
		size, spilled := resultSize(b.Content)
		status := statusOK
		switch {
		case spilled:
			status = statusSpilled
		case b.IsError:
			status = statusError
		}
		r.w.Unconsumed = append(r.w.Unconsumed, Result{Key: key, Bytes: size, Status: status})
	}
}

// resultSize returns the byte size of a tool_result's content (text blocks by
// text length, other blocks by their serialized length) and whether it is the
// harness's truncation notice ("exceeds maximum allowed tokens … Output has
// been saved to …/tool-results/…"), which is treated as `spilled`.
func resultSize(content json.RawMessage) (size int64, spilled bool) {
	var text strings.Builder
	var s string
	if json.Unmarshal(content, &s) == nil {
		size = int64(len(s))
		text.WriteString(s)
	} else {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			return int64(len(content)), false
		}
		for _, raw := range blocks {
			var b struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &b) == nil && b.Type == "text" {
				size += int64(len(b.Text))
				text.WriteString(b.Text)
			} else {
				size += int64(len(raw))
			}
		}
	}
	t := text.String()
	return size, strings.Contains(t, "exceeds maximum allowed tokens") && strings.Contains(t, "Output has been saved")
}

// closeOpen emits the per-response and per-result metrics for the open group
// (called when the next group opens, or on a final flush) and clears it.
func (r *runner) closeOpen() {
	g := r.open
	r.open = nil
	host, repo := r.cfg.Host, r.cfg.Repo

	var requestCacheWrite int64
	if g.usage != nil && g.model != "" && g.model != "<synthetic>" {
		model := modelSegment(g.model)
		base := host + "." + repo + "." + model
		u := g.usage
		w5m, w1h := u.cacheWrite()
		requestCacheWrite = w5m + w1h
		for _, t := range []struct {
			name string
			v    int64
		}{
			{"input", u.InputTokens},
			{"cache_read", u.CacheReadInputTokens},
			{"cache_write_5m", w5m},
			{"cache_write_1h", w1h},
			{"output", u.OutputTokens},
			{"thinking", thinkingTokens(u)},
		} {
			if t.v > 0 {
				r.emit("clown.tokens."+base+"."+t.name, KindCounter, t.v)
			}
		}
		stop := g.stopReason
		if stop == "" {
			stop = "none" // a subagent transcript records only the stream's opening
		}
		r.emit("clown.responses."+base+"."+Segment(stop), KindCounter, 1)
		total := u.InputTokens + u.CacheReadInputTokens + requestCacheWrite
		r.emit("clown.context."+base, KindGauge, total)
		if !r.w.FloorDone {
			// The first response of a session carries only the static prefix,
			// so its total input IS the context floor.
			r.w.FloorDone = true
			r.emit("clown.context_floor."+base, KindGauge, total)
			if window, pct := r.cfg.floorLimit(); total*100 > window*int64(pct) {
				r.emit("clown.context_floor_over."+base, KindCounter, 1)
			}
		}
		r.w.LastModel = model
	}

	// Carry first: this request re-read every result consumed by an EARLIER
	// one. Results consumed by this request start carrying from the next.
	for key, tokens := range r.w.Live {
		if tokens > 0 {
			r.emit(toolMetric(host, key, "carry_tokens"), KindCounter, tokens)
		}
	}

	weights := make([]int64, len(g.consumes))
	for i, c := range g.consumes {
		weights[i] = c.Bytes
	}
	// A request that read nothing from cache re-wrote the WHOLE context (cache
	// expiry, a model switch, the first request): that write is not the cost of
	// the results it happened to consume, so none of it is attributed.
	attributable := requestCacheWrite
	if g.usage == nil || g.usage.CacheReadInputTokens == 0 {
		attributable = 0
	}
	shares := splitProportional(attributable, weights)
	for i, c := range g.consumes {
		r.emit(toolMetric(host, c.Key, "result_bytes"), KindTimer, c.Bytes)
		r.emit(toolMetric(host, c.Key, c.Status), KindCounter, 1)
		if shares[i] > 0 {
			r.emit(toolMetric(host, c.Key, "cache_write"), KindCounter, shares[i])
		}
		r.w.Live[c.Key] += c.Bytes / 4
	}

	prev := make(map[string]bool, len(r.w.PrevCalls))
	for _, k := range r.w.PrevCalls {
		prev[k] = true
	}
	seen := map[string]bool{}
	var calls []string
	for _, k := range g.calls {
		if prev[k] {
			r.emit(toolMetric(host, k, "retry"), KindCounter, 1)
		}
		if !seen[k] {
			seen[k] = true
			calls = append(calls, k)
		}
	}
	r.w.PrevCalls = calls
}

func thinkingTokens(u *usage) int64 {
	if u.OutputDetails == nil {
		return 0
	}
	return u.OutputDetails.Thinking
}

func toolMetric(host, key, suffix string) string {
	return "clown.tool_result." + host + "." + key + "." + suffix
}

// splitProportional divides total across weights proportionally (cumulative
// floor, so the shares sum to total exactly). All-zero weights split equally.
func splitProportional(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	if len(weights) == 0 || total <= 0 {
		return out
	}
	var sum int64
	for _, w := range weights {
		sum += w
	}
	if sum == 0 {
		weights = make([]int64, len(weights))
		for i := range weights {
			weights[i] = 1
		}
		sum = int64(len(weights))
	}
	var cum, prevShare int64
	for i, w := range weights {
		cum += w
		share := total * cum / sum
		out[i] = share - prevShare
		prevShare = share
	}
	return out
}

// String renders a metric in statsd line form, for logs and tests.
func (m Metric) String() string { return fmt.Sprintf("%s:%d|%s", m.Name, m.Value, m.Kind) }

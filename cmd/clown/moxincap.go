package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// A per-session call cap for a frontend-served moxin (clown#325 follow-up).
//
// The issue-filer takes caller-chosen text, repo_hint and notify, with no
// limit on how often a confined worker may call it. The cap sits between the
// stdio bridge and `moxy serve-moxin`: `clown frontend-moxin` forwards every
// MCP message unchanged except a `tools/call` beyond the cap, which it answers
// itself with an isError result. The count is kept in a state file keyed by the
// session and the moxin, so a restart (which relaunches the frontend) does not
// reset it, and it fails closed: a counter that cannot be read or written
// refuses the call.

// defaultMoxinMaxCalls is the cap per window when a frontendMoxin names none.
const defaultMoxinMaxCalls = 10

// moxinCapWindow is the sliding window the cap applies to.
const moxinCapWindow = time.Hour

// moxinLedgerMaxAge is how long a ledger may go without a call before it is
// pruned. The cap's window is an hour, so a ledger untouched for a week counts
// nothing; this is far longer, so a session that restarts or resumes later
// keeps its count within the window.
const moxinLedgerMaxAge = 7 * 24 * time.Hour

// pruneMoxinLedgers removes ledgers (and their locks) in keep's directory that
// no call has touched for moxinLedgerMaxAge, except keep itself, and reports how
// many files it removed. Housekeeping: errors are ignored.
func pruneMoxinLedgers(keep string, now time.Time) int {
	return toolcost.PruneState(filepath.Dir(keep), moxinLedgerMaxAge, now, keep)
}

// callLedger counts a session's calls to one moxin in a sliding window.
type callLedger struct {
	path   string
	max    int
	window time.Duration
	now    func() time.Time
}

// moxinLedgerPath is the state file for one session and moxin:
// $XDG_STATE_HOME/clown/moxin-calls/<sha256(session)>-<moxin>.json.
func moxinLedgerPath(session, moxin string) (string, error) {
	sum := sha256.Sum256([]byte(session))
	return userStateWritePath("moxin-calls", hex.EncodeToString(sum[:])+"-"+moxinFileSegment(moxin)+".json")
}

// moxinFileSegment keeps a moxin name usable as a file name segment.
func moxinFileSegment(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

// take records one call if the window still has room. When it does not, ok is
// false and retryAt is when the oldest call leaves the window. Any error
// (state dir, lock, file content) is returned and the caller refuses.
func (l *callLedger) take() (ok bool, retryAt time.Time, err error) {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return false, time.Time{}, err
	}
	// The lock is on a file of its own: the state file is replaced by rename,
	// which would swap out the inode a lock on it was held on.
	lock, err := os.OpenFile(l.path+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, time.Time{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, time.Time{}, err
	}
	var stamps []int64
	// A missing file is no calls yet. The file is only ever replaced whole
	// (below), so an empty or unreadable one is not a crash's leftover but
	// something else's doing: fail closed, naming it.
	switch b, err := os.ReadFile(l.path); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return false, time.Time{}, err
	case len(b) > 1<<20:
		return false, time.Time{}, fmt.Errorf("call counter %s is over 1 MiB", l.path)
	default:
		if err := json.Unmarshal(b, &stamps); err != nil {
			return false, time.Time{}, fmt.Errorf("call counter %s is not a list of times: %w", l.path, err)
		}
	}
	now := l.now()
	cutoff := now.Add(-l.window).UnixMilli()
	kept := stamps[:0]
	for _, s := range stamps {
		if s > cutoff {
			kept = append(kept, s)
		}
	}
	if len(kept) >= l.max {
		oldest := kept[0]
		for _, s := range kept {
			oldest = min(oldest, s)
		}
		return false, time.UnixMilli(oldest).Add(l.window), nil
	}
	kept = append(kept, now.UnixMilli())
	out, err := json.Marshal(kept)
	if err != nil {
		return false, time.Time{}, err
	}
	// Replaced whole: a temp file in the same directory, synced, then renamed
	// over the state, so a crash leaves the old count or the new one.
	tmp, err := os.CreateTemp(filepath.Dir(l.path), filepath.Base(l.path)+".tmp-*")
	if err != nil {
		return false, time.Time{}, err
	}
	_, werr := tmp.Write(out)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return false, time.Time{}, err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		_ = os.Remove(tmp.Name())
		return false, time.Time{}, err
	}
	return true, time.Time{}, nil
}

// capProxy forwards MCP messages (one JSON object per line) between a client
// and a moxin server, answering `tools/call` itself, with an isError result,
// when the ledger refuses.
type capProxy struct {
	name   string
	ledger *callLedger
	// onCall runs for each call let through, onRefuse for each refused.
	onCall, onRefuse func(msg string)

	outMu sync.Mutex
	out   io.Writer
}

func (p *capProxy) write(b []byte) error {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	_, err := p.out.Write(append(b, '\n'))
	return err
}

func jsonRPCResult(id json.RawMessage, text string, isError bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isError},
	})
	return b
}

// refusalText is what the worker reads when its call is refused.
func (p *capProxy) refusalText(retryAt time.Time, err error) string {
	if err != nil {
		return fmt.Sprintf("%s: this call was refused because the session's call counter cannot be kept (%v). Nothing was sent. Ask your coordinator to do it.", p.name, err)
	}
	return fmt.Sprintf("%s: call limit reached (%d per %s for this session). Nothing was sent. Try again after %s, or ask your coordinator to do it.",
		p.name, p.ledger.max, p.ledger.window, retryAt.UTC().Format("15:04 UTC"))
}

// serve reads client lines from in and writes them to toServer, except the
// calls it refuses; fromServer is copied to the client. It returns when in
// ends or the server's output ends.
func (p *capProxy) serve(in io.Reader, toServer io.Writer, fromServer io.Reader) error {
	serverDone := make(chan error, 1)
	go func() {
		r := bufio.NewReader(fromServer)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				p.outMu.Lock()
				_, werr := p.out.Write(line)
				p.outMu.Unlock()
				if werr != nil {
					serverDone <- werr
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				serverDone <- err
				return
			}
		}
	}()
	clientDone := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			line, err := readBoundedLine(r)
			if errors.Is(err, errLineTooLong) {
				_ = p.write(jsonRPCError(nil, -32600, errLineTooLong.Error()))
				clientDone <- err
				return
			}
			if len(line) > 0 {
				if ferr := p.handle(line, toServer); ferr != nil {
					clientDone <- ferr
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				clientDone <- err
				return
			}
		}
	}()
	select {
	case err := <-serverDone:
		// The server ended: there is nothing left to forward to.
		return err
	case err := <-clientDone:
		// The client closed: close the server's input and let it finish what
		// it has.
		if c, ok := toServer.(io.Closer); ok {
			_ = c.Close()
		}
		if err != nil {
			return err
		}
		return <-serverDone
	}
}

// handle forwards one client line or answers it.
func (p *capProxy) handle(line []byte, toServer io.Writer) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	// Fail closed: a line is forwarded only when it is exactly one JSON object
	// this proxy understood. Anything else (a batch, several objects on a line,
	// half an object, trailing text, a repeated or differently cased key) is
	// refused rather than forwarded, because a server that reads with a
	// streaming decoder could otherwise run a tools/call this proxy never saw.
	id, method, perr := parseClientLine(line)
	if perr != nil {
		return p.write(jsonRPCError(nil, -32700, "refused: each message must be exactly one JSON object on one line ("+perr.Error()+")"))
	}
	m := struct{ ID json.RawMessage }{ID: id}
	if method != "tools/call" {
		_, err := toServer.Write(line)
		return err
	}
	ok, retryAt, lerr := p.ledger.take()
	if lerr == nil && ok {
		if p.onCall != nil {
			p.onCall("")
		}
		_, err := toServer.Write(line)
		return err
	}
	text := p.refusalText(retryAt, lerr)
	if p.onRefuse != nil {
		p.onRefuse(text)
	}
	if len(m.ID) == 0 { // a notification has no answer
		return nil
	}
	return p.write(jsonRPCResult(m.ID, text, true))
}

// parseClientLine decodes line as exactly one JSON object and returns its id
// (nil when absent) and method. Keys match case-insensitively, the way Go's
// decoder reads them, a key present twice is refused, and so is anything but
// whitespace after the object.
func parseClientLine(line []byte) (id json.RawMessage, method string, err error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, "", errors.New("not a JSON object")
	}
	seenMethod, seenID := false, false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, "", err
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, "", err
		}
		switch {
		case strings.EqualFold(key, "method"):
			if seenMethod {
				return nil, "", errors.New("method appears twice")
			}
			seenMethod = true
			if err := json.Unmarshal(raw, &method); err != nil {
				return nil, "", errors.New("method is not a string")
			}
		case strings.EqualFold(key, "id"):
			if seenID {
				return nil, "", errors.New("id appears twice")
			}
			seenID = true
			id = raw
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, "", err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("text after the object")
	}
	return id, method, nil
}

// maxMCPLine bounds one client line: the unit chooses what it sends and the
// frontend buffers a line whole.
const maxMCPLine = 4 << 20

var errLineTooLong = errors.New("message line is over 4 MiB")

// readBoundedLine reads one line (the newline included) of at most
// maxMCPLine bytes.
func readBoundedLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMCPLine {
			return nil, errLineTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func jsonRPCError(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	return b
}

// runFrontendMoxinCap is `clown frontend-moxin --name N --session S
// [--max-calls K] [--window D] -- CMD ARGS...`: it runs CMD as the moxin
// server and proxies its stdio through a capProxy. Metrics: clown.moxin.<N>.calls
// and .refused; one log line per refusal.
func runFrontendMoxinCap(args []string) int {
	var name, session string
	max, window := defaultMoxinMaxCalls, moxinCapWindow
	for len(args) > 0 && args[0] != "--" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "clown frontend-moxin: %s needs a value\n", args[0])
			return 2
		}
		switch args[0] {
		case "--name":
			name = args[1]
		case "--session":
			session = args[1]
		case "--max-calls":
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "clown frontend-moxin: --max-calls %q is not a positive number\n", args[1])
				return 2
			}
			max = n
		case "--window":
			d, err := time.ParseDuration(args[1])
			if err != nil || d <= 0 {
				fmt.Fprintf(os.Stderr, "clown frontend-moxin: --window %q is not a positive duration\n", args[1])
				return 2
			}
			window = d
		default:
			fmt.Fprintf(os.Stderr, "clown frontend-moxin: unknown argument %q\n", args[0])
			return 2
		}
		args = args[2:]
	}
	if name == "" || session == "" || len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: clown frontend-moxin --name N --session S [--max-calls K] [--window D] -- CMD [ARGS...]")
		return 2
	}
	path, err := moxinLedgerPath(session, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown frontend-moxin: %v\n", err)
		return 1
	}
	pruneMoxinLedgers(path, time.Now())
	logPath, _ := userStateWritePath("close-self-run.log")
	p := &capProxy{
		name:   name,
		ledger: &callLedger{path: path, max: max, window: window, now: time.Now},
		out:    os.Stdout,
		onCall: func(string) { countMetric(metricMoxinCalls(name)) },
		onRefuse: func(text string) {
			countMetric(metricMoxinRefused(name))
			logSelfClose(logPath, "moxin %s (%s): %s", name, session, printableOneLine(text))
		},
	}
	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	in, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown frontend-moxin: %v\n", err)
		return 1
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown frontend-moxin: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clown frontend-moxin: %v\n", err)
		return 1
	}
	serveErr := p.serve(os.Stdin, in, out)
	_ = in.Close()
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	if serveErr != nil {
		fmt.Fprintf(os.Stderr, "clown frontend-moxin: %v\n", serveErr)
		return 1
	}
	return 0
}

func metricMoxinCalls(name string) string { return "clown.moxin." + moxinFileSegment(name) + ".calls" }

func metricMoxinRefused(name string) string {
	return "clown.moxin." + moxinFileSegment(name) + ".refused"
}

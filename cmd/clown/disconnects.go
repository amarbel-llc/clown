package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/buildcfg"
	"code.linenisgreat.com/clown/internal/0/disconnectlog"
	"code.linenisgreat.com/clown/internal/0/staging"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// The plugin disconnect record, frontend side (clown#344, FDR 0002 "Disconnect
// records", docs/plans/2026-11-06-clown-344-disconnect-record.md). The bridge
// writes a record when its child goes away and fails the calls in flight with
// the record's URI; the plugin host reports a bridge that died. This file is
// what the session reads them with and how it is told:
//
//   - a built-in plugin, clown-builtin-diagnostics, whose one stdio server
//     (`clown disconnects-serve`) serves the session's records as MCP
//     resources, resources only, scope frontend so a confined unit reaches it
//     over the same serve socket as ringmaster and troupe;
//   - a wake (a ringmaster job on the session's own channel, as for a restart)
//     naming the record, for a death with no call in flight;
//   - `clown disconnects`, the operator's view of the same records.

// disconnectsServerName is the diagnostics plugin's one server.
const disconnectsServerName = "disconnects"

// disconnectPollEvery paces the watcher that wakes the session.
const disconnectPollEvery = 2 * time.Second

// disconnectsDisabled is the kill switch: CLOWN_DISABLE_DISCONNECT_RECORDS=1
// turns off the records, the diagnostics server and the wake.
func disconnectsDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_DISCONNECT_RECORDS") == "1"
}

// disconnectsRoot is where the records live: $XDG_STATE_HOME/clown/disconnects.
func disconnectsRoot() (string, error) { return userStateWritePath("disconnects") }

// disconnectRecordRoot is the records directory this launch writes to and
// serves, or "" when the launch has none: switched off, --naked, a plan only,
// no session key to name the records by, or no state directory.
func disconnectRecordRoot(flags parsedFlags) string {
	if disconnectsDisabled() || flags.naked || flags.printLaunchPlan || flags.identity.Key == "" {
		return ""
	}
	root, err := disconnectsRoot()
	if err != nil {
		return ""
	}
	return root
}

// synthDiagnosticsPluginDir writes the built-in plugin that serves the
// session's disconnect records and returns its directory (under the launch's
// staging root, which removes it). One stdioServers entry, `clown
// disconnects-serve`, scope frontend. Nothing secret is written: the server
// reads only the session's own record directory.
func synthDiagnosticsPluginDir(root *staging.Root, exe, session, recordRoot string) (string, error) {
	dir, err := root.Dir("clown-diagnostics-plugin-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(map[string]any{"name": pluginhost.DiagnosticsPluginName, "version": "1"}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return "", err
	}
	cfg, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"stdioServers": map[string]any{
			disconnectsServerName: map[string]any{
				"command": exe,
				"args":    []string{"disconnects-serve", "--session", session, "--root", recordRoot},
				// Forwarded into a confined agent unit like ringmaster and
				// troupe, so a confined worker can read its own records.
				"scope": pluginhost.ScopeFrontend,
			},
		},
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), cfg, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// addDiagnosticsPlugin is the launch-time step: for a launch that records
// (recordRoot set), runs a plugin-dir provider and has the stdio bridge and its
// own executable, it synthesizes the diagnostics plugin and returns its dir
// ("" when not applicable). A failure only costs the session the resource, so
// the caller logs it and goes on.
func addDiagnosticsPlugin(root *staging.Root, flags parsedFlags, recordRoot string) (string, error) {
	if recordRoot == "" || !providerUsesPluginDirs(flags.provider) || buildcfg.StdioBridgePath == "" {
		return "", nil
	}
	exe := clownExePath()
	if exe == "" {
		return "", nil
	}
	return synthDiagnosticsPluginDir(root, exe, flags.identity.Key, recordRoot)
}

// writeBridgeExit records a bridge that died while the session ran, unless the
// bridge wrote its own record in that life (it records before it exits for
// every end it can see, so a record at or after the bridge's start means the
// death is already told). hostLog is the host's own log path.
func writeBridgeExit(root, session, hostLog string, ev pluginhost.ExitEvent) (disconnectlog.Record, bool, error) {
	startedAt := ev.StartedAt.UTC().Format(disconnectlog.TimeFormat)
	for _, e := range disconnectlog.List(root, session, ev.PluginName, ev.ServerName) {
		if e.At >= startedAt {
			return disconnectlog.Record{}, false, nil
		}
	}
	closedBy := disconnectlog.ClosedByUnknown
	if ev.Signal != "" {
		closedBy = disconnectlog.ClosedByExternal
	}
	rec, err := disconnectlog.Write(root, disconnectlog.Record{
		Session:    session,
		Plugin:     ev.PluginName,
		Server:     ev.ServerName,
		Kind:       disconnectlog.KindBridgeExit,
		ClosedBy:   closedBy,
		BridgePID:  ev.PID,
		BridgeExit: &disconnectlog.Exit{Code: ev.Code, Signal: ev.Signal},
		UptimeMS:   time.Since(ev.StartedAt).Milliseconds(),
		HostLog:    hostLog,
	})
	return rec, err == nil, err
}

// disconnectWakeMessage is the one line the session is woken with.
func disconnectWakeMessage(e disconnectlog.Entry) string {
	return fmt.Sprintf("plugin %s/%s disconnected (%s) at %s; the record is readable as the MCP resource %s (server clown-builtin-diagnostics/disconnects). Calls to that plugin in flight failed with an error that names it.", e.Plugin, e.Server, e.Kind, e.At, e.URI)
}

// watchDisconnectWakes wakes the session once for each record that appears for
// it after the watch starts (records from before are the session's history, not
// news): it polls the session's records every period until ctx ends and calls
// send for each new one, in the background so a slow wake never holds the poll.
func watchDisconnectWakes(ctx context.Context, root, session string, every time.Duration, send wakeSender, logf func(format string, args ...any)) {
	if root == "" || session == "" || send == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range disconnectlog.ListAll(root, session) {
		seen[e.URI] = true
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			entries := disconnectlog.ListAll(root, session)
			for i := len(entries) - 1; i >= 0; i-- { // oldest first
				e := entries[i]
				if seen[e.URI] {
					continue
				}
				seen[e.URI] = true
				msg := disconnectWakeMessage(e)
				wakeWG.Add(1)
				go func() {
					defer wakeWG.Done()
					if err := send(session, msg); err != nil {
						logf("disconnect wake for %s not delivered: %v", e.URI, err)
					}
				}()
			}
		}
	}()
}

// armDisconnectRecords is the launch step after the servers start: it reports a
// recording bridge that dies mid-session as a record (writeBridgeExit), and
// wakes the session for every new record. Neither does anything for a launch
// without a record root.
func armDisconnectRecords(ctx context.Context, host *pluginhost.Host, session, recordRoot, hostLog string, logf func(format string, args ...any)) {
	if recordRoot == "" || session == "" {
		return
	}
	host.WatchExits(func(ev pluginhost.ExitEvent) {
		rec, wrote, err := writeBridgeExit(recordRoot, session, hostLog, ev)
		switch {
		case err != nil:
			logf("disconnect record for the bridge of %s/%s not written: %v", ev.PluginName, ev.ServerName, err)
		case wrote:
			logf("bridge of %s/%s died (exit %d %s); record %s", ev.PluginName, ev.ServerName, ev.Code, ev.Signal, rec.URI)
		}
	})
	var send wakeSender
	if buildcfg.RingmasterPath != "" && !jobWakeupDisabled() {
		send = ringmasterWakeAs(buildcfg.RingmasterPath, "plugin-disconnect", "clown-disconnect")
	}
	watchDisconnectWakes(ctx, recordRoot, session, disconnectPollEvery, send, logf)
}

// sweepDisconnects removes the records of sessions untouched for the retention
// period (30 days), at launch.
func sweepDisconnects(root string) {
	if root != "" {
		disconnectlog.Sweep(root, time.Now(), disconnectlog.Retention)
	}
}

// ---- the MCP resource server: `clown disconnects-serve` -------------------

const disconnectsMIME = "application/json"

// runDisconnectsServe is `clown disconnects-serve --session ID --root DIR`: a
// stdio MCP server that serves the session's disconnect records as resources,
// and nothing else. It is the diagnostics plugin's one server.
func runDisconnectsServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var session, root string
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) || (args[i] != "--session" && args[i] != "--root") {
			fmt.Fprintln(stderr, "usage: clown disconnects-serve --session ID --root DIR")
			return 2
		}
		if args[i] == "--session" {
			session = args[i+1]
		} else {
			root = args[i+1]
		}
	}
	if session == "" || !filepath.IsAbs(root) {
		fmt.Fprintln(stderr, "usage: clown disconnects-serve --session ID --root DIR (an absolute path)")
		return 2
	}
	if err := serveDisconnects(stdin, stdout, root, session); err != nil {
		fmt.Fprintf(stderr, "clown disconnects-serve: %v\n", err)
		return 1
	}
	return 0
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error codes: method not found, invalid params, and MCP's resource not found.
const (
	rpcMethodNotFound   = -32601
	rpcInvalidParams    = -32602
	rpcResourceNotFound = -32002
)

// maxListedResources bounds resources/list.
const maxListedResources = 100

// serveDisconnects answers MCP requests from in with the session's records
// under root, one JSON-RPC message per line, until in ends.
func serveDisconnects(in io.Reader, out io.Writer, root, session string) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, rerr *rpcError) error {
		msg := map[string]any{"jsonrpc": "2.0", "id": id}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = result
		}
		return enc.Encode(msg)
	}
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil || req.Method == "" {
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue // a notification: nothing to answer
		}
		var err error
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			err = reply(req.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"resources": map[string]any{}},
				"serverInfo":      map[string]any{"name": "clown-disconnects", "version": buildcfg.Version},
				"instructions":    "Resources only: this session's plugin disconnect records. Read " + disconnectlog.AllURI(session) + " for the list.",
			}, nil)
		case "ping":
			err = reply(req.ID, map[string]any{}, nil)
		case "resources/list":
			err = reply(req.ID, map[string]any{"resources": listResources(root, session)}, nil)
		case "resources/templates/list":
			pluginsBase := strings.TrimSuffix(disconnectlog.AllURI(session), "/disconnects")
			err = reply(req.ID, map[string]any{"resourceTemplates": []map[string]any{
				{"uriTemplate": pluginsBase + "/{plugin}/{server}/disconnects", "name": "a plugin server's disconnect records", "mimeType": disconnectsMIME},
				{"uriTemplate": pluginsBase + "/{plugin}/{server}/disconnects/{n}", "name": "one disconnect record", "mimeType": disconnectsMIME},
			}}, nil)
		case "resources/read":
			var p struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal(req.Params, &p) != nil || p.URI == "" {
				err = reply(req.ID, nil, &rpcError{Code: rpcInvalidParams, Message: "resources/read needs a uri"})
				break
			}
			text, ok := readResource(root, session, p.URI)
			if !ok {
				err = reply(req.ID, nil, &rpcError{Code: rpcResourceNotFound, Message: "resource not found: " + p.URI})
				break
			}
			err = reply(req.ID, map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": disconnectsMIME, "text": text}}}, nil)
		default:
			err = reply(req.ID, nil, &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method})
		}
		if err != nil {
			return err
		}
	}
	return sc.Err()
}

// listResources is the aggregate list, each server that has records, and the
// newest records, capped at maxListedResources.
func listResources(root, session string) []map[string]any {
	res := []map[string]any{{"uri": disconnectlog.AllURI(session), "name": "all plugin disconnects of this session", "mimeType": disconnectsMIME}}
	entries := disconnectlog.ListAll(root, session)
	seenList := map[string]bool{}
	for _, e := range entries {
		if u := disconnectlog.ListURI(session, e.Plugin, e.Server); !seenList[u] {
			seenList[u] = true
			res = append(res, map[string]any{"uri": u, "name": e.Plugin + "/" + e.Server + " disconnects", "mimeType": disconnectsMIME})
		}
	}
	for _, e := range entries {
		if len(res) >= maxListedResources {
			break
		}
		res = append(res, map[string]any{
			"uri": e.URI, "name": fmt.Sprintf("%s/%s #%d (%s)", e.Plugin, e.Server, e.N, e.Kind),
			"description": e.At, "mimeType": disconnectsMIME,
		})
	}
	return res
}

// readResource returns the JSON text of the resource uri names, for this
// session only: another session's URI, a malformed one and a missing record
// are all "not found", the same answer, so a session cannot probe for others.
func readResource(root, session, uri string) (string, bool) {
	ref, err := disconnectlog.ParseURI(uri)
	if err != nil || ref.Principal != session {
		return "", false
	}
	switch ref.Kind {
	case disconnectlog.RefAll:
		return entriesJSON(disconnectlog.ListAll(root, session)), true
	case disconnectlog.RefList:
		return entriesJSON(disconnectlog.List(root, session, ref.Plugin, ref.Server)), true
	case disconnectlog.RefOne:
		b, err := disconnectlog.Read(root, session, ref.Plugin, ref.Server, ref.N)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

func entriesJSON(es []disconnectlog.Entry) string {
	if es == nil {
		es = []disconnectlog.Entry{}
	}
	b, _ := json.MarshalIndent(es, "", "  ")
	return string(b)
}

// ---- the operator's view: `clown disconnects` ---------------------------------

const disconnectsUsage = "usage: clown disconnects --session ID [--plugin P --server S [N]]"

// runDisconnectsVerb is `clown disconnects --session ID [--plugin P --server S
// [N]]`: the session's records, newest first, or one server's, or one record's
// JSON. It reads the same directory the diagnostics server serves.
func runDisconnectsVerb(args []string, stdout, stderr io.Writer) int {
	var session, plugin, server string
	n := 0
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--session", "--plugin", "--server":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "clown disconnects: %s needs a value\n%s\n", a, disconnectsUsage)
				return 2
			}
			i++
			switch a {
			case "--session":
				session = args[i]
			case "--plugin":
				plugin = args[i]
			default:
				server = args[i]
			}
		default:
			v, err := strconv.Atoi(a)
			if err != nil || v < 1 || n != 0 {
				fmt.Fprintf(stderr, "clown disconnects: unexpected argument %q\n%s\n", a, disconnectsUsage)
				return 2
			}
			n = v
		}
	}
	if session == "" || (plugin == "") != (server == "") || (n != 0 && plugin == "") {
		fmt.Fprintln(stderr, disconnectsUsage)
		return 2
	}
	root, err := disconnectsRoot()
	if err != nil {
		fmt.Fprintf(stderr, "clown disconnects: %v\n", err)
		return 1
	}
	if n != 0 {
		b, err := disconnectlog.Read(root, session, plugin, server, n)
		if err != nil {
			fmt.Fprintf(stderr, "clown disconnects: no record %d for %s/%s of session %s\n", n, plugin, server, session)
			return 1
		}
		_, _ = stdout.Write(b)
		return 0
	}
	var entries []disconnectlog.Entry
	if plugin != "" {
		entries = disconnectlog.List(root, session, plugin, server)
	} else {
		entries = disconnectlog.ListAll(root, session)
	}
	if len(entries) == 0 {
		fmt.Fprintf(stderr, "no disconnect records for session %s\n", session)
		return 0
	}
	for _, e := range entries {
		fmt.Fprintf(stdout, "%s/%s #%d\t%s\t%s\t%s\n", e.Plugin, e.Server, e.N, e.At, e.Kind, strings.TrimSpace(e.URI))
	}
	return 0
}

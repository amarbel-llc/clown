// Command clown-mcp-collapse fronts N upstream MCP servers and exposes them
// over streamable-HTTP as a single MCP server presenting only three generic
// tools (mcp_list, mcp_describe, mcp_call) instead of every upstream tool
// flattened into the catalog — the "collapse" that saves the agent's context.
// It speaks the clown plugin protocol handshake on its own stdout, exactly
// like clown-stdio-bridge (the single-child sibling), so clown-plugin-host
// drives it the same way. clown (Task 7) spawns the upstreams and passes their
// names+URLs; this binary only aggregates.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/mcpcollapse"
	"code.linenisgreat.com/clown/internal/mcphttp"
	"code.linenisgreat.com/clown/internal/pluginhost"
)

// perUpstreamEnumerateTimeout bounds each upstream's initialize+tools/list
// handshake during the startup fan-out. NewAggregator applies its own default
// (enumerateBudget) when passed a non-positive value, so this is simply the
// binary's explicit choice; it mirrors the aggregator's internal default.
const perUpstreamEnumerateTimeout = 3 * time.Second

func main() {
	parsed, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-mcp-collapse: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(parsed))
}

type parsedArgs struct {
	upstreams []mcpcollapse.Upstream
}

// parseArgs accepts repeated `--upstream <name>=<url>` flags, one per fronted
// MCP server. The name becomes the {server} half of every dotted tool id the
// upstream contributes; the url is where the aggregator dispatches. Only the
// FIRST '=' splits name from url, so a url carrying its own query-string '='s
// survives intact. At least one --upstream is required.
func parseArgs(args []string) (parsedArgs, error) {
	var p parsedArgs
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--upstream":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--upstream requires an argument")
			}
			up, err := parseUpstream(args[i+1])
			if err != nil {
				return p, err
			}
			p.upstreams = append(p.upstreams, up)
			i += 2
		default:
			return p, fmt.Errorf("unknown flag %q (expected --upstream name=url)", args[i])
		}
	}
	if len(p.upstreams) == 0 {
		return p, fmt.Errorf("at least one --upstream name=url is required")
	}
	return p, nil
}

// parseUpstream splits a single `name=url` spec into an Upstream. Splitting on
// the first '=' only keeps a url's own '='s (e.g. query strings) in the url.
func parseUpstream(spec string) (mcpcollapse.Upstream, error) {
	name, url, ok := strings.Cut(spec, "=")
	if !ok {
		return mcpcollapse.Upstream{}, fmt.Errorf("invalid --upstream %q (expected name=url)", spec)
	}
	if name == "" {
		return mcpcollapse.Upstream{}, fmt.Errorf("invalid --upstream %q (empty name)", spec)
	}
	if url == "" {
		return mcpcollapse.Upstream{}, fmt.Errorf("invalid --upstream %q (empty url)", spec)
	}
	return mcpcollapse.Upstream{Name: name, URL: url}, nil
}

func run(p parsedArgs) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdLogger := log.New(os.Stderr, "", 0)

	// NewAggregator BLOCKS on the fan-out — this IS the health gate. By the time
	// it returns, the registry and the degraded roster are fully populated, so
	// the handshake below (printed AFTER this) and the fragment endpoint can
	// always report the final degraded set. Only a genuine config error (two
	// upstreams sharing a server name) is fatal; enumeration failures are
	// fail-open and land in Degraded().
	agg, err := mcpcollapse.NewAggregator(ctx, p.upstreams, perUpstreamEnumerateTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-mcp-collapse: building aggregator: %v\n", err)
		return 1
	}

	for _, d := range agg.Degraded() {
		reason := "unknown"
		if d.Err != nil {
			reason = d.Err.Error()
		}
		stdLogger.Printf("clown-mcp-collapse: upstream %q (%s) degraded — tools unavailable: %s", d.Name, d.URL, reason)
	}
	for _, w := range agg.Warnings() {
		stdLogger.Printf("clown-mcp-collapse: %s", w)
	}

	handler := mcpcollapse.NewHandler(agg)
	srv := mcphttp.NewServer(mcphttp.Config{
		Handler:   handler,
		Logger:    stdLogger,
		LogPrefix: "clown-mcp-collapse",
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-mcp-collapse: listen: %v\n", err)
		return 1
	}

	// Handshake format must match internal/pluginhost/handshake.go, byte for
	// byte with clown-stdio-bridge: 1|1|tcp|<addr>|streamable-http\n.
	fmt.Printf("1|1|tcp|%s|streamable-http\n", ln.Addr().String())
	_ = os.Stdout.Sync()

	// Precompute the steering fragment. The degraded roster is final once
	// NewAggregator has returned, so the fragment is fixed for the process's
	// life — unlike the bridge's, which asks its live child per request.
	fragment := buildSystemPromptFragment(agg.Degraded())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", srv.HandleMCP)
	// Dynamic system-prompt contribution (RFC-0002 §dynamic fragments). clown
	// GETs this path after the server is healthy and folds a 200 body into
	// claude's --append-system-prompt-file. The path literal MUST match
	// pluginhost.BridgeSystemPromptPath, the same path clown-stdio-bridge serves.
	// Unlike the bridge (which proxies a live child prompt), the collapse always
	// has a fragment: the steering text plus, when any upstream is degraded, the
	// roster of failed servers.
	mux.HandleFunc(pluginhost.BridgeSystemPromptPath, func(w http.ResponseWriter, r *http.Request) {
		if !mcphttp.ValidateOrigin(r) {
			http.Error(w, "origin not permitted", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if fragment == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, fragment)
	})

	httpSrv := &http.Server{Handler: mux}
	serveErr := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	stdLogger.Printf("clown-mcp-collapse: serving %d upstream(s) (%d degraded) on %s",
		len(p.upstreams), len(agg.Degraded()), ln.Addr().String())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	exit := 0
	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "clown-mcp-collapse: received %s; shutting down\n", sig)
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown-mcp-collapse: HTTP serve error: %v\n", err)
			exit = 1
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	return exit
}

// buildSystemPromptFragment assembles the steering fragment clown folds into
// the agent's system prompt: ALWAYS the collapse steering text (telling the
// agent the MCP tools are collapsed behind mcp_list/mcp_describe/mcp_call and
// the discover→describe→call flow), plus — only when degraded is non-empty — a
// roster naming the upstream servers that failed to enumerate and stating their
// tools are unavailable (the Q5 requirement, so the agent doesn't wait on tools
// that will never appear).
func buildSystemPromptFragment(degraded []mcpcollapse.DegradedUpstream) string {
	var sb strings.Builder
	sb.WriteString("This session's MCP tools are COLLAPSED behind three generic verbs to save context. ")
	sb.WriteString("Instead of every upstream tool appearing in your tool list, you get:\n")
	sb.WriteString("  - mcp_list — discover what is callable: compact {tool_id: description} rows grouped by server (no schemas). ")
	sb.WriteString("Optional query/server filters.\n")
	sb.WriteString("  - mcp_describe — get one tool's full input schema + description, keyed by its dotted \"{server}.{tool}\" tool_id.\n")
	sb.WriteString("  - mcp_call — invoke a tool by tool_id with an args object matching its schema.\n")
	sb.WriteString("Flow: mcp_list to discover, mcp_describe to learn a tool's schema, then mcp_call to invoke it.\n")

	if len(degraded) > 0 {
		sb.WriteString("\nDEGRADED upstream servers (failed to enumerate at startup — their tools are unavailable this session):\n")
		for _, d := range degraded {
			reason := "unknown"
			if d.Err != nil {
				reason = d.Err.Error()
			}
			fmt.Fprintf(&sb, "  - %s (%s) — %s\n", d.Name, d.URL, reason)
		}
	}
	return sb.String()
}

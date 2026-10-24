package main

import (
	"context"
	"io"
	"net"
	"net/http"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// peerClassKey carries, on a serve-socket request's context, whether the
// caller is the frontend clown (agentscope.ServeConn.Frontend).
type peerClassKey struct{}

// serveConnContext is the serve-mode http.Server's ConnContext: it records
// the accepted connection's peer class for frontendOnly.
func serveConnContext(ctx context.Context, c net.Conn) context.Context {
	if sc, ok := c.(*agentscope.ServeConn); ok {
		return context.WithValue(ctx, peerClassKey{}, sc.Frontend)
	}
	return ctx
}

// frontendOnly serves next only to the frontend clown on the serve socket
// (clown#301): a caller in the unit gets 403, so it cannot read or clear its
// own tool exclusions or fetch the prompt fragment. A request with no peer
// class is refused too (fail closed).
func frontendOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if frontend, _ := r.Context().Value(peerClassKey{}).(bool); !frontend {
			http.Error(w, "this control endpoint is served to the frontend only", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// newMux is the bridge's HTTP surface: /healthz, /mcp and the clown control
// routes /clown/system-prompt and /clown/exclude-tools. In serve mode (the
// confined session's serve socket, F12) the control routes are frontend-only;
// on loopback TCP every route is served as before.
func newMux(handler *httpHandler, tr *translator, childDone <-chan error, serveMode bool) *http.ServeMux {
	control := func(h http.HandlerFunc) http.HandlerFunc {
		if serveMode {
			return frontendOnly(h)
		}
		return h
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-childDone:
			http.Error(w, "wrapped child has exited", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/mcp", handler.handleMCP)
	// Dynamic system-prompt contribution (RFC-0002 §dynamic fragments).
	// clown GETs this path after the server is healthy and folds a 200 body
	// into claude's --append-system-prompt-file. The bridge answers by
	// issuing an MCP prompts/get for the well-known "system-prompt-append"
	// prompt to the wrapped child, so the fragment is child-owned and
	// computed at request time (live state). A child that exposes no such
	// prompt — or any error — yields 204, which clown treats as "no
	// fragment". The path literal MUST match pluginhost.BridgeSystemPromptPath.
	mux.HandleFunc("/clown/system-prompt", control(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		text, ok := fetchChildSystemPrompt(r.Context(), tr)
		if !ok || text == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, text)
	}))
	// --cheap-context v2 (cmd/clown/cheapcontext.go): clown GETs/POSTs the set
	// of tool names to exclude from every subsequent tools/list response,
	// after the user's picker selection is known. Loopback-only per
	// mcphttp.ValidateOrigin, like every other /clown/* control endpoint on
	// this bridge — this is a clown-internal control surface, not part of MCP.
	// Contract matches moxy's independently-shipped /clown/exclude-tools
	// (amarbel-llc/moxy#399): see httpHandler.handleExcludeTools.
	mux.HandleFunc("/clown/exclude-tools", control(handler.handleExcludeTools))
	return mux
}

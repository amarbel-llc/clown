package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

// bindCountingBinding records every Bind; the fallback path binds before it
// runs anything, so zero calls means the fallback never ran.
type bindCountingBinding struct{ calls int }

func (b *bindCountingBinding) Bind(*pluginhost.Host, []pluginhost.DiscoveredServer, []pluginhost.DiscoveredServer) (Command, error) {
	b.calls++
	return Command{}, errors.New("test binding: stop before running a provider")
}

// TestRunUnboundIsUnreachableUnderASavedSelection: every fallback goes through
// runUnbound, and under a saved selection it refuses before binding the
// original plugin dirs (audit 2026-10-22, top 1).
func TestRunUnboundIsUnreachableUnderASavedSelection(t *testing.T) {
	saved := &profile.Profile{Name: "narrow", ContextServers: []string{"moxy/moxy"}}
	b := &bindCountingBinding{}
	exec := &recordingExecutor{}
	if code := runUnbound(b, exec, nil, parsedFlags{cheapContextProfile: saved}, "no plugin server started"); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if b.calls != 0 || exec.gotArgs != nil {
		t.Errorf("the fallback bound %d time(s) and ran %v under a saved selection", b.calls, exec.gotArgs)
	}
}

// TestRunWithPluginHostRefusesItsFallbacksUnderASavedSelection drives the two
// fallbacks in runWithPluginHost that need no running server: the clown
// protocol disabled, and nothing discovered.
func TestRunWithPluginHostRefusesItsFallbacksUnderASavedSelection(t *testing.T) {
	saved := &profile.Profile{Name: "narrow", ContextServers: []string{"moxy/moxy"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, flags := range map[string]parsedFlags{
		"protocol disabled":  {cheapContextProfile: saved, disableClownProtocol: true},
		"nothing discovered": {cheapContextProfile: saved},
	} {
		b := &bindCountingBinding{}
		if code := runWithPluginHost(&recordingExecutor{}, nil, flags, logger, "", nil, b); code != 1 || b.calls != 0 {
			t.Errorf("%s: exit %d, %d bind(s); want a refusal before any bind", name, code, b.calls)
		}
	}
}

func TestRunUnboundStillFallsBackWithoutASavedSelection(t *testing.T) {
	for _, p := range []*profile.Profile{nil, {Name: "plain"}} {
		b := &bindCountingBinding{}
		runUnbound(b, &recordingExecutor{}, nil, parsedFlags{cheapContextProfile: p}, "no plugin server was discovered")
		if b.calls != 1 {
			t.Errorf("profile %+v: fallback bound %d time(s), want 1", p, b.calls)
		}
	}
}

func TestSavedSelectionFallbackRefusalNamesTheKeepSetAndWhatWasFound(t *testing.T) {
	saved := &profile.Profile{Name: "narrow", ContextServers: []string{"moxy/moxy", "just-us/just-us"}}
	err := savedSelectionFallbackRefusal(saved, "no plugin server started (discovered: moxy/moxy; failed: moxy/moxy)")
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{`"narrow"`, "moxy/moxy, just-us/just-us", "failed: moxy/moxy", "un-narrowed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
}

func TestDiscoveredServerNames(t *testing.T) {
	if got := discoveredServerNames(nil); got != "none" {
		t.Errorf("empty = %q", got)
	}
	got := discoveredServerNames([]pluginhost.DiscoveredServer{
		{PluginName: "moxy", ServerName: "moxy"}, {PluginName: "just-us", ServerName: "just-us"},
	})
	if got != "moxy/moxy, just-us/just-us" {
		t.Errorf("names = %q", got)
	}
}

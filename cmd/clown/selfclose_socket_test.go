package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// F3: a second listener for the same key does not steal a live idle socket.
func TestIdleListenerRefusesALiveSocket(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	var runs atomic.Int32
	first, err := startIdleListener(sock, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := startIdleListener(sock, func() {}); err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("err = %v, want a live-socket refusal", err)
	}
	pokeIdle(t, sock)
	waitFor(t, "the first listener to keep its pokes", func() bool { return runs.Load() == 1 })
}

// A stale socket file (nobody bound) is replaced.
func TestIdleListenerReplacesAStaleSocket(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	stale, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	stale.Close() // the file stays, nothing is bound
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("expected a stale file: %v", err)
	}
	l, err := startIdleListener(sock, func() {})
	if err != nil {
		t.Fatalf("a stale socket must be replaced: %v", err)
	}
	l.Close()
}

// Closing a listener whose path a successor took over leaves the successor's
// socket in place.
func TestIdleListenerCloseKeepsASuccessorsSocket(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	first, err := startIdleListener(sock, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	second, err := startIdleListener(sock, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.Close()
	pokeIdle(t, sock)
	waitFor(t, "the successor's poke", func() bool { return runs.Load() == 1 })
}

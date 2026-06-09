package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/circusmodels"
)

// clown#55: the bubbletea progress UI requires /dev/tty; off-terminal the
// gate must disable it so the download proceeds without a progress bar.
func TestProgressUIWantedFalseOffTerminal(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	if progressUIWanted(devnull, devnull) {
		t.Error("progress UI must be disabled when stdio is not a terminal")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if progressUIWanted(r, w) {
		t.Error("progress UI must be disabled when stdio is a pipe")
	}
}

// The non-TTY path: download runs with no progress UI and prints a single
// final summary line.
func TestDownloadPlain(t *testing.T) {
	payload := []byte("fake-gguf-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	sum := sha256.Sum256(payload)
	entry := circusmodels.RegistryEntry{
		Name:   "tiny",
		URL:    srv.URL,
		SHA256: hex.EncodeToString(sum[:]),
	}
	dir := t.TempDir()

	var out bytes.Buffer
	if err := downloadPlain(context.Background(), entry, dir, &out); err != nil {
		t.Fatalf("downloadPlain: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "tiny.gguf")); err != nil {
		t.Errorf("model file not installed: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "downloaded tiny") || !strings.Contains(got, fmt.Sprintf("%d bytes", len(payload))) {
		t.Errorf("summary line missing name or byte count: %q", got)
	}
	if strings.Count(strings.TrimSpace(got), "\n") != 0 {
		t.Errorf("want a single summary line, got %q", got)
	}
}

func TestDownloadPlainPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	entry := circusmodels.RegistryEntry{Name: "missing", URL: srv.URL}
	var out bytes.Buffer
	if err := downloadPlain(context.Background(), entry, t.TempDir(), &out); err == nil {
		t.Fatal("want error from failed download")
	}
	if out.Len() != 0 {
		t.Errorf("no summary line may be printed on failure, got %q", out.String())
	}
}

func TestParseDownloadArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantURL  string
		wantSHA  string
		wantSize int64
		wantErr  bool
	}{
		{
			name:     "name only (registry lookup case)",
			args:     []string{"qwen3-coder"},
			wantName: "qwen3-coder",
		},
		{
			name:     "name plus space-form url",
			args:     []string{"foo", "--url", "https://example.com/foo.gguf"},
			wantName: "foo",
			wantURL:  "https://example.com/foo.gguf",
		},
		{
			name:     "name plus equals-form url",
			args:     []string{"foo", "--url=https://example.com/foo.gguf"},
			wantName: "foo",
			wantURL:  "https://example.com/foo.gguf",
		},
		{
			name:     "full triple: url + sha + size",
			args:     []string{"big", "--url", "https://x/y.gguf", "--sha256", "deadbeef", "--size", "1234567"},
			wantName: "big",
			wantURL:  "https://x/y.gguf",
			wantSHA:  "deadbeef",
			wantSize: 1234567,
		},
		{
			name:     "flag order independent",
			args:     []string{"--size=99", "--sha256=abc", "--url=https://x", "bar"},
			wantName: "bar",
			wantURL:  "https://x",
			wantSHA:  "abc",
			wantSize: 99,
		},
		{
			name:    "unknown flag rejected",
			args:    []string{"foo", "--frobnitz"},
			wantErr: true,
		},
		{
			name:    "two positional args rejected",
			args:    []string{"foo", "bar"},
			wantErr: true,
		},
		{
			name:    "missing --url argument",
			args:    []string{"foo", "--url"},
			wantErr: true,
		},
		{
			name:    "non-integer --size rejected",
			args:    []string{"foo", "--size", "huge"},
			wantErr: true,
		},
		{
			name:    "empty args (name omitted)",
			args:    []string{"--url", "https://x"},
			wantURL: "https://x",
			// name is empty; cmdDownload errors on this, parser does not
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotURL, gotSHA, gotSize, err := parseDownloadArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if gotName != tc.wantName {
				t.Errorf("name: got %q want %q", gotName, tc.wantName)
			}
			if gotURL != tc.wantURL {
				t.Errorf("url: got %q want %q", gotURL, tc.wantURL)
			}
			if gotSHA != tc.wantSHA {
				t.Errorf("sha: got %q want %q", gotSHA, tc.wantSHA)
			}
			if gotSize != tc.wantSize {
				t.Errorf("size: got %d want %d", gotSize, tc.wantSize)
			}
		})
	}
}

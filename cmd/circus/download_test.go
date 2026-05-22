package main

import "testing"

func TestParseDownloadArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantName  string
		wantURL   string
		wantSHA   string
		wantSize  int64
		wantErr   bool
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

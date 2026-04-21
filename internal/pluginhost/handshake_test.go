package pluginhost

import (
	"testing"
)

func TestParseHandshake(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Handshake
		wantErr bool
	}{
		{
			name:  "streamable-http",
			input: "1|1|tcp|127.0.0.1:54321|streamable-http",
			want: Handshake{
				CoreVersion: 1,
				AppVersion:  1,
				NetworkType: "tcp",
				Address:     "127.0.0.1:54321",
				Protocol:    "streamable-http",
			},
		},
		{
			name:  "sse",
			input: "1|1|tcp|127.0.0.1:8080|sse",
			want: Handshake{
				CoreVersion: 1,
				AppVersion:  1,
				NetworkType: "tcp",
				Address:     "127.0.0.1:8080",
				Protocol:    "sse",
			},
		},
		{
			name:  "with trailing newline",
			input: "1|1|tcp|127.0.0.1:9999|streamable-http\n",
			want: Handshake{
				CoreVersion: 1,
				AppVersion:  1,
				NetworkType: "tcp",
				Address:     "127.0.0.1:9999",
				Protocol:    "streamable-http",
			},
		},
		{
			name:  "extra fields ignored",
			input: "1|1|tcp|127.0.0.1:9999|streamable-http|extra",
			want: Handshake{
				CoreVersion: 1,
				AppVersion:  1,
				NetworkType: "tcp",
				Address:     "127.0.0.1:9999",
				Protocol:    "streamable-http",
			},
		},
		{
			name:    "too few fields",
			input:   "1|1|tcp|127.0.0.1:1234",
			wantErr: true,
		},
		{
			name:    "bad core version",
			input:   "2|1|tcp|127.0.0.1:1234|streamable-http",
			wantErr: true,
		},
		{
			name:    "non-numeric core version",
			input:   "x|1|tcp|127.0.0.1:1234|streamable-http",
			wantErr: true,
		},
		{
			name:    "unsupported network type",
			input:   "1|1|unix|/tmp/sock|streamable-http",
			wantErr: true,
		},
		{
			name:    "empty address",
			input:   "1|1|tcp||streamable-http",
			wantErr: true,
		},
		{
			name:    "unsupported protocol",
			input:   "1|1|tcp|127.0.0.1:1234|grpc",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHandshake(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHandshakeURL(t *testing.T) {
	tests := []struct {
		proto string
		addr  string
		want  string
	}{
		{"streamable-http", "127.0.0.1:8080", "http://127.0.0.1:8080/mcp"},
		{"sse", "127.0.0.1:9090", "http://127.0.0.1:9090/sse"},
	}
	for _, tt := range tests {
		h := Handshake{Protocol: tt.proto, Address: tt.addr}
		if got := h.URL(); got != tt.want {
			t.Errorf("URL() = %q, want %q", got, tt.want)
		}
	}
}

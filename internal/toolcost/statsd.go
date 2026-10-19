package toolcost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/userpath"
)

const (
	statsdHostEnvVar    = "STATSD_HOST"
	statsdPortEnvVar    = "STATSD_PORT"
	statsdDisableEnvVar = "CLOWN_DISABLE_STATSD"

	statsdDefaultHost = "127.0.0.1"
	statsdDefaultPort = "8125"

	// datagramMax keeps each UDP payload well under a 1500-byte MTU; statsd
	// accepts several newline-separated metrics per datagram.
	datagramMax = 1200
)

// StatsdDisabled reports the shared kill switch (CLOWN_DISABLE_STATSD=1, the
// same one clown-stdio-bridge honours).
func StatsdDisabled() bool { return os.Getenv(statsdDisableEnvVar) == "1" }

// Encode folds metrics into statsd lines: counters with the same name are
// summed, a gauge keeps its last value, timers stay individual samples (the
// timer aggregation is what yields p50/p90/max). Output is sorted by name so
// it is deterministic.
func Encode(ms []Metric) []string {
	counters := map[string]int64{}
	gauges := map[string]int64{}
	var timers []Metric
	for _, m := range ms {
		switch m.Kind {
		case KindCounter:
			counters[m.Name] += m.Value
		case KindGauge:
			gauges[m.Name] = m.Value
		case KindTimer:
			timers = append(timers, m)
		}
	}
	var lines []string
	for name, v := range counters {
		lines = append(lines, fmt.Sprintf("%s:%d|c", name, v))
	}
	for name, v := range gauges {
		lines = append(lines, fmt.Sprintf("%s:%d|g", name, v))
	}
	for _, m := range timers {
		lines = append(lines, fmt.Sprintf("%s:%d|ms", m.Name, m.Value))
	}
	sort.Strings(lines)
	return lines
}

// Send writes lines to the stats-me statsd listener (STATSD_HOST/STATSD_PORT,
// default 127.0.0.1:8125) as fire-and-forget UDP, packed into datagrams of at
// most datagramMax bytes. Write errors are returned so the caller can log
// them, never acted on.
func Send(lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	host := os.Getenv(statsdHostEnvVar)
	if host == "" {
		host = statsdDefaultHost
	}
	port := os.Getenv(statsdPortEnvVar)
	if port == "" {
		port = statsdDefaultPort
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, port), 250*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	var buf strings.Builder
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		_, err := conn.Write([]byte(buf.String()))
		buf.Reset()
		return err
	}
	var firstErr error
	for _, l := range lines {
		if buf.Len() > 0 && buf.Len()+1+len(l) > datagramMax {
			if err := flush(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(l)
	}
	if err := flush(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// StatePath is the persisted State file for one transcript:
// $XDG_STATE_HOME/clown/hook-toolcost/<sha256(path)>.json. Keyed by the
// transcript path (unique per session and per subagent).
func StatePath(transcriptPath string) (string, error) {
	sum := sha256.Sum256([]byte(transcriptPath))
	return userpath.StatePath("hook-toolcost", hex.EncodeToString(sum[:])+".json")
}

// LoadState reads a transcript's State; a missing or malformed file yields
// the zero State (process from the start).
func LoadState(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var st State
	if json.Unmarshal(data, &st) != nil {
		return State{}
	}
	return st
}

// SaveState atomically persists st (write-temp + rename).
func SaveState(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

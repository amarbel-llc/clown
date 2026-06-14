package jobwake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// presenceStale is how long after its last refresh a presence entry is taken to
// be stale — a session that crashed or was killed without removing its file.
// ListPresence drops and best-effort-removes entries older than this. It is a
// comfortable multiple of the monitor's refresh cadence so a couple of missed
// refreshes do not drop a live session.
const presenceStale = 2 * time.Minute

// Presence is one clown instance's chat presence record (RFC-0013 §3.3): enough
// for the recipient listing (`chat list`) to show who is reachable and group
// them by their spinclass session.
type Presence struct {
	SessionKey  string `json:"sessionKey"`            // per-instance routing key
	ChannelID   string `json:"channelId"`             // ChannelID(SessionKey)
	Decoration  string `json:"decoration,omitempty"`  // SPINCLASS_SESSION_ID group label
	Description string `json:"description,omitempty"` // SPINCLASS_DESCRIPTION (readable)
	LastSeen    string `json:"lastSeen"`              // RFC3339Nano, refreshed by the monitor
}

// PresenceDir holds the per-instance presence files (mode 0700), a sibling of
// the jobs channels so channel walks never see it.
func PresenceDir() string {
	return filepath.Join(stateHome(), "clown", "presence")
}

func presenceFile(channelID string) string {
	return filepath.Join(PresenceDir(), channelID+".json")
}

// RegisterPresence writes (or refreshes) the current session's presence record:
// its per-instance key + channel, the SPINCLASS_SESSION_ID group decoration and
// SPINCLASS_DESCRIPTION (both launch-time env), and a fresh lastSeen. The write
// is atomic (temp + rename). Best-effort by contract — a presence failure must
// never break the monitor.
func RegisterPresence(now time.Time) error {
	key := SessionKey()
	p := Presence{
		SessionKey:  key,
		ChannelID:   ChannelID(key),
		Decoration:  GroupKey(),
		Description: os.Getenv("SPINCLASS_DESCRIPTION"),
		LastSeen:    now.UTC().Format(time.RFC3339Nano),
	}
	if err := os.MkdirAll(PresenceDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := presenceFile(p.ChannelID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path) // atomic
}

// RemovePresence deletes the current session's presence record — a clean
// monitor shutdown disappears from the listing immediately rather than waiting
// out the stale window.
func RemovePresence() {
	_ = os.Remove(presenceFile(ChannelID(SessionKey())))
}

// ListPresence returns every live presence record, oldest-seen first, dropping
// (and best-effort removing) entries staler than presenceStale.
func ListPresence(now time.Time) ([]Presence, error) {
	entries, err := os.ReadDir(PresenceDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Presence
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(PresenceDir(), name))
		if err != nil {
			continue
		}
		var p Presence
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		if ts, perr := time.Parse(time.RFC3339Nano, p.LastSeen); perr == nil && now.Sub(ts) > presenceStale {
			_ = os.Remove(filepath.Join(PresenceDir(), name)) // prune stale
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen < out[j].LastSeen })
	return out, nil
}

package jugglerrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultRights is the rights list a holder is granted when none is named
// (spinclass FDR 0032 D13's conservative handoff).
const DefaultRights = "observe,close"

// HandleStatus is a holder's standing on a job.
type HandleStatus string

const (
	// HandlePending: granted but never exercised. A pending handle confers
	// nothing — no authority and no exit wake — until its first use.
	HandlePending HandleStatus = "pending"
	// HandleAccepted: exercised at least once (the spawner's own handle is
	// accepted at spawn, its first use).
	HandleAccepted HandleStatus = "accepted"
	// HandleReleased: the holder released its own handle. Only the journal
	// mirror carries it; a rebuilt table drops released holders.
	HandleReleased HandleStatus = "released"
)

// Holder is one handle on a job (FDR 0019 §11). Field meanings are
// spinclass's so v2 relocates rather than migrates: Principal is the
// holder's principal string, Rights a comma-separated list.
type Holder struct {
	Principal string       `json:"holder"`
	Rights    string       `json:"rights"`
	Status    HandleStatus `json:"status"`
	GrantedBy string       `json:"granted_by,omitempty"`
	GrantedAt time.Time    `json:"granted_at"`
}

// FirstHolder is the spawner's handle: the first grant on a job, accepted by
// the spawn itself.
func FirstHolder(principal string, now time.Time) Holder {
	return Holder{Principal: principal, Rights: DefaultRights, Status: HandleAccepted, GrantedBy: principal, GrantedAt: now.UTC()}
}

// ReleaseHolder removes the caller's own handle and nobody else's.
func ReleaseHolder(holders []Holder, caller string) []Holder {
	out := make([]Holder, 0, len(holders))
	for _, h := range holders {
		if h.Principal != caller {
			out = append(out, h)
		}
	}
	return out
}

// WakeRecipients is the set of principals an exit wake goes to: every
// accepted holder, once each, in first-grant order. Pending holders receive
// nothing.
func WakeRecipients(holders []Holder) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range holders {
		if h.Status != HandleAccepted || h.Principal == "" || seen[h.Principal] {
			continue
		}
		seen[h.Principal] = true
		out = append(out, h.Principal)
	}
	return out
}

// holderRecordPrefix marks a ringmaster progress record that mirrors a grant.
// The journal copy is a rebuildable cache of the handle table (FDR 0019 §6).
const holderRecordPrefix = "juggler-holder "

// HolderProgressMessage is the progress-record text mirroring h.
func HolderProgressMessage(h Holder) (string, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	return holderRecordPrefix + string(b), nil
}

// HoldersFromRecords rebuilds a job's handle table from its journal mirror:
// the latest record per principal wins, released holders are dropped, and
// order is first grant.
func HoldersFromRecords(recs []JobRecord) []Holder {
	latest := map[string]Holder{}
	var order []string
	for _, r := range recs {
		if r.Type != "progress" || !strings.HasPrefix(r.Message, holderRecordPrefix) {
			continue
		}
		var h Holder
		if json.Unmarshal([]byte(strings.TrimPrefix(r.Message, holderRecordPrefix)), &h) != nil || h.Principal == "" {
			continue
		}
		if _, ok := latest[h.Principal]; !ok {
			order = append(order, h.Principal)
		}
		latest[h.Principal] = h
	}
	var out []Holder
	for _, p := range order {
		if h := latest[p]; h.Status != HandleReleased {
			out = append(out, h)
		}
	}
	return out
}

// MergeHolders unions holder tables by principal; a later table's entry wins.
func MergeHolders(tables ...[]Holder) []Holder {
	latest := map[string]Holder{}
	var order []string
	for _, t := range tables {
		for _, h := range t {
			if _, ok := latest[h.Principal]; !ok {
				order = append(order, h.Principal)
			}
			latest[h.Principal] = h
		}
	}
	out := make([]Holder, 0, len(order))
	for _, p := range order {
		out = append(out, latest[p])
	}
	return out
}

// mirrorHolder appends h's journal mirror to the job.
func mirrorHolder(ctx context.Context, rmc Ringmaster, target, job string, h Holder) error {
	msg, err := HolderProgressMessage(h)
	if err != nil {
		return err
	}
	if err := rmc.Progress(ctx, target, job, msg); err != nil {
		return fmt.Errorf("mirroring holder %s on %s: %w", h.Principal, job, err)
	}
	return nil
}

package catalogsnap

import (
	"testing"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
)

// The listing reports RFC 0018 §2's lock (#354): the LockDigest of the
// tool's inputSchema, the same function an enforcer calls. A built-in has
// no schema and no lock.
func TestLockIsTheLockDigestOfTheInputSchemaAndBuiltinsHaveNone(t *testing.T) {
	got := Present(fixture(), contract(t))
	want, err := catalogcontract.LockDigest([]byte(folioReadSchema))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := find(got, "moxy/folio/read"); n.Lock != want {
		t.Errorf("folio read lock %q, want %q", n.Lock, want)
	}
	if n, _ := find(got, "clown-builtin-harness/harness/Read"); n.Lock != "" {
		t.Errorf("harness Read lock %q: a built-in has no schema to lock", n.Lock)
	}
}

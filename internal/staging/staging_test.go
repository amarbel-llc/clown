package staging_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/clown/internal/staging"
)

func TestRoot_DirAndFileLandUnderRoot(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	d, err := r.Dir("plugin-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d, r.Path()) {
		t.Errorf("Dir() = %q, want under %q", d, r.Path())
	}

	f, err := r.File("prompt-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !strings.HasPrefix(f.Name(), r.Path()) {
		t.Errorf("File() = %q, want under %q", f.Name(), r.Path())
	}
}

// The property a future container mount depends on: ONE directory to expose.
func TestRoot_CloseRemovesEverything(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Not `d, _ :=` — a failed Dir yields "", os.Stat("") is ENOENT, and the
	// "everything beneath it" half of this test would then assert nothing.
	d, err := r.Dir("plugin-")
	if err != nil {
		t.Fatal(err)
	}
	root := r.Path()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d, root} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%q survived Close()", p)
		}
	}
}

func TestRoot_CloseIsIdempotent(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

// A non-empty base must be honored literally. This is the property the
// clownbox path will lean on to replace its global os.Setenv("TMPDIR", ...)
// with configuration, and the one a container locus needs to place the root
// inside a mounted directory.
func TestRoot_NewHonorsBase(t *testing.T) {
	base := t.TempDir()
	r, err := staging.New(base)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got := filepath.Dir(r.Path()); got != base {
		t.Errorf("Path() parent = %q, want %q", got, base)
	}
}

// New must create a missing base rather than returning ENOENT. The clownbox
// root is <repo>/.tmp, which is gitignored and therefore absent on a fresh
// clone — strictness here would turn the first launch after a clone into a
// failure.
func TestRoot_NewCreatesMissingBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing", "nested")
	r, err := staging.New(base)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got := filepath.Dir(r.Path()); got != base {
		t.Errorf("Path() parent = %q, want %q", got, base)
	}
	if fi, err := os.Stat(r.Path()); err != nil {
		t.Errorf("root not created: %v", err)
	} else if !fi.IsDir() {
		t.Errorf("root %q is not a directory", r.Path())
	}
}

// Use-after-close is a bug, not a request to silently recreate artifacts under
// a root something upstream already finished with.
func TestRoot_DirAndFileFailAfterClose(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Dir("plugin-"); !errors.Is(err, staging.ErrClosed) {
		t.Errorf("Dir() after Close = %v, want ErrClosed", err)
	}
	f, err := r.File("prompt-*.txt")
	if !errors.Is(err, staging.ErrClosed) {
		t.Errorf("File() after Close = %v, want ErrClosed", err)
	}
	if f != nil {
		f.Close()
		t.Error("File() after Close returned a usable *os.File")
	}
}

// Path() must not keep handing out a path to a directory that no longer
// exists: a caller that mounts it, or os.MkdirAll's it, would resurrect state
// outside the root's lifetime.
func TestRoot_PathIsEmptyAfterClose(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.Path(); got != "" {
		t.Errorf("Path() after Close = %q, want %q", got, "")
	}
}

// concurrentRaceIterations is what makes this test a reliable guard rather than
// a lottery. No repo lane runs -race over ./... (clown-go-test's checkPhase is
// a plain `go test -p $NIX_BUILD_CORES ./...`), so a dropped lock has to be
// caught WITHOUT the race detector, purely from the observable failure — and
// one unsynchronised pass is only about a 1% detector.
//
// Measured against a mutex-stripped copy, as fraction of invocations that
// failed: 4/50 unrepeated and gateless, 8/30 at 200 iterations gateless,
// 26/30 at 200 iterations with the start gate below, and 30/30 at this count.
// 1000 compounds that ~1% to better than 99.9% per invocation, and costs the
// suite about 0.26s.
const concurrentRaceIterations = 1000

// One Root is shared by every artifact writer in a launch, and plugin startup
// already fans out into goroutines — so concurrent use must be safe. Close
// races the writers deliberately: that is the only interleaving that exercises
// the closed flag's write, and it pins the contract that Dir/File are atomic
// with respect to Close. Each call must either fully succeed or report
// ErrClosed; it must never half-create an artifact into a directory that
// RemoveAll is walking, which is exactly what an unsynchronised
// check-then-create would do.
func TestRoot_ConcurrentUseRacesCloseSafely(t *testing.T) {
	base := t.TempDir()

	for range concurrentRaceIterations {
		r, err := staging.New(base)
		if err != nil {
			t.Fatal(err)
		}

		// Every goroutine parks on start, so releasing it puts the writers and
		// the closer in flight together. Without the gate the closer is
		// spawned last and the writers have usually finished before it is
		// scheduled at all, which collapses the race window: measured against
		// a mutex-stripped copy, gateless detection was 8 of 30 invocations
		// even at this iteration count.
		start := make(chan struct{})
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := r.Close(); err != nil {
				t.Errorf("Close() = %v, want nil", err)
			}
		}()
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := r.Dir("plugin-"); err != nil && !errors.Is(err, staging.ErrClosed) {
					t.Errorf("Dir() = %v, want nil or ErrClosed", err)
				}
				f, err := r.File("prompt-*.txt")
				if err != nil && !errors.Is(err, staging.ErrClosed) {
					t.Errorf("File() = %v, want nil or ErrClosed", err)
				}
				if f != nil {
					f.Close()
				}
				_ = r.Path()
			}()
		}

		close(start)
		wg.Wait()

		if t.Failed() {
			return
		}
	}
}

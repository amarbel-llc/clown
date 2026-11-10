package catalogserve

import (
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
)

func TestFrontendAliveIsTheRecordedPidWithMatchingStartTicks(t *testing.T) {
	pid := os.Getpid()
	ticks := sessionstatus.StartTicks(pid)
	if !FrontendAlive(catalogsnap.Snapshot{FrontendPID: pid, FrontendStartTicks: ticks}) {
		t.Error("this process's own snapshot is not alive")
	}
	if FrontendAlive(catalogsnap.Snapshot{}) {
		t.Error("a snapshot with no frontend pid is alive")
	}
	if ticks != 0 && FrontendAlive(catalogsnap.Snapshot{FrontendPID: pid, FrontendStartTicks: ticks + 1}) {
		t.Error("a pid whose start ticks differ (a reused pid) is alive")
	}
}

// RFC 0013: a plugin refusing every offered version SHOULD exit after
// answering; answer stops reading after that reply.
func TestAnswerStopsAfterRefusingTheVersion(t *testing.T) {
	s, _ := newServer(t)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_versions":["traversal-plugin/v9"]}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"roots.list","params":{}}` + "\n")
	var out strings.Builder
	s.answer(in, &out)
	if n := strings.Count(out.String(), "\n"); n != 1 || !strings.Contains(out.String(), `"code":-32000`) {
		t.Errorf("answer wrote %d line(s): %s; want only the version refusal", n, out.String())
	}
}

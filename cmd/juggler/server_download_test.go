package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglermodels"
)

func downloadModel(t *testing.T, s *server, name string) (rm.DownloadModelResult, *rm.Error) {
	t.Helper()
	resp := s.dispatch(context.Background(), rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodDownloadModel,
		Params: mustJSON(t, rm.DownloadModelParams{Name: name}),
	})
	if resp.Error != nil {
		return rm.DownloadModelResult{}, resp.Error
	}
	var res rm.DownloadModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res, nil
}

// DownloadModel of a model that is already installed (clown#314) answers
// with that model rather than an error: the client has what it asked for.
// The installed file is left untouched.
func TestServerDownloadModelOfAnInstalledModel(t *testing.T) {
	modelsDir := shortTempDir(t)
	dest := filepath.Join(modelsDir, "m.gguf")
	if err := os.WriteFile(dest, []byte("installed"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = modelsDir
	s.models = []jugglermodels.RegistryEntry{{Name: "m", URL: "http://example.invalid/m.gguf"}}

	res, rpcErr := downloadModel(t, s, "m")
	if rpcErr != nil {
		t.Fatalf("DownloadModel of an installed model: %+v", rpcErr)
	}
	if res.Model.Path != dest || res.Model.Size != int64(len("installed")) {
		t.Errorf("result %+v, want the installed model at %s", res.Model, dest)
	}
	if b, _ := os.ReadFile(dest); string(b) != "installed" {
		t.Errorf("installed model changed: %q", b)
	}
}

// Concurrent DownloadModel calls of one model all answer with that model.
func TestServerConcurrentDownloadModelCalls(t *testing.T) {
	payload := []byte("a fake gguf payload")
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()
	modelsDir := shortTempDir(t)
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = modelsDir
	s.models = []jugglermodels.RegistryEntry{{Name: "m", URL: httpSrv.URL + "/m.gguf"}}

	const n = 4
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, rpcErr := downloadModel(t, s, "m")
			if rpcErr != nil {
				t.Errorf("DownloadModel: %+v", rpcErr)
				return
			}
			if res.Model.Path != filepath.Join(modelsDir, "m.gguf") || res.Model.Size != int64(len(payload)) {
				t.Errorf("result %+v", res.Model)
			}
		}()
	}
	wg.Wait()
}

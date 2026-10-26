package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglermodels"
)

// downloadModel dispatches a DownloadModel; it reports through its error
// rather than t.Fatal, so it is safe to call from a goroutine.
func downloadModel(s *server, name string) (rm.DownloadModelResult, error) {
	params, err := json.Marshal(rm.DownloadModelParams{Name: name})
	if err != nil {
		return rm.DownloadModelResult{}, err
	}
	resp := s.dispatch(context.Background(), rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodDownloadModel, Params: params,
	})
	if resp.Error != nil {
		return rm.DownloadModelResult{}, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var res rm.DownloadModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return rm.DownloadModelResult{}, err
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

	res, err := downloadModel(s, "m")
	if err != nil {
		t.Fatalf("DownloadModel of an installed model: %v", err)
	}
	if res.Model.Path != dest || res.Model.Size != int64(len("installed")) {
		t.Errorf("result %+v, want the installed model at %s", res.Model, dest)
	}
	if b, _ := os.ReadFile(dest); string(b) != "installed" {
		t.Errorf("installed model changed: %q", b)
	}
}

// A directory where the model should be is not an installed model: the
// RPC fails rather than answering with the directory.
func TestServerDownloadModelWithADirectoryInTheWay(t *testing.T) {
	modelsDir := shortTempDir(t)
	if err := os.Mkdir(filepath.Join(modelsDir, "m.gguf"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = modelsDir
	s.models = []jugglermodels.RegistryEntry{{Name: "m", URL: "http://example.invalid/m.gguf"}}

	if res, err := downloadModel(s, "m"); err == nil || !strings.Contains(err.Error(), "not a model") {
		t.Fatalf("DownloadModel with a directory in the way: res %+v, err %v; want a not-a-model error", res, err)
	}
}

// `juggler download` of an installed model still fails with "already
// installed".
func TestDownloadPlainOfAnInstalledModelFails(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "m.gguf"), []byte("installed"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := downloadPlain(context.Background(), jugglermodels.RegistryEntry{Name: "m", URL: "http://example.invalid/m.gguf"}, dir, &out)
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("downloadPlain of an installed model: err = %v, want already installed", err)
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
			res, err := downloadModel(s, "m")
			if err != nil {
				t.Errorf("DownloadModel: %v", err)
				return
			}
			if res.Model.Path != filepath.Join(modelsDir, "m.gguf") || res.Model.Size != int64(len(payload)) {
				t.Errorf("result %+v", res.Model)
			}
		}()
	}
	wg.Wait()
}

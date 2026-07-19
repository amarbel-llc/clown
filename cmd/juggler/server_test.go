package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglermodels"
)

// mustJSON marshals v for use as an rm.Envelope's Params field in tests
// that call s.dispatch directly (bypassing the socket/Client roundtrip).
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// shortTempDir returns a short-path tmpdir. macOS imposes a ~104-char
// limit on Unix domain socket paths (sun_path), and the project's
// devshell sets TMPDIR inside the worktree, which can exceed it. Use
// /tmp explicitly and clean up via t.Cleanup. Mirrors the helper in
// internal/juggler/client_test.go; test packages are independent
// so the helper is copied rather than imported.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "juggler-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServer_ListInstances_Empty(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 {
		t.Errorf("expected empty, got %+v", res.Instances)
	}
}

func TestServer_ListAvailableModels(t *testing.T) {
	// Create a fake models dir with two .gguf files of known sizes.
	modelsDir := shortTempDir(t)
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(modelsDir, name+".gguf")
		if err := os.WriteFile(path, []byte("not-a-real-gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Also a non-gguf file that should be skipped.
	_ = os.WriteFile(filepath.Join(modelsDir, "notes.txt"), []byte("ignore me"), 0o644)

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir // see Step 3 — server needs this field
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListAvailableModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(res.Models), res.Models)
	}
	// listAvailableModels uses jugglermodels.List which sorts.
	if res.Models[0].Name != "alpha" || res.Models[1].Name != "beta" {
		t.Errorf("model order wrong: %+v", res.Models)
	}
	for _, m := range res.Models {
		if m.Path == "" || m.Size == 0 {
			t.Errorf("model missing path/size: %+v", m)
		}
	}
}

func TestServer_DownloadModel(t *testing.T) {
	payload := []byte("this is a fake gguf file for testing")
	sum := sha256.Sum256(payload)
	expectedSHA := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/fake.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	modelsDir := shortTempDir(t)
	models := []jugglermodels.RegistryEntry{
		{
			Name:   "fake",
			URL:    httpSrv.URL + "/fake.gguf",
			SHA256: expectedSHA,
			Size:   int64(len(payload)),
		},
	}

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir
	srv.models = models
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.DownloadModel(ctx, rm.DownloadModelParams{Name: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model.Name != "fake" {
		t.Errorf("name: %q", res.Model.Name)
	}
	if res.Model.Size != int64(len(payload)) {
		t.Errorf("size: got %d, want %d", res.Model.Size, len(payload))
	}
	want := filepath.Join(modelsDir, "fake.gguf")
	if res.Model.Path != want {
		t.Errorf("path: got %q, want %q", res.Model.Path, want)
	}
	if _, err := os.Stat(res.Model.Path); err != nil {
		t.Errorf("file not present at %s: %v", res.Model.Path, err)
	}
}

func TestServer_DownloadModel_UnknownName(t *testing.T) {
	modelsDir := shortTempDir(t)
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir
	srv.models = []jugglermodels.RegistryEntry{} // explicit empty (non-nil) override
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if _, err := cli.DownloadModel(ctx, rm.DownloadModelParams{Name: "nope"}); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestServer_StopAll(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(reg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// Start two instances.
	for _, alias := range []string{"a", "b"} {
		if _, err := cli.StartInstance(ctx, rm.StartInstanceParams{
			Alias: alias,
			Model: alias,
		}); err != nil {
			t.Fatalf("start %s: %v", alias, err)
		}
	}

	// StopAll should return both aliases and leave the registry empty.
	res, err := cli.StopAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 2 {
		t.Errorf("expected 2 stopped, got %d: %+v", len(res.Stopped), res.Stopped)
	}
	list, _ := cli.ListInstances(ctx)
	if len(list.Instances) != 0 {
		t.Errorf("registry should be empty after StopAll, got %+v", list)
	}
}

func TestServer_StartListStop(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	srv := newServer(reg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	startRes, err := cli.StartInstance(ctx, rm.StartInstanceParams{
		Alias: "a",
		Model: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if startRes.Instance.Alias != "a" || startRes.Instance.Port == 0 {
		t.Errorf("start result: %+v", startRes)
	}

	list, err := cli.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 || list.Instances[0].Alias != "a" {
		t.Errorf("list=%+v", list)
	}

	getRes, err := cli.GetInstance(ctx, rm.GetInstanceParams{Alias: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if getRes.Instance.Alias != "a" {
		t.Errorf("get=%+v", getRes)
	}

	if err := cli.StopInstance(ctx, rm.StopInstanceParams{Alias: "a"}); err != nil {
		t.Fatal(err)
	}
	list2, err := cli.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list2.Instances) != 0 {
		t.Errorf("after stop list=%+v", list2)
	}
}

func TestDispatchListModels_MergesLocalAndRemote(t *testing.T) {
	modelsDir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(modelsDir, "local-model.gguf"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	remotePath := filepath.Join(shortTempDir(t), "models.toml")
	if err := rm.SaveRemoteModels(remotePath, []rm.RemoteModel{
		{Name: "remote-model", Style: "anthropic", URL: "https://example.com", Token: "tok"},
	}); err != nil {
		t.Fatal(err)
	}

	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = modelsDir
	s.remoteModelsPath = remotePath

	resp := s.dispatch(rm.Envelope{JSONRPC: "2.0", ID: "1", Method: rm.MethodListModels})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var res rm.ListModelsResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(res.Models), res.Models)
	}
	var gotLocal, gotRemote bool
	for _, m := range res.Models {
		switch m.Name {
		case "local-model":
			gotLocal = true
			if m.Kind != rm.ModelKindLocal {
				t.Errorf("local-model kind = %q, want local", m.Kind)
			}
		case "remote-model":
			gotRemote = true
			if m.Kind != rm.ModelKindRemote || m.Style != "anthropic" {
				t.Errorf("remote-model = %+v", m)
			}
		}
	}
	if !gotLocal || !gotRemote {
		t.Fatalf("missing entries: %+v", res.Models)
	}
}

func TestDispatchAddRemoteModel_ThenListModels(t *testing.T) {
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	addResp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodAddRemoteModel,
		Params: mustJSON(t, rm.AddRemoteModelParams{
			Name: "gw", Style: "openai-compat", URL: "https://gw.example.com", Token: "${GW_TOKEN}",
		}),
	})
	if addResp.Error != nil {
		t.Fatalf("AddRemoteModel error: %+v", addResp.Error)
	}

	listResp := s.dispatch(rm.Envelope{JSONRPC: "2.0", ID: "2", Method: rm.MethodListModels})
	if listResp.Error != nil {
		t.Fatalf("ListModels error: %+v", listResp.Error)
	}
	var res rm.ListModelsResult
	if err := json.Unmarshal(listResp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 1 || res.Models[0].Name != "gw" || res.Models[0].Kind != rm.ModelKindRemote {
		t.Fatalf("models = %+v", res.Models)
	}

	// Confirm it round-tripped to disk (not just an in-memory cache).
	onDisk, err := rm.LoadRemoteModels(s.remoteModelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 1 || onDisk[0].Name != "gw" || onDisk[0].Token != "${GW_TOKEN}" {
		t.Fatalf("on-disk models = %+v", onDisk)
	}
}

func TestDispatchAddRemoteModel_ConcurrentWritesDontLoseUpdates(t *testing.T) {
	// Regression test for issue #182: AddRemoteModel's load->mutate->save
	// sequence had no mutex, so two concurrent callers could each read the
	// same on-disk snapshot and one write would clobber the other's
	// addition (a lost update). Fire enough concurrent adds for distinct
	// names that, without serialization, at least one is very likely to
	// be dropped; with the mutex in place every add is fully serialized so
	// all of them must survive.
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("gw-%02d", i)
			resp := s.dispatch(rm.Envelope{
				JSONRPC: "2.0",
				ID:      json.Number(fmt.Sprintf("%d", i)),
				Method:  rm.MethodAddRemoteModel,
				Params: mustJSON(t, rm.AddRemoteModelParams{
					Name: name, Style: "openai-compat", URL: "https://example.com",
				}),
			})
			if resp.Error != nil {
				errs <- fmt.Sprintf("AddRemoteModel(%s): %+v", name, resp.Error)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	onDisk, err := rm.LoadRemoteModels(s.remoteModelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != n {
		t.Fatalf("expected %d remote models on disk, got %d: %+v", n, len(onDisk), onDisk)
	}
	seen := make(map[string]bool, n)
	for _, m := range onDisk {
		seen[m.Name] = true
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("gw-%02d", i)
		if !seen[name] {
			t.Errorf("missing %s in final registry — lost update", name)
		}
	}
}

func TestDispatchRemoveRemoteModel_ThenListModels(t *testing.T) {
	remotePath := filepath.Join(shortTempDir(t), "models.toml")
	if err := rm.SaveRemoteModels(remotePath, []rm.RemoteModel{
		{Name: "gw", URL: "https://gw.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = remotePath

	removeResp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodRemoveRemoteModel,
		Params: mustJSON(t, rm.RemoveRemoteModelParams{Name: "gw"}),
	})
	if removeResp.Error != nil {
		t.Fatalf("RemoveRemoteModel error: %+v", removeResp.Error)
	}

	listResp := s.dispatch(rm.Envelope{JSONRPC: "2.0", ID: "2", Method: rm.MethodListModels})
	if listResp.Error != nil {
		t.Fatalf("ListModels error: %+v", listResp.Error)
	}
	var res rm.ListModelsResult
	if err := json.Unmarshal(listResp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 0 {
		t.Fatalf("expected empty after remove, got %+v", res.Models)
	}
}

func TestDispatchRemoveRemoteModel_NotFound(t *testing.T) {
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodRemoveRemoteModel,
		Params: mustJSON(t, rm.RemoveRemoteModelParams{Name: "nope"}),
	})
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("resp = %#v, want -32001", resp)
	}
}

func TestDispatchResolveModel_Remote(t *testing.T) {
	t.Setenv("JUGGLER_TEST_TOKEN", "resolved-secret")
	remotePath := filepath.Join(shortTempDir(t), "models.toml")
	if err := rm.SaveRemoteModels(remotePath, []rm.RemoteModel{
		{Name: "gw", Style: "anthropic", URL: "https://gw.example.com", Token: "${JUGGLER_TEST_TOKEN}"},
	}); err != nil {
		t.Fatal(err)
	}
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = remotePath

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "gw"}),
	})
	if resp.Error != nil {
		t.Fatalf("ResolveModel error: %+v", resp.Error)
	}
	var res rm.ResolveModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != rm.ModelKindRemote || res.URL != "https://gw.example.com" || res.Token != "resolved-secret" || res.Style != "anthropic" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDispatchResolveModel_AlreadyRunningLocal(t *testing.T) {
	reg := rm.NewRegistry()
	if err := reg.Add(rm.Instance{Alias: "running-model", Bind: "127.0.0.1", Port: 4242}); err != nil {
		t.Fatal(err)
	}
	// nil launcher: if the dispatch code took the "start a new instance"
	// path instead of reusing the registry entry, calling Start on a nil
	// Launcher would panic — this proves the reuse branch is taken.
	s := newServer(reg, nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "running-model"}),
	})
	if resp.Error != nil {
		t.Fatalf("ResolveModel error: %+v", resp.Error)
	}
	var res rm.ResolveModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != rm.ModelKindLocal || res.URL != "http://127.0.0.1:4242" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDispatchResolveModel_LocalGGUFStartsInstance(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	modelsDir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(modelsDir, "local-model.gguf"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, modelsDir)
	s := newServer(reg, l)
	s.modelsDir = modelsDir
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "local-model"}),
	})
	if resp.Error != nil {
		t.Fatalf("ResolveModel error: %+v", resp.Error)
	}
	var res rm.ResolveModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != rm.ModelKindLocal || res.URL == "" {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := reg.Get("local-model"); !ok {
		t.Errorf("expected instance registered after ResolveModel start")
	}
	t.Cleanup(func() { _ = l.Stop(context.Background(), "local-model") })
}

func TestDispatchResolveModel_RemoteWinsOnCollision(t *testing.T) {
	// A name registered in BOTH the remote registry and as a local .gguf
	// file must resolve to the remote entry — the dispatch checks remote
	// first and returns before ever consulting the local list. Regression
	// test for issue #181 (no prior test constructed a genuine name
	// collision across both sources).
	modelsDir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(modelsDir, "shared-name.gguf"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	remotePath := filepath.Join(shortTempDir(t), "models.toml")
	if err := rm.SaveRemoteModels(remotePath, []rm.RemoteModel{
		{Name: "shared-name", Style: "anthropic", URL: "https://gw.example.com", Token: "tok"},
	}); err != nil {
		t.Fatal(err)
	}

	// nil launcher: if dispatch fell through to the local-start path
	// instead of returning the remote entry, calling Start on a nil
	// Launcher would panic — this proves the remote branch wins.
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = modelsDir
	s.remoteModelsPath = remotePath

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "shared-name"}),
	})
	if resp.Error != nil {
		t.Fatalf("ResolveModel error: %+v", resp.Error)
	}
	var res rm.ResolveModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != rm.ModelKindRemote || res.URL != "https://gw.example.com" || res.Style != "anthropic" {
		t.Fatalf("res = %+v, want remote entry to win", res)
	}
}

func TestDispatchResolveModelUnknown(t *testing.T) {
	s := newServer(rm.NewRegistry(), nil)
	s.modelsDir = shortTempDir(t)
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")

	resp := s.dispatch(rm.Envelope{
		JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "nope"}),
	})
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("resp = %#v, want -32001", resp)
	}
}

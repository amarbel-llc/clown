package ringmaster

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestStartInstanceParams_JSON(t *testing.T) {
	p := StartInstanceParams{
		Alias: "coder-32k",
		Model: "qwen3-coder",
		Bind:  "127.0.0.1",
		Args:  []string{"--ctx-size", "32768"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"alias":"coder-32k","model":"qwen3-coder","bind":"127.0.0.1","args":["--ctx-size","32768"]}`
	if string(data) != want {
		t.Errorf("got  %s\nwant %s", data, want)
	}
}

// sampleInstance mirrors the Instance shape in types_test.go so the
// nested-Instance Result tests stay consistent with the Task 1 fixture.
func sampleInstance() Instance {
	return Instance{
		Alias:     "qwen3-coder",
		Model:     "qwen3-coder",
		Port:      43219,
		PID:       91234,
		Bind:      "127.0.0.1",
		StartedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
}

func TestRPC_RoundTrip(t *testing.T) {
	inst := sampleInstance()
	model := AvailableModel{
		Name: "qwen3-coder",
		Path: "/home/user/.local/share/circus/models/qwen3-coder.gguf",
		Size: 4_700_000_000,
	}

	cases := []struct {
		name     string
		value    any
		wantJSON string
		into     any // pointer to a fresh zero value of the same type
	}{
		{
			name:     "StartInstanceResult",
			value:    StartInstanceResult{Instance: inst},
			wantJSON: `{"instance":{"alias":"qwen3-coder","model":"qwen3-coder","port":43219,"pid":91234,"bind":"127.0.0.1","started_at":"2026-05-18T12:00:00Z"}}`,
			into:     new(StartInstanceResult),
		},
		{
			name:     "StopInstanceParams",
			value:    StopInstanceParams{Alias: "coder-32k"},
			wantJSON: `{"alias":"coder-32k"}`,
			into:     new(StopInstanceParams),
		},
		{
			name:     "StopAllParams_empty",
			value:    StopAllParams{},
			wantJSON: `{}`,
			into:     new(StopAllParams),
		},
		{
			name:     "StopAllResult",
			value:    StopAllResult{Stopped: []string{"coder-32k", "scratch"}},
			wantJSON: `{"stopped":["coder-32k","scratch"]}`,
			into:     new(StopAllResult),
		},
		{
			name:     "ListInstancesParams_empty",
			value:    ListInstancesParams{},
			wantJSON: `{}`,
			into:     new(ListInstancesParams),
		},
		{
			name:     "ListInstancesResult",
			value:    ListInstancesResult{Instances: []Instance{inst}},
			wantJSON: `{"instances":[{"alias":"qwen3-coder","model":"qwen3-coder","port":43219,"pid":91234,"bind":"127.0.0.1","started_at":"2026-05-18T12:00:00Z"}]}`,
			into:     new(ListInstancesResult),
		},
		{
			name:     "GetInstanceParams",
			value:    GetInstanceParams{Alias: "qwen3-coder"},
			wantJSON: `{"alias":"qwen3-coder"}`,
			into:     new(GetInstanceParams),
		},
		{
			name:     "GetInstanceResult",
			value:    GetInstanceResult{Instance: inst},
			wantJSON: `{"instance":{"alias":"qwen3-coder","model":"qwen3-coder","port":43219,"pid":91234,"bind":"127.0.0.1","started_at":"2026-05-18T12:00:00Z"}}`,
			into:     new(GetInstanceResult),
		},
		{
			name:     "ListAvailableModelsParams_empty",
			value:    ListAvailableModelsParams{},
			wantJSON: `{}`,
			into:     new(ListAvailableModelsParams),
		},
		{
			name:     "ListAvailableModelsResult",
			value:    ListAvailableModelsResult{Models: []AvailableModel{model}},
			wantJSON: `{"models":[{"name":"qwen3-coder","path":"/home/user/.local/share/circus/models/qwen3-coder.gguf","size":4700000000}]}`,
			into:     new(ListAvailableModelsResult),
		},
		{
			name:     "DownloadModelParams",
			value:    DownloadModelParams{Name: "qwen3-coder"},
			wantJSON: `{"name":"qwen3-coder"}`,
			into:     new(DownloadModelParams),
		},
		{
			name:     "DownloadModelResult",
			value:    DownloadModelResult{Model: model},
			wantJSON: `{"model":{"name":"qwen3-coder","path":"/home/user/.local/share/circus/models/qwen3-coder.gguf","size":4700000000}}`,
			into:     new(DownloadModelResult),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.wantJSON {
				t.Errorf("marshal mismatch:\n got  %s\n want %s", data, tc.wantJSON)
			}
			if err := json.Unmarshal(data, tc.into); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := reflect.ValueOf(tc.into).Elem().Interface()
			if !reflect.DeepEqual(got, tc.value) {
				t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, tc.value)
			}
		})
	}
}

func TestMethodConstants(t *testing.T) {
	// Guard against accidental rename — the wire protocol depends on
	// these exact strings.
	cases := map[string]string{
		MethodStartInstance:       "StartInstance",
		MethodStopInstance:        "StopInstance",
		MethodStopAll:             "StopAll",
		MethodListInstances:       "ListInstances",
		MethodGetInstance:         "GetInstance",
		MethodListAvailableModels: "ListAvailableModels",
		MethodDownloadModel:       "DownloadModel",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("method constant drifted: got %q want %q", got, want)
		}
	}
}

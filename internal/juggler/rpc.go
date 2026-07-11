package juggler

// Method names. Exported as constants so tests, server, and client
// agree without string typos.
const (
	MethodStartInstance       = "StartInstance"
	MethodStopInstance        = "StopInstance"
	MethodStopAll             = "StopAll"
	MethodListInstances       = "ListInstances"
	MethodGetInstance         = "GetInstance"
	MethodListAvailableModels = "ListAvailableModels"
	MethodDownloadModel       = "DownloadModel"
	MethodListModels          = "ListModels"
	MethodAddRemoteModel      = "AddRemoteModel"
	MethodRemoveRemoteModel   = "RemoveRemoteModel"
	MethodResolveModel        = "ResolveModel"
)

// StartInstanceParams launches a new llama-server child. Alias is the
// registry key; Model resolves to a GGUF file in the models dir (or an
// absolute path). Bind defaults to "127.0.0.1" if empty. Args are
// passed through to llama-server.
type StartInstanceParams struct {
	Alias string   `json:"alias"`
	Model string   `json:"model"`
	Bind  string   `json:"bind,omitempty"`
	Args  []string `json:"args,omitempty"`
}
type StartInstanceResult struct {
	Instance Instance `json:"instance"`
}

// StopInstanceParams stops by alias. Returns no result on success.
type StopInstanceParams struct {
	Alias string `json:"alias"`
}

// StopAllParams is empty by convention (no fields). Returns the
// aliases that were stopped.
type (
	StopAllParams struct{}
	StopAllResult struct {
		Stopped []string `json:"stopped"`
	}
)

// ListInstancesParams is empty.
type (
	ListInstancesParams struct{}
	ListInstancesResult struct {
		Instances []Instance `json:"instances"`
	}
)

// GetInstanceParams looks up a single instance by alias.
type GetInstanceParams struct {
	Alias string `json:"alias"`
}
type GetInstanceResult struct {
	Instance Instance `json:"instance"`
}

// ListAvailableModelsParams is empty.
type (
	ListAvailableModelsParams struct{}
	ListAvailableModelsResult struct {
		Models []AvailableModel `json:"models"`
	}
)

// DownloadModelParams identifies a model by registry name.
type DownloadModelParams struct {
	Name string `json:"name"`
}
type DownloadModelResult struct {
	Model AvailableModel `json:"model"`
}

// ModelKind distinguishes a spawned local instance from a static remote
// endpoint in the unified model listing.
type ModelKind string

const (
	ModelKindLocal  ModelKind = "local"
	ModelKindRemote ModelKind = "remote"
)

// Model is one entry in the unified ListModels view. Token is
// deliberately omitted here — only ResolveModel returns a resolved
// secret, keeping list output safe to print/log.
type Model struct {
	Name  string    `json:"name"`
	Kind  ModelKind `json:"kind"`
	Style string    `json:"style,omitempty"` // remote only
}

// ListModelsParams is empty.
type (
	ListModelsParams struct{}
	ListModelsResult struct {
		Models []Model `json:"models"`
	}
)

// AddRemoteModelParams registers (or overwrites, by Name) a remote model.
type AddRemoteModelParams struct {
	Name  string `json:"name"`
	Style string `json:"style"`
	URL   string `json:"url"`
	Token string `json:"token"`
}
type AddRemoteModelResult struct{}

// RemoveRemoteModelParams deletes a remote model by name. No result type;
// the server returns a null result (mirrors StopInstance).
type RemoveRemoteModelParams struct {
	Name string `json:"name"`
}

// ResolveModelParams looks up name in the unified registry.
type ResolveModelParams struct {
	Name string `json:"name"`
}

// ResolveModelResult is what a consumer needs to actually talk to the
// model: for kind "remote", URL + the resolved Token (env-expanded
// server-side) + Style; for kind "local", just URL (the running
// instance's address — ResolveModel starts it if not already running).
type ResolveModelResult struct {
	Kind  ModelKind `json:"kind"`
	URL   string    `json:"url"`
	Token string    `json:"token,omitempty"`
	Style string    `json:"style,omitempty"`
}

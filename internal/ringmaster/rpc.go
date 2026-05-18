package ringmaster

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
type StopAllParams struct{}
type StopAllResult struct {
	Stopped []string `json:"stopped"`
}

// ListInstancesParams is empty.
type ListInstancesParams struct{}
type ListInstancesResult struct {
	Instances []Instance `json:"instances"`
}

// GetInstanceParams looks up a single instance by alias.
type GetInstanceParams struct {
	Alias string `json:"alias"`
}
type GetInstanceResult struct {
	Instance Instance `json:"instance"`
}

// ListAvailableModelsParams is empty.
type ListAvailableModelsParams struct{}
type ListAvailableModelsResult struct {
	Models []AvailableModel `json:"models"`
}

// DownloadModelParams identifies a model by registry name.
type DownloadModelParams struct {
	Name string `json:"name"`
}
type DownloadModelResult struct {
	Model AvailableModel `json:"model"`
}

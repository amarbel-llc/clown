// Package ringmaster defines the wire types and helpers for the
// ringmaster control plane. Both cmd/ringmaster (the daemon) and
// cmd/circus (the CLI client) depend on this package; clown will
// too in a later plan.
package ringmaster

import "time"

// Instance describes a running llama-server child as ringmaster sees it.
// PID is the child process's PID; ringmaster reaps the child on exit.
type Instance struct {
	Alias     string    `json:"alias"`
	Model     string    `json:"model"`
	Port      int       `json:"port"`
	PID       int       `json:"pid"`
	Bind      string    `json:"bind"`
	StartedAt time.Time `json:"started_at"`
}

// AvailableModel describes a GGUF file on disk that can be loaded.
type AvailableModel struct {
	Name string `json:"name"` // bare name without .gguf, e.g. "qwen3-coder"
	Path string `json:"path"` // absolute path to the file
	Size int64  `json:"size"` // bytes
}

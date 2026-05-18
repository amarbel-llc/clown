package ringmaster

import (
	"fmt"
	"sort"
	"sync"
)

// Registry tracks the running llama-server instances. All methods are
// safe for concurrent use. The registry is purely in-memory; there is
// no on-disk persistence.
type Registry struct {
	mu        sync.RWMutex
	instances map[string]Instance
}

func NewRegistry() *Registry {
	return &Registry{instances: make(map[string]Instance)}
}

func (r *Registry) Add(in Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[in.Alias]; ok {
		return fmt.Errorf("alias %q already registered", in.Alias)
	}
	r.instances[in.Alias] = in
	return nil
}

func (r *Registry) Remove(alias string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, alias)
}

func (r *Registry) Get(alias string) (Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.instances[alias]
	return in, ok
}

func (r *Registry) List() []Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Instance, 0, len(r.instances))
	for _, in := range r.instances {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

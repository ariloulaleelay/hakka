package agent

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a named collection of LLM adapters with a default
// selection. It deliberately knows nothing about sessions; choosing an
// adapter for a given session is the Router's job.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]LLMAdapter
	def      string
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[string]LLMAdapter{}}
}

func (reg *Registry) Register(name string, a LLMAdapter) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.adapters[name] = a
	if reg.def == "" {
		reg.def = name
	}
}

func (reg *Registry) SetDefault(name string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.adapters[name]; !ok {
		return fmt.Errorf("registry: unknown adapter %q", name)
	}
	reg.def = name
	return nil
}

func (reg *Registry) Default() string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.def
}

func (reg *Registry) Get(name string) (LLMAdapter, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	a, ok := reg.adapters[name]
	return a, ok
}

// Names returns registered adapter names in sorted order.
func (reg *Registry) Names() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]string, 0, len(reg.adapters))
	for n := range reg.adapters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

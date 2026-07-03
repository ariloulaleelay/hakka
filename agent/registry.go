package agent

import (
	"fmt"
	"sort"
	"sync"
)

// ModelProfile carries per-model metadata alongside the adapter.
// Use GetProfile to read it; profiles are registered via Register
// and updated via SetProfileCompactSoftLimit and SetProfileHacks.
type ModelProfile struct {
	Adapter          LLMAdapter
	CompactSoftLimit int // 0 = use engine config default (resolved at session init)
	Hacks            Hacks
}

// Registry is a named collection of LLM adapters with per-model
// metadata and a default selection.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]ModelProfile
	def      string
}

func NewRegistry() *Registry {
	return &Registry{profiles: map[string]ModelProfile{}}
}

func (reg *Registry) Register(name string, a LLMAdapter) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.profiles[name] = ModelProfile{Adapter: a}
	if reg.def == "" {
		reg.def = name
	}
}

// SetProfileCompactSoftLimit updates the compact soft limit for an
// existing model profile. Silently ignores unknown names so callers
// can safely set limits during config loading without error handling.
func (reg *Registry) SetProfileCompactSoftLimit(name string, limit int) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if p, ok := reg.profiles[name]; ok {
		p.CompactSoftLimit = limit
		reg.profiles[name] = p
	}
}

// SetProfileHacks updates the hacks for an existing model profile.
func (reg *Registry) SetProfileHacks(name string, h Hacks) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if p, ok := reg.profiles[name]; ok {
		p.Hacks = h
		reg.profiles[name] = p
	}
}

func (reg *Registry) SetDefault(name string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.profiles[name]; !ok {
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
	p, ok := reg.profiles[name]
	if !ok {
		return nil, false
	}
	return p.Adapter, true
}

// GetProfile returns the full ModelProfile for the named model.
func (reg *Registry) GetProfile(name string) (ModelProfile, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	p, ok := reg.profiles[name]
	return p, ok
}

// Names returns registered adapter names in sorted order.
func (reg *Registry) Names() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]string, 0, len(reg.profiles))
	for n := range reg.profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

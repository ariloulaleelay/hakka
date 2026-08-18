package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionMetaPatch describes which session metadata fields to update.
// Nil pointer = no change; non-nil = set to this value.
// Map/slice fields: nil = no change; non-nil = replace entire value.
type SessionMetaPatch struct {
	Name                   *string
	Model                  *string
	CompactSoftLimit       *int
	ClientCWD              *string
	EstimatedContextTokens *int
	EnabledTools           map[string]bool
	BlockedTools           map[string]bool
	ActiveSkills           []string
	UpdatedAt              *time.Time
}

// SessionStore persists sessions, keyed by (namespace, id).
type SessionStore interface {
	Get(ctx context.Context, namespace, id string) (*Session, bool, error)
	Put(ctx context.Context, namespace string, s *Session) error
	Delete(ctx context.Context, namespace, id string) error
	List(ctx context.Context, namespace string) ([]*Session, error)

	// AppendMessages appends messages to an existing session and atomically
	// adds deltaTokens and deltaCost to the session's accumulated counters.
	// Does NOT touch any other session metadata (name, model, tools, etc.).
	AppendMessages(ctx context.Context, namespace, id string, msgs []Message,
		deltaTokens int, deltaCost float64) error

	// PatchMeta updates only the non-nil fields in the patch. Fields with
	// nil pointers are left unchanged. Does NOT touch messages.
	PatchMeta(ctx context.Context, namespace, id string, patch *SessionMetaPatch) error
}

// MemoryStore is an in-process, goroutine-safe SessionStore.
// Sessions are stored as deep-copied SessionData to avoid aliasing
// between callers and the store's internal state.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]SessionData // key = "namespace:id"
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]SessionData)}
}

func storeKey(namespace, id string) string {
	return namespace + ":" + id
}

func (ms *MemoryStore) Get(_ context.Context, namespace, id string) (*Session, bool, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	d, ok := ms.data[storeKey(namespace, id)]
	if !ok {
		return nil, false, nil
	}
	cp := d.DeepCopy()
	return NewSessionFromData(&cp), true, nil
}

func (ms *MemoryStore) Put(_ context.Context, namespace string, s *Session) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	data := s.Read()
	data.Namespace = namespace
	ms.data[storeKey(namespace, data.ID)] = data
	return nil
}

func (ms *MemoryStore) Delete(_ context.Context, namespace, id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	delete(ms.data, storeKey(namespace, id))
	return nil
}

func (ms *MemoryStore) List(_ context.Context, namespace string) ([]*Session, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	prefix := namespace + ":"
	var out []*Session
	for key, d := range ms.data {
		if strings.HasPrefix(key, prefix) {
			cp := d.DeepCopy()
			out = append(out, NewSessionFromData(&cp))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := out[i].Read(), out[j].Read()
		return di.UpdatedAt.After(dj.UpdatedAt)
	})
	return out, nil
}

// AppendMessages appends messages to an existing session in memory.
func (ms *MemoryStore) AppendMessages(_ context.Context, namespace, id string, msgs []Message, deltaTokens int, deltaCost float64) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	d, ok := ms.data[storeKey(namespace, id)]
	if !ok {
		return fmt.Errorf("session %q not found in namespace %q", id, namespace)
	}

	d.Messages = append(d.Messages, msgs...)
	d.TotalTokens += deltaTokens
	d.TotalCost += deltaCost
	d.UpdatedAt = time.Now()
	ms.data[storeKey(namespace, id)] = d

	return nil
}

// PatchMeta updates only the non-nil fields on the in-memory session.
func (ms *MemoryStore) PatchMeta(_ context.Context, namespace, id string, patch *SessionMetaPatch) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	d, ok := ms.data[storeKey(namespace, id)]
	if !ok {
		return fmt.Errorf("session %q not found in namespace %q", id, namespace)
	}

	if patch == nil {
		return nil
	}
	if patch.Name != nil {
		d.Name = *patch.Name
	}
	if patch.Model != nil {
		d.Model = *patch.Model
	}
	if patch.CompactSoftLimit != nil {
		d.CompactSoftLimit = *patch.CompactSoftLimit
	}
	if patch.ClientCWD != nil {
		d.ClientCWD = *patch.ClientCWD
	}
	if patch.EstimatedContextTokens != nil {
		d.EstimatedContextTokens = *patch.EstimatedContextTokens
	}
	if patch.EnabledTools != nil {
		d.EnabledTools = patch.EnabledTools
	}
	if patch.BlockedTools != nil {
		d.BlockedTools = patch.BlockedTools
	}
	if patch.ActiveSkills != nil {
		d.ActiveSkills = patch.ActiveSkills
	}
	if patch.UpdatedAt != nil {
		d.UpdatedAt = *patch.UpdatedAt
	} else {
		d.UpdatedAt = time.Now()
	}
	ms.data[storeKey(namespace, id)] = d

	return nil
}

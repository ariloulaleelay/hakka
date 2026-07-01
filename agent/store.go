package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// SessionStore persists sessions, keyed by (namespace, id).
type SessionStore interface {
	Get(ctx context.Context, namespace, id string) (*Session, bool, error)
	Put(ctx context.Context, namespace string, s *Session) error
	Delete(ctx context.Context, namespace, id string) error
	List(ctx context.Context, namespace string) ([]*Session, error)
}

// MemoryStore is an in-process, goroutine-safe SessionStore.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]*Session // key = "namespace:id"
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*Session)}
}

func storeKey(namespace, id string) string {
	return namespace + ":" + id
}

func (ms *MemoryStore) Get(_ context.Context, namespace, id string) (*Session, bool, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	s, ok := ms.data[storeKey(namespace, id)]
	return s, ok, nil
}

func (ms *MemoryStore) Put(_ context.Context, namespace string, s *Session) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s.Update(func(d *SessionData) {
		d.Namespace = namespace
	})
	ms.data[storeKey(namespace, s.SessionID())] = s
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
	for key, s := range ms.data {
		if strings.HasPrefix(key, prefix) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := out[i].Read(), out[j].Read()
		return di.UpdatedAt.After(dj.UpdatedAt)
	})
	return out, nil
}

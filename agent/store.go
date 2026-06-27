package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionStore persists sessions, keyed by (namespace, id). The namespace
// isolates sessions from different gateways (e.g. "tcp", "ws", "tg:12345").
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
	s.Namespace = namespace
	s.UpdatedAt = time.Now()
	ms.data[storeKey(namespace, s.ID)] = s
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
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SessionManager wraps a SessionStore with creation/lookup helpers.
// All store operations require a namespace parameter, which isolates
// sessions from different gateways (e.g. "tcp", "ws", "tg:12345").
type SessionManager struct {
	Store        SessionStore
	SystemPrompt string
}

func NewSessionManager(store SessionStore, systemPrompt string) *SessionManager {
	if store == nil {
		store = NewMemoryStore()
	}
	return &SessionManager{Store: store, SystemPrompt: systemPrompt}
}

// GetOrCreate returns an existing session by (namespace, id), or creates a
// new one in the given namespace. If id is empty a fresh UUID is generated.
func (sm *SessionManager) GetOrCreate(ctx context.Context, namespace, id string) (*Session, error) {
	if id != "" {
		if session, ok, err := sm.Store.Get(ctx, namespace, id); err != nil {
			return nil, err
		} else if ok {
			return session, nil
		}
	}
	session := NewSession(namespace, sm.SystemPrompt)
	if id != "" {
		session.ID = id
	}
	if err := sm.Store.Put(ctx, namespace, session); err != nil {
		return nil, err
	}
	return session, nil
}

// Save persists a session under the given namespace.
// Accepts a SessionView for use by the orchestration layer; internally
// it works with the concrete *Session required by the store.
func (sm *SessionManager) Save(ctx context.Context, namespace string, session SessionView) error {
	sess, ok := session.(*Session)
	if !ok {
		return &ErrBadSession{Msg: "Save: expected *Session"}
	}
	return sm.Store.Put(ctx, namespace, sess)
}

// ErrBadSession is returned when a SessionManager operation receives
// a SessionView that is not backed by a *Session.
type ErrBadSession struct{ Msg string }

func (e *ErrBadSession) Error() string { return e.Msg }

// Drop deletes a session by (namespace, id).
func (sm *SessionManager) Drop(ctx context.Context, namespace, id string) error {
	return sm.Store.Delete(ctx, namespace, id)
}

// List returns all sessions within the given namespace, ordered by creation
// time (oldest first). Returns the full session objects so callers have
// access to metadata such as CreatedAt, Name, and ID without extra lookups.
func (sm *SessionManager) List(ctx context.Context, namespace string) ([]*Session, error) {
	return sm.Store.List(ctx, namespace)
}

// ResolveSessionID resolves a potentially-short session ID prefix to the
// full session ID. It first tries an exact match, then a prefix match
// against all sessions in the namespace. Returns an error if the prefix
// is ambiguous or matches no session.
func (sm *SessionManager) ResolveSessionID(ctx context.Context, namespace, prefix string) (string, error) {
	sessions, err := sm.List(ctx, namespace)
	if err != nil {
		return "", err
	}

	// Exact match first — always preferred over prefix match.
	for _, s := range sessions {
		if s.ID == prefix {
			return s.ID, nil
		}
	}

	// Prefix match
	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no session matching %q", prefix)
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", fmt.Errorf("ambiguous session prefix %q matches %d sessions: %s",
			prefix, len(matches), strings.Join(matches, ", "))
	}
	return matches[0], nil
}

package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SessionManager wraps a SessionStore with creation/lookup helpers.
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
		session.SetID(id)
	}
	if err := sm.Store.Put(ctx, namespace, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (sm *SessionManager) Save(ctx context.Context, namespace string, session SessionView) error {
	sess, ok := session.(*Session)
	if !ok {
		return fmt.Errorf("Save: expected *Session, got %T", session)
	}
	return sm.Store.Put(ctx, namespace, sess)
}

func (sm *SessionManager) Drop(ctx context.Context, namespace, id string) error {
	return sm.Store.Delete(ctx, namespace, id)
}

func (sm *SessionManager) List(ctx context.Context, namespace string) ([]*Session, error) {
	return sm.Store.List(ctx, namespace)
}

func (sm *SessionManager) ResolveSessionID(ctx context.Context, namespace, prefix string) (string, error) {
	sessions, err := sm.List(ctx, namespace)
	if err != nil {
		return "", err
	}

	for _, s := range sessions {
		if s.SessionID() == prefix {
			return s.SessionID(), nil
		}
	}

	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(s.SessionID(), prefix) {
			matches = append(matches, s.SessionID())
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

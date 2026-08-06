package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// Get fetches an existing session. It never creates one.
func (sm *SessionManager) Get(ctx context.Context, namespace, id string) (*Session, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty session ID", ErrSessionNotFound)
	}
	session, ok, err := sm.Store.Get(ctx, namespace, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return session, nil
}

var ErrSessionNotFound = errors.New("session not found")

func (sm *SessionManager) Create(ctx context.Context, namespace string) (*Session, error) {
	session := NewSession(namespace, sm.SystemPrompt)
	if err := sm.Store.Put(ctx, namespace, session); err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "session created", "ns", namespace, "id", session.SessionID())
	return session, nil
}

// CreateWithID creates a new session with an explicitly supplied ID. This is
// intended for restore/import workflows; normal callers should use Create.
func (sm *SessionManager) CreateWithID(ctx context.Context, namespace, id string) (*Session, error) {
	if id == "" {
		return sm.Create(ctx, namespace)
	}
	session := NewSession(namespace, sm.SystemPrompt)
	session.Update(func(d *SessionData) { d.ID = id })
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
	err := sm.Store.Delete(ctx, namespace, id)
	if err != nil {
		slog.ErrorContext(ctx, "session delete failed", "ns", namespace, "id", id, "err", err)
		return err
	}
	slog.InfoContext(ctx, "session deleted", "ns", namespace, "id", id)
	return nil
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

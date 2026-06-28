package agent

import (
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ToolCall represents a model-requested tool invocation.
type ToolCall struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
	ExecSnippet string `json:"exec_snippet,omitempty"`
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single conversation turn.
type Message struct {
	Role             Role           `json:"role"`
	Content          string         `json:"content"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
	Usage            *Usage         `json:"usage,omitempty"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

// ---------------------------------------------------------------------------
// SessionData — plain data struct, no methods, no mutex.
//
// SessionData holds the raw state of a conversation session. It is the
// single source of truth for all session fields. Session (below) wraps
// it with thread-safe access.
//
// The composite primary key is (Namespace, ID), where Namespace isolates
// sessions from different gateways (e.g. "tcp", "ws", "tg:12345").
// ---------------------------------------------------------------------------

type SessionData struct {
	Namespace        string
	ID               string
	SystemPrompt     string
	Messages         []Message
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ClientCWD        string
	Name             string
	Model            string
	TotalTokens      int
	EnabledTools     map[string]bool
	CompactSoftLimit int
}

// NewSessionData creates a SessionData with sensible defaults.
func NewSessionData(namespace, systemPrompt string) SessionData {
	cwd, _ := os.Getwd()
	now := time.Now()
	return SessionData{
		Namespace:        namespace,
		ID:               uuid.NewString(),
		SystemPrompt:     systemPrompt,
		CreatedAt:        now,
		UpdatedAt:        now,
		ClientCWD:        cwd,
		CompactSoftLimit: 0,
	}
}

// DeepCopy returns an independent copy of the SessionData.
func (sd *SessionData) DeepCopy() SessionData {
	cp := *sd
	cp.Messages = make([]Message, len(sd.Messages))
	copy(cp.Messages, sd.Messages)
	if sd.EnabledTools != nil {
		cp.EnabledTools = make(map[string]bool, len(sd.EnabledTools))
		for k, v := range sd.EnabledTools {
			cp.EnabledTools[k] = v
		}
	}
	return cp
}

// ---------------------------------------------------------------------------
// Session — thread-safe wrapper around SessionData.
//
// Session implements the SessionView composite interface.
// For atomic batch reads use Read(); for atomic batch mutations use
// Update() — these acquire the lock once instead of N times.
// ---------------------------------------------------------------------------

type Session struct {
	mu   sync.RWMutex
	data *SessionData
}

// NewSession creates a new Session with default data.
func NewSession(namespace, systemPrompt string) *Session {
	data := NewSessionData(namespace, systemPrompt)
	return &Session{data: &data}
}

// NewSessionFromData wraps an existing SessionData in a Session.
func NewSessionFromData(data *SessionData) *Session {
	return &Session{data: data}
}

// Read returns a deep copy of the underlying data for atomic batch reads.
func (sess *Session) Read() SessionData {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.DeepCopy()
}

// Update applies a mutation function to the underlying data atomically.
func (sess *Session) Update(fn func(data *SessionData)) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	fn(sess.data)
}

// --- SessionIdentity ---

func (sess *Session) SessionID() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.ID
}

func (sess *Session) SessionName() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.Name
}

func (sess *Session) SetSessionName(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.Name = name
}

func (sess *Session) DisplayName() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.data.Name != "" {
		return sess.data.Name
	}
	return sess.data.ID
}

// --- SessionHistory ---

func (sess *Session) Append(msg Message) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.Messages = append(sess.data.Messages, msg)
}

func (sess *Session) History() []Message {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	out := make([]Message, 0, len(sess.data.Messages)+1)
	if sess.data.SystemPrompt != "" {
		out = append(out, Message{Role: RoleSystem, Content: sess.data.SystemPrompt})
	}
	out = append(out, sess.data.Messages...)
	return out
}

func (sess *Session) AllMessages() []Message {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	out := make([]Message, len(sess.data.Messages))
	copy(out, sess.data.Messages)
	return out
}

func (sess *Session) CWDMessage() *Message {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.data.ClientCWD == "" {
		return nil
	}
	return &Message{
		Role:    RoleSystem,
		Content: "You are working in the user's project directory: " + sess.data.ClientCWD + ".\nAll tools operate relative to this directory.\nUse relative paths whenever possible (every tool).",
	}
}

// --- SessionToolAuth ---

func (sess *Session) IsToolEnabled(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if len(sess.data.EnabledTools) == 0 {
		return false
	}
	enabled, ok := sess.data.EnabledTools[name]
	if !ok {
		return false
	}
	return enabled
}

func (sess *Session) EnableTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.data.EnabledTools == nil {
		sess.data.EnabledTools = make(map[string]bool)
	}
	sess.data.EnabledTools[name] = true
}

func (sess *Session) DisableTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.data.EnabledTools == nil {
		sess.data.EnabledTools = make(map[string]bool)
	}
	sess.data.EnabledTools[name] = false
}

// --- SessionModelBinding ---

func (sess *Session) GetModel() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.Model
}

func (sess *Session) SetModel(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.Model = name
}

// --- SessionTokenTracking ---

func (sess *Session) TotalTokenUsage() int {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.TotalTokens
}

func (sess *Session) AddTokenUsage(tokens int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.TotalTokens += tokens
}

func (sess *Session) SetTotalTokenUsage(tokens int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.TotalTokens = tokens
}

// --- SessionCompactLimits ---

func (sess *Session) GetCompactSoftLimit() int {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.CompactSoftLimit
}

func (sess *Session) SetCompactSoftLimit(n int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.CompactSoftLimit = n
}

// --- Non-interface helpers ---

func (sess *Session) SetNamespace(ns string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.Namespace = ns
}

func (sess *Session) SetID(id string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.ID = id
}

func (sess *Session) SetClientCWD(cwd string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.ClientCWD = cwd
}

func (sess *Session) SetUpdatedAt(t time.Time) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.UpdatedAt = t
}

func (sess *Session) SetCreatedAt(t time.Time) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.CreatedAt = t
}

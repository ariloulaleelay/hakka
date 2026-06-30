package agent

import (
	"fmt"
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
// sessions from different gateways (e.g. "ws", "tg:12345").
//
// Tool authorisation model (v2):
//   - BlockedTools[name]=true  → denied (completely invisible to LLM)
//   - BlockedTools missing/false → allowed (visible in system prompt)
//   - EnabledTools[name]=true  → enabled (appears in API "tools" section)
//   - EnabledTools missing/false → disabled (system prompt only, but
//     mutual activation allows execution if LLM calls anyway)
//   - Once a tool appears in conversation history as a tool call, it
//     becomes "locked" and cannot be denied or disabled.
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
	TotalCost        float64
	EstimatedContextTokens int
	EnabledTools     map[string]bool
	BlockedTools     map[string]bool
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
		// Only show_tool is pre-enabled by default so the LLM can
		// discover and enable other tools at runtime.
		// allow_tool and deny_tool are human-only slash commands.
		EnabledTools: map[string]bool{
			"show_tool": true,
		},
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
	if sd.BlockedTools != nil {
		cp.BlockedTools = make(map[string]bool, len(sd.BlockedTools))
		for k, v := range sd.BlockedTools {
			cp.BlockedTools[k] = v
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

// --- SessionToolAuth (v2) ---
//
// Tool authorisation model:
//   - Denied (BlockedTools[name]=true): completely invisible to LLM
//   - Allowed (!BlockedTools[name]): visible in system prompt listing
//   - Enabled (Allowed + EnabledTools[name]=true): in API "tools" section
//   - Locked (tool appears in history calls): cannot be denied/disabled

func (sess *Session) IsToolAllowed(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.data.BlockedTools == nil {
		return true
	}
	return !sess.data.BlockedTools[name]
}

func (sess *Session) IsToolDenied(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.data.BlockedTools == nil {
		return false
	}
	return sess.data.BlockedTools[name]
}

func (sess *Session) IsToolEnabled(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	// Must be allowed first
	if sess.data.BlockedTools != nil && sess.data.BlockedTools[name] {
		return false
	}
	if len(sess.data.EnabledTools) == 0 {
		return false
	}
	enabled, ok := sess.data.EnabledTools[name]
	if !ok {
		return false
	}
	return enabled
}

func (sess *Session) IsToolConfigured(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if len(sess.data.EnabledTools) == 0 {
		return false
	}
	_, ok := sess.data.EnabledTools[name]
	return ok
}

// IsToolLocked returns true if the tool has been used in this session.
func (sess *Session) IsToolLocked(name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	// Check UsedTools map first (fast path)
	// Then scan history for any tool call with this name
	for _, msg := range sess.data.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Name == name {
				return true
			}
		}
	}
	return false
}

func (sess *Session) EnableTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.data.EnabledTools == nil {
		sess.data.EnabledTools = make(map[string]bool)
	}
	sess.data.EnabledTools[name] = true
}

func (sess *Session) DisableTool(name string) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// Check if tool is locked (used in history)
	for _, msg := range sess.data.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Name == name {
				return fmt.Errorf("tool %q is locked because it was already used in this session", name)
			}
		}
	}
	if sess.data.EnabledTools == nil {
		sess.data.EnabledTools = make(map[string]bool)
	}
	sess.data.EnabledTools[name] = false
	return nil
}

func (sess *Session) AllowTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.data.BlockedTools == nil {
		return // already allowed
	}
	delete(sess.data.BlockedTools, name)
	if len(sess.data.BlockedTools) == 0 {
		sess.data.BlockedTools = nil
	}
}

func (sess *Session) DenyTool(name string) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// Check if tool is locked (used in history)
	for _, msg := range sess.data.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Name == name {
				return fmt.Errorf("tool %q is locked because it was already used in this session", name)
			}
		}
	}
	if sess.data.BlockedTools == nil {
		sess.data.BlockedTools = make(map[string]bool)
	}
	sess.data.BlockedTools[name] = true
	return nil
}

func (sess *Session) MarkToolUsed(name string) {
	// This is a no-op at the data level because IsToolLocked scans history.
	// We keep the method for future optimizations (e.g. a UsedTools map).
	// For now, locking is derived from message history.
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

func (sess *Session) TotalCost() float64 {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.TotalCost
}

func (sess *Session) AddCost(cost float64) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.TotalCost += cost
}

func (sess *Session) SetTotalCost(cost float64) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.TotalCost = cost
}

func (sess *Session) GetEstimatedContextTokens() int {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.EstimatedContextTokens
}

func (sess *Session) SetEstimatedContextTokens(tokens int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.EstimatedContextTokens = tokens
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

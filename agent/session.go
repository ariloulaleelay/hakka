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
	ID               string         `json:"id"`
	Role             Role           `json:"role"`
	Content          string         `json:"content"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
	Usage            *Usage         `json:"usage,omitempty"`
	FinishReason     string         `json:"finish_reason,omitempty"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
	Timestamp        int64          `json:"ts,omitempty"` // Unix timestamp in milliseconds
}

// SessionData — plain data struct, no methods, no mutex.
//
// SessionData holds the raw state of a conversation session. It is the
// single source of truth for all session fields. Session (below) wraps
// it with thread-safe access.
//
// The composite primary key is (Namespace, ID), where Namespace isolates
// sessions from different gateways (e.g. "default", "tg:12345").
//
// Tool authorisation is managed through EnabledTools and BlockedTools maps.
// See AGENT.md §"Core Components | Session" for the full v2 model.

type SessionData struct {
	Namespace        string
	ID               string
	ParentID         string // parent session ID (empty = root)
	ForkPoint        string // message ID in parent: last message shared with child (inclusive; empty = no fork)
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
	ActiveSkills     []string // names of loaded skills
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
	if sd.ActiveSkills != nil {
		cp.ActiveSkills = make([]string, len(sd.ActiveSkills))
		copy(cp.ActiveSkills, sd.ActiveSkills)
	}
	return cp
}

// ForkData creates SessionData for a child session by copying messages
// from the parent up to forkPoint (inclusive). If forkPoint is "",
// the child starts with no messages but inherits all parent settings.
//
// If the last copied message is an assistant with tool_calls, the fork
// point is silently advanced to include the corresponding tool results,
// ensuring the child never sees orphaned tool calls.
func (sd *SessionData) ForkData(forkPoint string) (SessionData, error) {
	child := NewSessionData(sd.Namespace, sd.SystemPrompt)

	// Inherit settings.
	child.Model = sd.Model
	child.ClientCWD = sd.ClientCWD
	child.CompactSoftLimit = sd.CompactSoftLimit
	if sd.EnabledTools != nil {
		child.EnabledTools = make(map[string]bool, len(sd.EnabledTools))
		for k, v := range sd.EnabledTools {
			child.EnabledTools[k] = v
		}
	}
	if sd.BlockedTools != nil {
		child.BlockedTools = make(map[string]bool, len(sd.BlockedTools))
		for k, v := range sd.BlockedTools {
			child.BlockedTools[k] = v
		}
	}
	if len(sd.ActiveSkills) > 0 {
		child.ActiveSkills = make([]string, len(sd.ActiveSkills))
		copy(child.ActiveSkills, sd.ActiveSkills)
	}

	child.ParentID = sd.ID
	child.ForkPoint = forkPoint

	// Empty forkPoint means no messages copied — blank child.
	if forkPoint == "" {
		return child, nil
	}

	// Find the fork point index.
	forkIdx := -1
	for i, m := range sd.Messages {
		if m.ID == forkPoint {
			forkIdx = i
			break
		}
	}
	if forkIdx < 0 {
		return child, fmt.Errorf("fork_point %q not found in parent session %s", forkPoint, shortID(sd.ID))
	}

	// Advance past unresolved tool calls. If the last message in the
	// copied range is an assistant with tool_calls, walk forward to
	// include any tool results that follow it within the same turn.
	endIdx := forkIdx
	if endIdx < len(sd.Messages)-1 {
		lastMsg := sd.Messages[endIdx]
		if lastMsg.Role == RoleAssistant && len(lastMsg.ToolCalls) > 0 {
			// Collect the set of tool call IDs from the last assistant.
			needed := make(map[string]bool, len(lastMsg.ToolCalls))
			for _, tc := range lastMsg.ToolCalls {
				needed[tc.ID] = true
			}
			// Advance until all needed tool results are found (or EOM).
			for endIdx+1 < len(sd.Messages) && len(needed) > 0 {
				endIdx++
				m := sd.Messages[endIdx]
				if m.Role == RoleTool && m.ToolCallID != "" {
					delete(needed, m.ToolCallID)
				} else {
					// Hit a non-tool message before all results — stop.
					break
				}
			}
		}
	}

	// Copy messages [0..endIdx] inclusive.
	child.Messages = make([]Message, endIdx+1)
	copy(child.Messages, sd.Messages[:endIdx+1])

	// Defensive cleanup: if the last copied message is an assistant with
	// tool calls whose results are NOT present in the copied range (e.g.
	// the fork point is the parent's in-flight tool call like subagent_run
	// whose result has not been appended yet), strip those unresolved
	// calls so the child never sees orphaned tool_calls. If the message
	// becomes empty, drop it entirely.
	stripUnresolvedToolCalls(&child)

	return child, nil
}

// stripUnresolvedToolCalls removes tool calls without matching tool
// results from the last message of data, if it is an assistant message.
// If that message becomes empty (no content, no remaining calls), it is
// dropped. All other messages are left untouched. It is a no-op for
// sessions whose last message is not an assistant with tool calls.
func stripUnresolvedToolCalls(data *SessionData) {
	msgs := data.Messages
	if len(msgs) == 0 {
		return
	}
	last := msgs[len(msgs)-1]
	if last.Role != RoleAssistant || len(last.ToolCalls) == 0 {
		return
	}

	// Collect all tool call IDs that have a result within the range.
	resolved := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleTool && m.ToolCallID != "" {
			resolved[m.ToolCallID] = true
		}
	}

	var hasUnresolved bool
	filtered := make([]ToolCall, 0, len(last.ToolCalls))
	for _, tc := range last.ToolCalls {
		if resolved[tc.ID] {
			filtered = append(filtered, tc)
		} else {
			hasUnresolved = true
		}
	}
	if !hasUnresolved {
		return
	}

	if last.Content == "" && len(filtered) == 0 {
		data.Messages = msgs[:len(msgs)-1]
		return
	}
	last.ToolCalls = filtered
	msgs[len(msgs)-1] = last
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

func (sess *Session) SystemPrompt() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.SystemPrompt
}

func (sess *Session) SetSessionName(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.Name = name
	sess.data.UpdatedAt = time.Now()
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
	sess.data.UpdatedAt = time.Now()
}

func (sess *Session) Messages() []Message {
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

// GetCWD returns "" if not set.
func (sess *Session) GetCWD() string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.data.ClientCWD
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
	sess.data.UpdatedAt = time.Now()
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

// Metadata is the single point of truth for session metadata format.
// All consumers (get_session, session_info, session_create, session_list,
// welcome, type:"session" frames) must use this method to ensure
// consistent field names and values.
func (sess *Session) Metadata() map[string]any {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	d := sess.data
	return map[string]any{
		"id":                      d.ID,
		"parent_id":               d.ParentID,
		"fork_point":              d.ForkPoint,
		"name":                    d.Name,
		"short_id":                shortID(d.ID),
		"model":                   d.Model,
		"message_count":           len(d.Messages),
		"total_tokens":            d.TotalTokens,
		"total_cost":              d.TotalCost,
		"estimated_context_tokens": d.EstimatedContextTokens,
		"client_cwd":              d.ClientCWD,
		"compact_soft_limit":      d.CompactSoftLimit,
		"created_at":              d.CreatedAt.Format(time.RFC3339),
		"updated_at":              formatTime(d.UpdatedAt, d.CreatedAt),
	}
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func formatTime(t, fallback time.Time) string {
	if t.IsZero() {
		return fallback.Format(time.RFC3339)
	}
	return t.Format(time.RFC3339)
}

// --- ActiveSkills management ---
func (sess *Session) SetClientCWD(cwd string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.ClientCWD = cwd
	sess.data.UpdatedAt = time.Now()
}

func (sess *Session) ActiveSkills() []string {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if len(sess.data.ActiveSkills) == 0 {
		return nil
	}
	out := make([]string, len(sess.data.ActiveSkills))
	copy(out, sess.data.ActiveSkills)
	return out
}

func (sess *Session) AddActiveSkill(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.data.ActiveSkills = append(sess.data.ActiveSkills, name)
	sess.data.UpdatedAt = time.Now()
}

func (sess *Session) RemoveActiveSkill(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i, s := range sess.data.ActiveSkills {
		if s == name {
			sess.data.ActiveSkills = append(sess.data.ActiveSkills[:i], sess.data.ActiveSkills[i+1:]...)
			break
		}
	}
	sess.data.UpdatedAt = time.Now()
}

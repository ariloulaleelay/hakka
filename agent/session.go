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
	Arguments   string `json:"arguments"`             // raw JSON string
	ExecSnippet string `json:"exec_snippet,omitempty"` // user-facing execution snippet; set by Engine
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
	Role       Role           `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`

	// Usage holds the provider-reported token consumption for this
	// message. Only assistant-role messages carry usage; user, system,
	// and tool messages store nil here.
	//
	// This is more precise than any estimate the engine could compute
	// and is recorded per-call so consumers (hooks, session_info,
	// gateways) can inspect per-message token cost.
	Usage *Usage `json:"usage,omitempty"`
	// ProviderMetadata holds provider-specific data that the core engine
	// treats as opaque. Each adapter stores its own key-value pairs here:
	//
	//   OpenAI:  "reasoning_content" (string) — chain-of-thought trace
	//   Gemini:  "gemini_signatures" ([]string) — thought signatures for
	//            function calls
	//
	// Using a map instead of dedicated fields keeps the core domain model
	// stable when new providers are added (Open/Closed Principle).
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

// Session holds the durable state of a conversation: messages, system
// prompt, and metadata. It deliberately does not know about LLM adapters
// or tool registries; only the Router and Engine read model/totalTokens.
//
// The composite primary key is (Namespace, ID), where Namespace isolates
// sessions from different gateways (e.g. "tcp", "ws", "tg:12345").
//
// CWD is stored as ClientCWD but is NOT injected into History() —
// that is the responsibility of the Conversation layer, which builds
// the full context via BuildContext().
type Session struct {
	Namespace    string    `json:"namespace"`
	ID           string    `json:"id"`
	SystemPrompt string    `json:"system_prompt"`
	Messages     []Message `json:"messages"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	// ClientCWD is the working directory of the client (e.g. Neovim).
	// Defaults to the server's working directory at session creation time.
	// Injected into the LLM context by Conversation.BuildContext().
	ClientCWD string `json:"client_cwd,omitempty"`

	// Name is a human-readable label for the session.
	// Empty by default; auto-generated or manually set via /session rename.
	Name string `json:"name,omitempty"`

	// Model is the LLM model bound to this session.
	Model string `json:"model,omitempty"`

	// TotalTokens is the accumulated token usage.
	TotalTokens int `json:"total_tokens,omitempty"`

	// EnabledTools is a whitelist of tool names that the LLM may use in
	// this session. When nil or empty, NO tools are enabled — the user
	// must explicitly enable tools via /tool commands.
	// Only tools listed here will appear in the LLM's tool schemas.
	EnabledTools map[string]bool `json:"enabled_tools,omitempty"`

	// CompactSoftLimit is the token threshold at which the engine
	// prompts the LLM to call context_compactify. When the estimated
	// context exceeds this value, [N] index prefixes and a system
	// warning are injected.
	// 0 (default) means "use engine config default" (DefaultEngineConfig()).
	CompactSoftLimit int `json:"compact_soft_limit,omitempty"`

	mu sync.Mutex
}

// CWDMessage returns a system message informing the LLM of the working
// directory, or nil if ClientCWD is empty.
func (sess *Session) CWDMessage() *Message {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.ClientCWD == "" {
		return nil
	}
	return &Message{
		Role:    RoleSystem,
		Content: "You are working in the user's project directory: " + sess.ClientCWD + ".\nAll tools operate relative to this directory.\nUse relative paths whenever possible (every tool).",
	}
}

// DisplayName returns the human-readable name if set, otherwise the session ID.
func (sess *Session) DisplayName() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.Name != "" {
		return sess.Name
	}
	return sess.ID
}

func NewSession(namespace, systemPrompt string) *Session {
	cwd, _ := os.Getwd()
	now := time.Now()
	return &Session{
		Namespace:         namespace,
		ID:                uuid.NewString(),
		SystemPrompt:      systemPrompt,
		CreatedAt:         now,
		UpdatedAt:         now,
		ClientCWD:         cwd,
		CompactSoftLimit:  0, // 0 means "use engine config default"; see DefaultEngineConfig()
	}
}

func (sess *Session) Append(msg Message) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.Messages = append(sess.Messages, msg)
}

// History returns the conversation messages preceded by the system
// prompt (if set), in the order they were recorded. This method does
// NOT inject ClientCWD — call BuildContext(session) for that.
func (sess *Session) History() []Message {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	out := make([]Message, 0, len(sess.Messages)+1)
	if sess.SystemPrompt != "" {
		out = append(out, Message{Role: RoleSystem, Content: sess.SystemPrompt})
	}
	out = append(out, sess.Messages...)
	return out
}

func (sess *Session) GetModel() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.Model
}

func (sess *Session) SetModel(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.Model = name
}

func (sess *Session) TotalTokenUsage() int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.TotalTokens
}

func (sess *Session) AddTokenUsage(tokens int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.TotalTokens += tokens
}

// SetTotalTokenUsage replaces the accumulated token count.
// Used by session stores when deserializing.
func (sess *Session) SetTotalTokenUsage(tokens int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.TotalTokens = tokens
}

// IsToolEnabled reports whether the named tool is enabled for this session.
// Thread-safe.
//
// When EnabledTools is nil or empty (no explicit configuration), NO tools
// are enabled — the user must explicitly enable tools via /tool enable or
// /tool enable or /tool disable commands before the LLM can use them.
// Once at least one tool has been explicitly enabled or disabled, the map
// is consulted: tools with a true value are enabled, tools with a false
// value are disabled, and tools not mentioned are also disabled (opt-in
// model — you must explicitly enable tools you want).
func (sess *Session) IsToolEnabled(name string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.EnabledTools) == 0 {
		return false // nothing configured → all tools disabled
	}
	enabled, ok := sess.EnabledTools[name]
	if !ok {
		return false // not explicitly mentioned → disabled (opt-in model)
	}
	return enabled
}

// EnableTool marks the named tool as enabled for this session. Thread-safe.
func (sess *Session) EnableTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.EnabledTools == nil {
		sess.EnabledTools = make(map[string]bool)
	}
	sess.EnabledTools[name] = true
}

// DisableTool marks the named tool as disabled for this session. Thread-safe.
func (sess *Session) DisableTool(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.EnabledTools == nil {
		sess.EnabledTools = make(map[string]bool)
	}
	sess.EnabledTools[name] = false
}

// GetCompactSoftLimit returns the session's compaction soft limit.
func (sess *Session) GetCompactSoftLimit() int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.CompactSoftLimit
}

// SetCompactSoftLimit sets the session's compaction soft limit.
func (sess *Session) SetCompactSoftLimit(n int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.CompactSoftLimit = n
}

// SessionID returns the unique identifier of this session.
// Implements SessionView.
func (sess *Session) SessionID() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.ID
}

// SessionName returns the human-readable name, or "" if none is set.
// Implements SessionView.
func (sess *Session) SessionName() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.Name
}

// SetSessionName sets the human-readable name for this session.
// Implements SessionView.
func (sess *Session) SetSessionName(name string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.Name = name
}

// AllMessages returns the raw conversation messages (without system prompt).
// Implements SessionView.
func (sess *Session) AllMessages() []Message {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	out := make([]Message, len(sess.Messages))
	copy(out, sess.Messages)
	return out
}

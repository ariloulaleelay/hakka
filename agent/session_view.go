package agent

// ---------------------------------------------------------------------------
// Role-specific session interfaces (Interface Segregation Principle).
//
// Each consumer depends on the narrowest possible interface — no client
// should be forced to depend on methods it does not use.
//
//   - Router depends on SessionModelBinding
//   - ToolRegistry.SchemasForSession depends on SessionToolAuth
//   - ToolRegistry.ExecuteForSession depends on SessionToolAuth + SessionIdentity
//   - BuildContext depends on SessionHistory
//   - Conversation / StreamSession use the full SessionView composite
// ---------------------------------------------------------------------------

// SessionIdentity provides session identification and naming.
type SessionIdentity interface {
	SessionID() string
	SessionName() string
	SetSessionName(name string)
	DisplayName() string
}

// SessionHistory provides read/write access to conversation messages.
type SessionHistory interface {
	Append(msg Message)
	History() []Message      // system prompt + conversation messages
	AllMessages() []Message  // raw conversation messages only (no system prompt)
	CWDMessage() *Message
}

// SessionToolAuth authorises tool usage for this session.
//
// Tool authorisation model (v2):
//   - Denied → completely invisible (not in system prompt, not in API tools)
//   - Allowed → visible in system prompt. If enabled, also in API "tools".
//   - Locked → tool has been called; cannot be denied or disabled.
type SessionToolAuth interface {
	IsToolEnabled(name string) bool
	IsToolConfigured(name string) bool
	IsToolAllowed(name string) bool
	IsToolDenied(name string) bool
	IsToolLocked(name string) bool
}

// SessionToolEditor provides write access to tool auth settings.
// Tool handlers that need to modify tool access (e.g. allow_tool, deny_tool)
// should depend on this narrow interface instead of the full SessionView.
type SessionToolEditor interface {
	EnableTool(name string)
	DisableTool(name string) error
	AllowTool(name string)
	DenyTool(name string) error
	MarkToolUsed(name string)
}

// SessionModelBinding stores and retrieves the session's model binding.
type SessionModelBinding interface {
	GetModel() string
	SetModel(name string)
}

// SessionTokenTracking tracks accumulated token usage.
// SessionTokenTracking tracks accumulated token usage and estimated
// context size for observability.
type SessionTokenTracking interface {
	TotalTokenUsage() int
	AddTokenUsage(tokens int)
	SetTotalTokenUsage(tokens int)

	// GetEstimatedContextTokens returns the last estimated context size in tokens.
	GetEstimatedContextTokens() int

	// SetEstimatedContextTokens stores the estimated context size in tokens.
	SetEstimatedContextTokens(tokens int)
}

// SessionCompactLimits provides per-session compaction settings.
type SessionCompactLimits interface {
	GetCompactSoftLimit() int
	SetCompactSoftLimit(n int)
}

// SessionView is the full composite interface that the orchestration
// layer (Conversation, StreamSession, SessionManager) depends on.
//
// New code should depend on one of the narrower role interfaces above
// instead of this composite, unless it genuinely needs all capabilities.
type SessionView interface {
	SessionIdentity
	SessionHistory
	SessionToolAuth
	SessionToolEditor
	SessionModelBinding
	SessionTokenTracking
	SessionCompactLimits
}

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
type SessionToolAuth interface {
	IsToolEnabled(name string) bool
}

// SessionModelBinding stores and retrieves the session's model binding.
type SessionModelBinding interface {
	GetModel() string
	SetModel(name string)
}

// SessionTokenTracking tracks accumulated token usage.
type SessionTokenTracking interface {
	TotalTokenUsage() int
	AddTokenUsage(tokens int)
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
	SessionModelBinding
	SessionTokenTracking
}

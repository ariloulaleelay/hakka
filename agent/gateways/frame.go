package gateways

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Wire Protocol
//
// Every frame (inbound and outbound) has a mandatory "type" field that
// acts as the sole discriminant.
//
// Inbound types:  "chat", "cmd", "resp", "cancel"
// Outbound types: "welcome", "delta", "done", "tool", "usage",
//                 "req", "result", "session", "error"
//
// LLM content is always carried in a field called "text" — whether it's
// a streaming delta, a non-stream output, or the final done payload.
// The discriminator is the "type" field alone.
// ---------------------------------------------------------------------------

// CommandRequest is a structured command sent by a JSON-capable client.
type CommandRequest struct {
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// FrameRequest is the inbound envelope.
type FrameRequest struct {
	Type      string          `json:"type"`                   // "chat", "cmd", "resp", "cancel"
	SessionID string          `json:"session_id,omitempty"`
	Input     string          `json:"input,omitempty"`        // for "chat"
	Stream    bool            `json:"stream,omitempty"`       // for "chat"
	Command   *CommandRequest `json:"command,omitempty"`      // for "cmd"
	RequestID string          `json:"request_id,omitempty"`   // for "resp"
	Result    json.RawMessage `json:"result,omitempty"`       // for "resp"
	ReqError  string          `json:"error,omitempty"`        // for "resp"
}

// TurnStats is the end-of-turn statistics embedded in a "done" frame.
type TurnStats struct {
	TotalTokens            int     `json:"total_tokens"`
	TotalCost              float64 `json:"total_cost"`
	MessageCount           int     `json:"message_count"`
	EstimatedContextTokens int     `json:"estimated_context_tokens"`
	Model                  string  `json:"model"`
}

// FrameResponse is the outbound envelope.
// Every frame has a mandatory "type" field — the sole discriminant.
//
// Fields are shared across frame types but always set in mutually exclusive
// combinations — the type field tells the client which fields to expect.
type FrameResponse struct {
	// Common fields
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`

	// --- "delta" / "done" fields ---
	// LLM content is always "text", regardless of streaming or final.
	Text   string `json:"text,omitempty"`

	// --- "done" fields ---
	Error     string     `json:"error,omitempty"`     // turn error (done) or tool error (tool.status=="err")
	Cancelled bool       `json:"cancelled,omitempty"`
	Stats     *TurnStats `json:"stats,omitempty"`

	// --- "tool" fields ---
	Tool       string          `json:"tool,omitempty"`
	ID         string          `json:"id,omitempty"`         // tool call ID (correlates start↔ok/err)
	Status     string          `json:"status,omitempty"`     // "start", "ok", "err"
	Args       json.RawMessage `json:"args,omitempty"`       // tool arguments (on "start")
	Snippet    string          `json:"snippet,omitempty"`    // human-readable summary
	ToolResult string          `json:"result,omitempty"`     // tool output (on "ok")

	// --- "usage" fields ---
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	DurationNS       *int64   `json:"duration_ns,omitempty"`
	Cost             *float64 `json:"cost,omitempty"`
	TotalCost        *float64 `json:"total_cost,omitempty"`
	EstimatedTokens  *int     `json:"estimated_context_tokens,omitempty"`

	// --- "req" fields (flat — no nested client_request object) ---
	RequestID string `json:"request_id,omitempty"`
	Command   string `json:"command,omitempty"` // client-specific command (e.g. Lua code for Neovim)

	// --- "result" fields ---
	Cmd  string         `json:"cmd,omitempty"`
	Data map[string]any `json:"data,omitempty"`

	// --- "session" / "welcome" fields ---
	// Sessions list (welcome) / session object (session events) /
	// messages list (get_session) are at top level, not nested in "data".
	Sessions []map[string]any `json:"sessions"`
	Session  map[string]any   `json:"session,omitempty"`
	Messages []map[string]any `json:"messages,omitempty"`
	// Events is a replay-friendly sequence of typed events (chat, delta, tool,
	// usage, done) that mirrors the live wire protocol. Returned alongside
	// Messages for backward compatibility. Clients can use Events to render
	// history with the same code path as live streaming frames.
	Events []map[string]any `json:"events,omitempty"`

	// --- "session" lifecycle fields ---
	Event   string `json:"event,omitempty"` // "session_create", "get_session", "renamed", "deleted"
	OldName string `json:"old_name,omitempty"`
	Name    string `json:"name,omitempty"`
}

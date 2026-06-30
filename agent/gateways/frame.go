package gateways

import (
	"encoding/json"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// v2 Wire Protocol
//
// Every frame (inbound and outbound) has a mandatory "type" field that
// acts as the sole discriminant. No more ambiguous "event" / "data" / "done"
// combinations.
//
// Inbound types:  "chat", "cmd", "resp", "cancel"
// Outbound types: "welcome", "delta", "output", "done", "tool", "usage",
//                 "req", "result", "session", "error"
// ---------------------------------------------------------------------------

// CommandRequest is a structured command sent by a JSON-capable client.
type CommandRequest struct {
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// FrameRequest is the inbound envelope.
// Every frame has a mandatory "type" field.
type FrameRequest struct {
	Type      string          `json:"type"`                   // "chat", "cmd", "resp", "cancel"
	SessionID string          `json:"session_id,omitempty"`
	Input     string          `json:"input,omitempty"`        // for "chat"
	Stream    bool            `json:"stream,omitempty"`       // for "chat"
	Command   *CommandRequest `json:"command,omitempty"`      // for "cmd"
	Cwd       string          `json:"cwd,omitempty"`          // for "chat" or "cmd"
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
// Some fields are context-sensitive (e.g. "error" is used both for
// done-level errors and tool-level errors). This is safe because they
// are set in mutually exclusive frame types — never simultaneously.
type FrameResponse struct {
	// Common fields
	Type      string `json:"type"`                // discriminant
	SessionID string `json:"session_id,omitempty"`

	// --- "welcome" fields ---
	ProtocolVersion string `json:"protocol_version,omitempty"`

	// --- "delta" / "output" fields ---
	Text   string `json:"text,omitempty"`         // streaming text chunk
	Output string `json:"output,omitempty"`       // full non-stream reply

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

	// --- "req" fields ---
	ClientReq *event.ClientRequest `json:"client_request,omitempty"`

	// --- "result" fields ---
	Cmd  string         `json:"cmd,omitempty"`
	Data map[string]any `json:"data,omitempty"`

	// --- "session" fields ---
	SessionEvent string `json:"event,omitempty"` // "created", "renamed", "deleted"
	OldName      string `json:"old_name,omitempty"`
	Name         string `json:"name,omitempty"`

	// --- Legacy fields (kept for backward compat during migration) ---
	Delta       string `json:"delta,omitempty"`
	Done        bool   `json:"done,omitempty"`
	Event       string `json:"event,omitempty"`
	ExecSnippet string `json:"exec_snippet,omitempty"`
}

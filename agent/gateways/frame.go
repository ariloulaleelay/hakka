package gateways

import (
	"encoding/json"

	"github.com/you/hakka/agent/event"
)

// FrameRequest is the inbound envelope shared by all gateways.
type FrameRequest struct {
	Type      string          `json:"type,omitempty"`      // "request" (default) or "response"
	SessionID string          `json:"session_id,omitempty"`
	Input     string          `json:"input,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
	// Cwd is the client's current working directory. The client should
	// send this once (e.g. on the first request) or whenever it changes.
	// When set, it is stored on the session and injected into the
	// conversation history as a system message.
	Cwd string `json:"cwd,omitempty"`
	// Response fields (for tool→client communication)
	RequestID string          `json:"request_id,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	ReqError  string          `json:"error,omitempty"`
}

// FrameResponse is the outbound envelope shared by all gateways.
type FrameResponse struct {
	SessionID string `json:"session_id,omitempty"`
	Output    string `json:"output,omitempty"`
	Delta     string `json:"delta,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Error     string `json:"error,omitempty"`
	// Event frames for non-text gateway signals (e.g. tool calls).
	Event  string         `json:"event,omitempty"`  // "tool" | "meta" | "client_request"
	Tool   string         `json:"tool,omitempty"`   // tool name
	Status string         `json:"status,omitempty"` // "start" | "ok" | "err"
	Data   map[string]any `json:"data,omitempty"`   // for meta events
	// ExecSnippet is a short human-readable summary of the tool arguments
	// shown to the user while the tool is executing.
	ExecSnippet string `json:"exec_snippet,omitempty"`
	// ClientReq is set when Event == "client_request" — the agent is asking the
	// client to execute a Vim command.
	ClientReq *event.ClientRequest `json:"vim_request,omitempty"`
}
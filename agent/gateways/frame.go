package gateways

import (
	"encoding/json"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// CommandRequest is a structured command sent by a JSON-capable client.
// It replaces the old text-based slash commands (/session list, etc.).
type CommandRequest struct {
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// FrameRequest is the inbound envelope shared by all gateways.
type FrameRequest struct {
	Type      string          `json:"type,omitempty"` // "request" (default), "response", "cancel", "init"
	SessionID string          `json:"session_id,omitempty"`
	Input     string          `json:"input,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
	Command   *CommandRequest `json:"command,omitempty"` // structured command (JSON-capable clients)
	// Cwd is the client's current working directory. The client should
	// send this once (e.g. on the first request) or whenever it changes.
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
	// Event frames for non-text gateway signals.
	Event  string         `json:"event,omitempty"`  // "tool" | "meta" | "command_result" | "init"
	Tool   string         `json:"tool,omitempty"`   // tool name
	Status string         `json:"status,omitempty"` // "start" | "ok" | "err"
	Data   map[string]any `json:"data,omitempty"`   // for meta/command_result events
	// Args carries structured tool arguments (JSON object).
	Args json.RawMessage `json:"args,omitempty"`
	// Cmd is set when Event == "command_result" — the command that produced it.
	Cmd string `json:"cmd,omitempty"`
	// ExecSnippet is a short human-readable summary of the tool arguments.
	ExecSnippet string `json:"exec_snippet,omitempty"`
	// ClientReq is set when Event == "vim_request" / "client_request".
	ClientReq *event.ClientRequest `json:"vim_request,omitempty"`
}

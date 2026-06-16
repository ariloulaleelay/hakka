// Package event defines the boundary types used by the engine and
// gateways/transports to communicate. It has no dependencies on the
// rest of the agent package, so transport layers can import it without
// pulling in the engine core.
package event

import (
	"context"
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Engine events — typed events emitted by Conversation/StreamSession
// during a single turn. Transports consume these and convert them to
// wire frames.
// ---------------------------------------------------------------------------

// EngineEvent is a marker interface for all event types.
type EngineEvent interface{ engineEvent() }

// ToolCallStarted is emitted when the engine begins executing a tool.
type ToolCallStarted struct {
	SessionID   string
	ID          string
	Name        string
	Arguments   string
	ExecSnippet string
}

func (ToolCallStarted) engineEvent() {}

// ToolCallFinished is emitted when a tool handler returns (success or error).
// Result is a typed ToolResult: callers should use Result.IsError() and
// Result.Output / Result.Err instead of scanning a raw string. The Err
// field on this event is a redundant shortcut equal to Result.Err and
// retained for backward compatibility with existing observers.
type ToolCallFinished struct {
	SessionID   string
	ID          string
	Name        string
	Arguments   string
	Result      ToolResult
	Err         error // nil on success; mirrors Result.Err
	ExecSnippet string
}

func (ToolCallFinished) engineEvent() {}

// UsageInfo carries token usage information for a single LLM call.
type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// UsageReported is emitted after every successful LLM call (including
// intermediate tool-call rounds). Consumers can use this to track token
// usage.
type UsageReported struct {
	SessionID string
	Usage     UsageInfo
}

func (UsageReported) engineEvent() {}

// TextDelta is emitted during streaming for each content chunk from the LLM.
type TextDelta struct {
	SessionID string
	Delta     string
}

func (TextDelta) engineEvent() {}

// TurnFinished is emitted once at the end of a turn, carrying the final
// assistant message or an error.
type TurnFinished struct {
	SessionID   string
	Reply       string
	Err         error
	TotalTokens int // accumulated token usage from the session
}

func (TurnFinished) engineEvent() {}

// ---------------------------------------------------------------------------
// Client communication — allows tools to send requests to the client
// (e.g. Neovim) and receive responses over the same connection.
//
// These types are intentionally transport-agnostic. A "client" is any
// entity that connects to the engine through a gateway (TCP, WebSocket,
// etc.) — it may be Neovim, a REPL, a web UI, or any other frontend.
// ---------------------------------------------------------------------------

// ClientRequest describes a request the agent sends to the connected client.
// The client executes the command (e.g. a Lua expression in Neovim) and
// sends back a response.
type ClientRequest struct {
	RequestID string `json:"request_id"`
	Command   string `json:"command"` // client-specific command (e.g. Lua code for Neovim)
}

// ClientResponse is the reply from the client after executing a ClientRequest.
type ClientResponse struct {
	RequestID string          `json:"request_id"`
	Result    json.RawMessage `json:"result,omitempty"` // JSON-encoded result
	Error     string          `json:"error,omitempty"`  // non-empty on failure
}

// ClientWriter is the interface that lets tools send frames back to the
// client (e.g. over the TCP/WS connection). It is the tool's only channel
// to the outside world.
type ClientWriter interface {
	// WriteFrame sends an event frame to the client. Not all transports
	// support all events; unknown events are silently dropped.
	WriteFrame(Frame) error
}

// Frame is a generic outbound envelope for tool→client communication.
type Frame struct {
	SessionID string         `json:"session_id,omitempty"`
	Event     string         `json:"event,omitempty"`       // e.g. "client_request"
	Data      map[string]any `json:"data,omitempty"`        // for simple payloads
	ClientReq *ClientRequest `json:"client_request,omitempty"` // for client requests
}

// ResponseReader allows a tool to block until a matching response arrives
// from the client. Each tool call gets its own response channel keyed by
// request ID.
type ResponseReader interface {
	// AwaitResponse blocks until the client sends a response with the
	// given request_id, or the context is cancelled. Returns nil on
	// cancellation.
	AwaitResponse(ctx context.Context, requestID string) *ClientResponse
}

// ---------------------------------------------------------------------------
// Context keys — for storing ClientWriter, ResponseReader, CWD, and
// namespace in the context so tools can access them.
// ---------------------------------------------------------------------------

type clientWriterKey struct{}
type responseReaderKey struct{}
type clientCWDKey struct{}
type sessionNamespaceKey struct{}

// ContextWithCWD stores the client working directory in the context so
// tools can access it and resolve relative paths against it.
func ContextWithCWD(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, clientCWDKey{}, cwd)
}

// CWDFromContext returns the client working directory stored in the
// context, or empty string if not set.
func CWDFromContext(ctx context.Context) string {
	cwd, _ := ctx.Value(clientCWDKey{}).(string)
	return cwd
}

// ContextWithNamespace stores the session namespace in the context so
// session-control tools can scope their operations to the correct
// isolated namespace (e.g. "tcp", "tg:12345").
func ContextWithNamespace(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, sessionNamespaceKey{}, ns)
}

// NamespaceFromContext returns the session namespace stored in the
// context, or "" if not set.
func NamespaceFromContext(ctx context.Context) string {
	ns, _ := ctx.Value(sessionNamespaceKey{}).(string)
	return ns
}

// ContextWithClient stores the client writer and response reader in the
// context so tools can communicate with the client.
func ContextWithClient(ctx context.Context, cw ClientWriter, rr ResponseReader) context.Context {
	ctx = context.WithValue(ctx, clientWriterKey{}, cw)
	ctx = context.WithValue(ctx, responseReaderKey{}, rr)
	return ctx
}

// ClientFromContext returns the ClientWriter stored in the context, or nil.
func ClientFromContext(ctx context.Context) ClientWriter {
	cw, _ := ctx.Value(clientWriterKey{}).(ClientWriter)
	return cw
}

// ResponseReaderFromContext returns the ResponseReader stored in the context, or nil.
func ResponseReaderFromContext(ctx context.Context) ResponseReader {
	rr, _ := ctx.Value(responseReaderKey{}).(ResponseReader)
	return rr
}

// ClientRequestSent is emitted when a tool wants to send a client_request
// to the client. The event loop (processEvent) writes the frame to the
// wire, so all outbound communication is serialised through a single
// goroutine — preventing races on the shared frameWriter.
type ClientRequestSent struct {
	SessionID string
	RequestID string
	Command   string
}

func (ClientRequestSent) engineEvent() {}

// ---------------------------------------------------------------------------
// ToolResult — typed outcome of a tool invocation.
//
// A tool either succeeds (Err == nil, Output carries the model-facing
// payload) or fails recoverably (Err != nil, the engine renders an
// "Error: ..." line for the LLM via ForLLM()). Carrying success/failure
// as a typed field instead of scanning the output for a literal "Error: "
// prefix removes a whole class of false positives (e.g. a read_file
// result whose own content happens to start with "Error: ").
// ---------------------------------------------------------------------------

// ToolResult is the outcome of running a single tool.
type ToolResult struct {
	// Output is the success payload. When Err != nil it may still carry
	// a human-readable hint, but ForLLM() will prefer the error message.
	Output string

	// Err is non-nil for recoverable errors that the model should see
	// and can self-correct from. Unrecoverable errors are handled
	// outside the tool path and never wrapped in a ToolResult.
	Err error
}

// IsError reports whether the result represents a tool failure.
func (r ToolResult) IsError() bool { return r.Err != nil }

// ForLLM renders the result as the plain-text string that is appended
// to the conversation history and shown to the model. On success it is
// the raw Output; on failure it is "Error: <message>" — the textual
// convention adapters and the model already understand.
func (r ToolResult) ForLLM() string {
	if r.Err != nil {
		return "Error: " + r.Err.Error()
	}
	return r.Output
}

// SuccessResult builds a ToolResult for a successful invocation.
func SuccessResult(output string) ToolResult {
	return ToolResult{Output: output}
}

// ErrorResult builds a ToolResult for a recoverable tool failure.
// The error message is what the model will see (prefixed with "Error: "
// at the boundary by ForLLM).
func ErrorResult(err error) ToolResult {
	return ToolResult{Err: err}
}

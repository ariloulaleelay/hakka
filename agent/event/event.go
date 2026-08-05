// Package event defines the boundary types used by the engine and
// gateways/transports to communicate. It has no dependencies on the
// rest of the agent package, so transport layers can import it without
// pulling in the engine core.
package event

import (
	"context"
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Engine events — typed events emitted by Conversation
// during a single turn. Transports consume these and convert them to
// wire frames.
// ---------------------------------------------------------------------------

// EngineEvent is a marker interface for all event types.
type EngineEvent interface{ engineEvent() }

type ToolCallStarted struct {
	SessionID   string
	ID          string
	Name        string
	Arguments   string
	ExecSnippet string
}

func (ToolCallStarted) engineEvent() {}

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

// UsageInfo carries token usage and cost information for a single LLM call.
// Duration is the wall-clock time of the LLM call (nanoseconds),
// set by the engine as an informational metric.
// Cost is the monetary cost in USD, extracted from the provider response.
// TotalCost is the accumulated cost across the entire session.
// EstimatedContextTokens is the last estimated context size before the LLM call.
type UsageInfo struct {
	PromptTokens           int
	CompletionTokens       int
	TotalTokens            int
	Duration               time.Duration
	Cost                   float64
	TotalCost              float64
	EstimatedContextTokens int
}

type UsageReported struct {
	SessionID string
	Usage     UsageInfo
}

func (UsageReported) engineEvent() {}

type TextDelta struct {
	SessionID string
	Delta     string
	MessageID string // ID of the assistant message being streamed
}

func (TextDelta) engineEvent() {}

type TurnFinished struct {
	SessionID              string
	Reply                  string
	Err                    error
	TotalTokens            int     // accumulated token usage from the session
	TotalCost              float64 // accumulated monetary cost in USD
	MessageCount           int     // total messages in session history
	EstimatedContextTokens int     // last estimated context size in tokens
	Model                  string  // model used for this turn
	MessageID              string  // ID of the final assistant message
}

func (TurnFinished) engineEvent() {}

type SessionRenamed struct {
	SessionID string
	OldName   string
	NewName   string
}

func (SessionRenamed) engineEvent() {}

// SessionCreated is emitted by tools that create a new session during
// a turn (subagent_run, session_create). Transports map it to a
// type:"session" frame with event:"session_create" and broadcast it
// through the namespace hub so all connected clients see the new
// session immediately — no reconnect or manual refresh needed.
type SessionCreated struct {
	SessionID string
	Session   map[string]any // session.Metadata() — already carries parent_id, fork_point, etc.
}

func (SessionCreated) engineEvent() {}

// QuotaUpdated is emitted after a turn completes (or on model switch) when
// the provider adapter supports quota fetching. Gateways map it to a
// type:"quota" wire frame so clients can display balance info.
type QuotaUpdated struct {
	Provider string
	Balance  *float64 `json:"balance,omitempty"`
	Currency string   `json:"currency,omitempty"`
}

func (QuotaUpdated) engineEvent() {}

// ---------------------------------------------------------------------------
// Client communication — allows tools to send requests to the client
// (e.g. Neovim) and receive responses over the same connection.
//
// These types are intentionally transport-agnostic. A "client" is any
// entity that connects to the engine through a gateway (WebSocket,
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
// client (e.g. over the WebSocket connection). It is the tool's only channel
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
type sessionIDKey struct{}
type sessionViewKey struct{}

// ContextWithCWD stores the client working directory in the context so
// tools can access it and resolve relative paths against it.
func ContextWithCWD(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, clientCWDKey{}, cwd)
}

func CWDFromContext(ctx context.Context) string {
	cwd, _ := ctx.Value(clientCWDKey{}).(string)
	return cwd
}

// ContextWithNamespace stores the session namespace in the context so
// session-control tools can scope their operations to the correct
// isolated namespace (e.g. "default", "tg:12345").
func ContextWithNamespace(ctx context.Context, ns string) context.Context {
	return context.WithValue(ctx, sessionNamespaceKey{}, ns)
}

func NamespaceFromContext(ctx context.Context) string {
	ns, _ := ctx.Value(sessionNamespaceKey{}).(string)
	return ns
}

// ContextWithSessionID stores the current session ID in the context so
// tools called from within a conversation can identify the session they
// are operating on.
func ContextWithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

func SessionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(sessionIDKey{}).(string)
	return id
}

// ContextWithSessionView stores the current session view in the context so
// tool handlers can modify session state (e.g. enable/disable tools)
// without fetching and overwriting the session from the store.
func ContextWithSessionView(ctx context.Context, session any) context.Context {
	return context.WithValue(ctx, sessionViewKey{}, session)
}

// SessionViewFromContext returns the session view stored in the context,
// or nil if not set.
func SessionViewFromContext(ctx context.Context) any {
	s, _ := ctx.Value(sessionViewKey{}).(any)
	return s
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
// Event sender context — lets tools emit engine events into the turn's
// event channel (which the turnTracker fans out to all hub subscribers).
// ---------------------------------------------------------------------------

type engineEventSenderKey struct{}

// ContextWithEventSender stores a sender in the context so tools can
// emit EngineEvents (e.g. SessionCreated) into the parent turn's event
// channel, where the turnTracker picks them up and broadcasts them.
func ContextWithEventSender(ctx context.Context, sender chan<- EngineEvent) context.Context {
	return context.WithValue(ctx, engineEventSenderKey{}, sender)
}

// EventSenderFromContext returns the EngineEvent sender stored in the
// context, or nil.
func EventSenderFromContext(ctx context.Context) chan<- EngineEvent {
	s, _ := ctx.Value(engineEventSenderKey{}).(chan<- EngineEvent)
	return s
}

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

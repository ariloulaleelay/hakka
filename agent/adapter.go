package agent

import (
	"context"
	"time"
)

// CompleteOptions carries per-request knobs. Adapters may ignore fields
// they don't support.
type CompleteOptions struct {
	Temperature *float32
	MaxTokens   *int
	// Extra carries additional body fields to inject into the LLM request.
	// Adapters that support it merge these into the outgoing JSON payload.
	Extra map[string]any
	// SessionID is the current hakka session UUID. Adapters can use it to
	// resolve placeholders like "$session_id" in their Extra configuration.
	SessionID string
}

// Usage represents the token consumption of an LLM generation.
// Duration is the wall-clock time of the LLM call in nanoseconds,
// set by the engine as an informational metric.
// Cost is the monetary cost of the LLM call in USD, extracted from
// the provider's response (usage.cost field) when available.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Duration         time.Duration `json:"duration_ns,omitempty"`
	Cost             float64       `json:"cost,omitempty"`
}

// LLMResponse is the normalized result of a single completion call.
type LLMResponse struct {
	Message      Message
	FinishReason string
	Usage        *Usage
}

// StreamResult is a single event from a streaming LLM response.
// Exactly one of Delta, ToolCalls, Done, or Err is non-zero.
type StreamResult struct {
	// Delta is a text content chunk.
	Delta string

	// ToolCalls contains all tool calls the model made in this round.
	// If non-nil, the stream has ended for this assistant turn and the
	// caller should execute the tools, append results, and start a new
	// stream if it wants to continue.
	ToolCalls []ToolCall

	// Done is true when the stream finished successfully with no more
	// content.
	Done bool

	// Err is set when the stream encountered a terminal error.
	Err error

	// Usage may be set on the Done event or earlier if the provider
	// reports it mid-stream.
	Usage *Usage
}

// LLMAdapter abstracts an LLM provider (OpenAI, Anthropic, Ollama, ...).
// Implementations should be safe for concurrent use.
type LLMAdapter interface {
	Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (*LLMResponse, error)

	// Stream returns a channel of StreamResult events for a single
	// assistant turn. The caller receives text deltas (Delta) and,
	// if the model requests tools, a single event with ToolCalls set.
	// When ToolCalls is non-nil, the caller should execute the tools,
	// append results to the session, and may call Stream again to
	// continue. A final event with Done=true (or Err) terminates the
	// channel.
	//
	// On initialisation failure (bad URL, auth), Stream returns the
	// error immediately. On mid-stream errors, the error is delivered
	// through the channel.
	Stream(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (<-chan StreamResult, error)
}

package agent

import (
	"log/slog"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ErrMaxIterations is returned when the tool loop fails to converge.
var ErrMaxIterations = ErrMaxIterationsSentinel("max tool iterations reached")

// ErrMaxIterationsSentinel is a named type so adapters/tests can
// detect this error without importing the agent package's sentinels.
type ErrMaxIterationsSentinel string

func (e ErrMaxIterationsSentinel) Error() string { return string(e) }

// Hooks provide lifecycle callbacks for observability.
type Hooks struct {
	OnToolCall    func(sessionID string, call ToolCall)
	OnToolResult  func(sessionID string, call ToolCall, result event.ToolResult)
	OnLLMResponse func(sessionID string, resp *LLMResponse)
	OnError       func(sessionID string, err error)
}

func (h Hooks) FireToolCall(sid string, call ToolCall) {
	if h.OnToolCall != nil {
		h.OnToolCall(sid, call)
	}
}

func (h Hooks) FireToolResult(sid string, call ToolCall, result event.ToolResult) {
	if h.OnToolResult != nil {
		h.OnToolResult(sid, call, result)
	}
}

func (h Hooks) FireLLMResponse(sid string, resp *LLMResponse) {
	if h.OnLLMResponse != nil {
		h.OnLLMResponse(sid, resp)
	}
}

func (h Hooks) FireError(sid string, err error) {
	if h.OnError != nil {
		h.OnError(sid, err)
	}
}

// Hacks carries per-provider workarounds for provider-specific quirks.
// Each field should be documented with what it does and why.
type Hacks struct {
	// IgnoreStopIfNoContent, when true, makes the engine automatically
	// re-prompt the LLM when it returns finish_reason="stop" with empty
	// content and no tool calls. Some providers (e.g. DeepSeek) do this
	// after tool results — they "reason" silently and stop without
	// producing visible text. The engine will continue up to 3 rounds.
	IgnoreStopIfNoContent *bool `json:"ignore_stop_if_no_content,omitempty"`
}

// EngineConfig tunes the orchestration loop.
type EngineConfig struct {
	MaxToolIterations int
	CompactSoftLimit  int // default soft limit used when model profile has none; 0 = use DefaultEngineConfig().CompactSoftLimit
	Options           CompleteOptions
	Logger            *slog.Logger
	Hooks             Hooks
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		MaxToolIterations: 512,
		CompactSoftLimit:  200000,
	}
}

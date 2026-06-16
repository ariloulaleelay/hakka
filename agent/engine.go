package agent

import (
	"log/slog"

	"github.com/you/hakka/agent/event"
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

// FireToolCall invokes the OnToolCall callback if set.
func (h Hooks) FireToolCall(sid string, call ToolCall) {
	if h.OnToolCall != nil {
		h.OnToolCall(sid, call)
	}
}

// FireToolResult invokes the OnToolResult callback if set.
func (h Hooks) FireToolResult(sid string, call ToolCall, result event.ToolResult) {
	if h.OnToolResult != nil {
		h.OnToolResult(sid, call, result)
	}
}

// FireLLMResponse invokes the OnLLMResponse callback if set.
func (h Hooks) FireLLMResponse(sid string, resp *LLMResponse) {
	if h.OnLLMResponse != nil {
		h.OnLLMResponse(sid, resp)
	}
}

// FireError invokes the OnError callback if set.
func (h Hooks) FireError(sid string, err error) {
	if h.OnError != nil {
		h.OnError(sid, err)
	}
}

// EngineConfig tunes the orchestration loop.
type EngineConfig struct {
	MaxToolIterations int
	Options           CompleteOptions
	Logger            *slog.Logger
	Hooks             Hooks
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{MaxToolIterations: 128}
}

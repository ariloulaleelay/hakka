package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// MockProvider — deterministic LLMAdapter for testing.
//
// The MockProvider treats the LAST user message as a script that controls
// what the "LLM" returns. This enables end-to-end tests of the wire
// protocol without any real LLM.
//
// Script format (user message content):
//
//	Simple text reply:
//	  {"message": {"content": "Hello world"}}
//
//	Simple tool call:
//	  {"run_tool": {"name": "echo_tool", "args": {"message": "hi"}}}
//
//	Multi-step sequence (tool call then reply):
//	  [
//	    {"run_tool": {"name": "echo_tool", "args": {"message": "hi"}}},
//	    {"message": {"content": "Tool returned: hi"}}
//	  ]
//
// If the user message is not valid script JSON, Complete/Stream return an
// error. If the script runs out of steps during a multi-turn loop, an
// error is returned.
// ---------------------------------------------------------------------------

// ScriptStep is one element of the script array.
type ScriptStep struct {
	Message *struct {
		Content string `json:"content"`
	} `json:"message,omitempty"`
	RunTool *struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	} `json:"run_tool,omitempty"`
}

// MockProvider implements agent.LLMAdapter with deterministic scripted
// responses parsed from user input.
type MockProvider struct {
	mu     sync.Mutex
	indent map[string]int // sessionID → next script index
}

func NewMockProvider() *MockProvider {
	return &MockProvider{
		indent: make(map[string]int),
	}
}

// parseScript parses the user message content as a script.
// Returns the parsed steps (nil for single-step) and whether parsing succeeded.
func parseScript(content string) (steps []ScriptStep, isArray bool, err error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, fmt.Errorf("mock: empty user message")
	}

	// Try parsing as array first.
	var arr []ScriptStep
	if err := json.Unmarshal([]byte(content), &arr); err == nil {
		if len(arr) == 0 {
			return nil, false, fmt.Errorf("mock: empty script array")
		}
		return arr, true, nil
	}

	// Try parsing as single object.
	var step ScriptStep
	if err := json.Unmarshal([]byte(content), &step); err != nil {
		return nil, false, fmt.Errorf("mock: invalid script JSON: %w", err)
	}

	// Validate: at least one field must be set.
	if step.Message == nil && step.RunTool == nil {
		return nil, false, fmt.Errorf("mock: script step must have 'message' or 'run_tool' field")
	}

	return []ScriptStep{step}, false, nil
}

// lastUserMessage extracts the content of the last user message from the
// message list.
func lastUserMessage(msgs []agent.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// getStep returns the current step for the given session, advancing the
// internal index.
func (m *MockProvider) getStep(sessionID string, msgs []agent.Message) (ScriptStep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	content := lastUserMessage(msgs)
	steps, isArray, err := parseScript(content)
	if err != nil {
		return ScriptStep{}, err
	}

	if !isArray {
		return steps[0], nil
	}

	idx := m.indent[sessionID]
	if idx >= len(steps) {
		return ScriptStep{}, fmt.Errorf("mock: script exhausted (index %d >= %d)", idx, len(steps))
	}
	m.indent[sessionID] = idx + 1
	return steps[idx], nil
}

func (m *MockProvider) Complete(ctx context.Context, msgs []agent.Message, _ []agent.ToolSchema, opts agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	step, err := m.getStep(opts.SessionID, msgs)
	if err != nil {
		return nil, err
	}

	if step.Message != nil {
		content := step.Message.Content
		if onDelta != nil {
			// Stream in chunks for realism.
			if len(content) > 3 {
				mid := len(content) / 2
				onDelta(content[:mid])
				onDelta(content[mid:])
			} else if len(content) > 0 {
				onDelta(content)
			}
		}
		return &agent.LLMResponse{
			Message:      agent.Message{Role: agent.RoleAssistant, Content: content},
			FinishReason: "stop",
		}, nil
	}

	if step.RunTool != nil {
		argsJSON, _ := json.Marshal(step.RunTool.Args)
		return &agent.LLMResponse{
			Message: agent.Message{
				Role: agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{{
					ID:        "mock_call_" + step.RunTool.Name,
					Name:      step.RunTool.Name,
					Arguments: string(argsJSON),
				}},
			},
			FinishReason: "tool_calls",
		}, nil
	}

	return nil, fmt.Errorf("mock: invalid step (no message or run_tool)")
}


// Reset clears all per-session indices. Used in test cleanup.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indent = make(map[string]int)
}

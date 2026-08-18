package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// Mock tools for deterministic end-to-end testing.
//
// These tools have predictable behavior that tests can rely on:
//   - echo_tool: returns the input message verbatim
//   - fail_tool: always returns an error
//   - slow_tool: sleeps for a configurable duration, then returns
// ---------------------------------------------------------------------------

// RegisterMockTools registers mock tools with the given registry.
// Tools are tagged with "mock" for easy enable/disable in tests.
func RegisterMockTools(reg *agent.ToolRegistry) {
	reg.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "echo_tool",
			Description: "Echoes back the input message. Returns 'Echo: <message>'.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The message to echo back",
					},
				},
				"required": []string{"message"},
			},
		},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("echo_tool: invalid args: %v", err)
			}
			return "Echo: " + args.Message, nil
		},
		ExecSnippet: func(raw json.RawMessage) string {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return ""
			}
			return "echo '" + args.Message + "'"
		},
		Tags: []string{"mock", "utility"},
	})

	reg.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "fail_tool",
			Description: "Always fails with an error message. Returns 'Error: <message>'.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The error message to return",
					},
				},
				"required": []string{"message"},
			},
		},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("fail_tool: invalid args: %w", err)
			}
			return "", fmt.Errorf("fail_tool: %s", args.Message)
		},
		ExecSnippet: func(raw json.RawMessage) string {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return ""
			}
			return "fail: " + args.Message
		},
		Tags: []string{"mock", "utility"},
	})

	reg.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "slow_tool",
			Description: "Sleeps for a configurable number of seconds, then returns a success message. Use for testing cancellation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"seconds": map[string]any{
						"type":        "number",
						"description": "Number of seconds to sleep (Default 1)",
					},
				},
				"required": []string{},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Seconds int `json:"seconds"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				args.Seconds = 1
			}
			if args.Seconds <= 0 {
				args.Seconds = 1
			}

			select {
			case <-time.After(time.Duration(args.Seconds) * time.Second):
				return fmt.Sprintf("Done sleeping for %d seconds", args.Seconds), nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		ExecSnippet: func(raw json.RawMessage) string {
			var args struct {
				Seconds int `json:"seconds"`
			}
			if err := json.Unmarshal(raw, &args); err != nil || args.Seconds <= 0 {
				return "sleep 1s"
			}
			return fmt.Sprintf("sleep %ds", args.Seconds)
		},
		Tags:    []string{"mock", "utility"},
		Timeout: 10 * time.Second,
	})
}

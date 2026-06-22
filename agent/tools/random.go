package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/ariloulaleelay/hakka/agent"
)

type randomArgs struct {
	MinValue int `json:"min_value"`
	MaxValue int `json:"max_value"`
}

// Random returns a tool that generates a random integer between min_value
// and max_value (inclusive). Returns an error if min_value > max_value.
func Random() agent.Tool {
	return agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "random",
			Description: "Generate a random integer between min_value and max_value (inclusive).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"min_value": map[string]any{"type": "integer", "description": "Minimum value (inclusive)."},
					"max_value": map[string]any{"type": "integer", "description": "Maximum value (inclusive)."},
				},
				"required": []string{"min_value", "max_value"},
			},
		},
		Tags: []string{"utility", "all"},
		ExecSnippet: func(args json.RawMessage) string {
			var params randomArgs
			if err := json.Unmarshal(args, &params); err != nil {
				return ""
			}
			return fmt.Sprintf("%d..%d", params.MinValue, params.MaxValue)
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args randomArgs
			if err := unmarshalToolArgs(raw, "random", &args); err != nil {
				return "", err
			}
			if args.MinValue > args.MaxValue {
				return "", fmt.Errorf("min_value must be <= max_value (got min=%d, max=%d)", args.MinValue, args.MaxValue)
			}
			val := rand.Intn(args.MaxValue-args.MinValue+1) + args.MinValue
			return fmt.Sprintf("%d", val), nil
		},
	}
}

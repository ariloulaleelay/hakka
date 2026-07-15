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
	return NewTool("random", "Generate a random integer between min_value and max_value (inclusive).").
		IntParam("min_value", "Minimum value (inclusive).", true).
		IntParam("max_value", "Maximum value (inclusive).", true).
		Tags("utility", "all").
		ExecSnippet(execSnippetRange()).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args randomArgs
			if err := unmarshalToolArgsStrict(raw, "random",
				"Generate a random integer between min_value and max_value (inclusive).",
				&args, []paramInfo{
					{Name: "min_value", Type: "integer", Description: "Minimum value (inclusive).", Required: true},
					{Name: "max_value", Type: "integer", Description: "Maximum value (inclusive).", Required: true},
				}); err != nil {
				return "", err
			}
			if args.MinValue > args.MaxValue {
				return "", fmt.Errorf("min_value must be <= max_value (got min=%d, max=%d)", args.MinValue, args.MaxValue)
			}
			val := rand.Intn(args.MaxValue-args.MinValue+1) + args.MinValue
			return fmt.Sprintf("%d", val), nil
		}).
		Build()
}

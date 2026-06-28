package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ariloulaleelay/hakka/agent"
)

// Compactify returns the context_compactify meta-tool. It accepts message
// index ranges to compact. The actual compaction is applied on-the-fly by
// BuildCompactContext on the next LLM call — this tool just acknowledges.
//
// The tool is always registered so Execute() can find it, but it only
// appears in schemas when BuildCompactContext signals needCompactify.
func Compactify() agent.Tool {
	return NewTool(agent.ContextCompactifyToolName, "Compress [range_start, range_end] message range using [N] indexes to free context. Provide a summary of what was compacted.").
		IntParam("range_start", "Start index of the message range to compact (inclusive)", true).
		IntParam("range_end", "End index of the message range to compact (inclusive)", true).
		StringParam("summary", "Summary of what the compacted range contained", false).
		Tags("meta").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				RangeStart int    `json:"range_start"`
				RangeEnd   int    `json:"range_end"`
				Summary    string `json:"summary,omitempty"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if args.RangeStart < 0 || args.RangeEnd < args.RangeStart {
				return "", fmt.Errorf("invalid range [%d,%d]: range_start must be >= 0 and <= range_end", args.RangeStart, args.RangeEnd)
			}

			info := fmt.Sprintf("[%d,%d]", args.RangeStart, args.RangeEnd)
			if args.Summary != "" {
				info += " " + args.Summary
			}
			return fmt.Sprintf("Noted: %s. Compaction applied on next context build.", info), nil
		}).
		Build()
}

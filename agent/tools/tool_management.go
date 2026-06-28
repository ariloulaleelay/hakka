package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// show_tool — show detailed tool information (and auto-enable)
// ---------------------------------------------------------------------------

type showToolArgs struct {
	Name string `json:"name"`
}

// ShowTool returns a tool that shows detailed information about a tool
// and automatically enables it so it appears in the API "tools" section.
func ShowTool(r *agent.ToolRegistry) agent.Tool {
	return NewTool("show_tool", "Enable tool by name. Show tool detailed description with parameters.").
		StringParam("name", "Name of the tool to inspect", true).
		Tags("tool", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args showToolArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("show_tool: %w", err)
			}
			if args.Name == "" {
				return "", fmt.Errorf("show_tool: name is required")
			}

			t, ok := r.Get(args.Name)
			if !ok {
				return "", fmt.Errorf("unknown tool: %s", args.Name)
			}

			// Auto-enable the tool in the current session
			session, ok := event.SessionViewFromContext(ctx).(agent.SessionToolEditor)
			if ok && session != nil {
				session.EnableTool(args.Name)
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Tool: %s\n", t.Schema.Name))
			b.WriteString(fmt.Sprintf("Description: %s\n", t.Schema.Description))
			b.WriteString(fmt.Sprintf("Status: enabled\n"))

			if len(t.Tags) > 0 {
				tagStrs := make([]string, len(t.Tags))
				for i, tag := range t.Tags {
					tagStrs[i] = "#" + tag
				}
				b.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(tagStrs, ", ")))
			}

			// Extract parameter info from the schema parameters
			params, _ := t.Schema.Parameters["properties"].(map[string]any)
			requiredList, _ := t.Schema.Parameters["required"].([]any)
			requiredSet := make(map[string]bool, len(requiredList))
			for _, r := range requiredList {
				if s, ok := r.(string); ok {
					requiredSet[s] = true
				}
			}

			if len(params) > 0 {
				b.WriteString("\nParameters:\n")
				// Sort param names for stable output
				names := make([]string, 0, len(params))
				for name := range params {
					names = append(names, name)
				}
				sort.Strings(names)

				// Find longest name for alignment
				maxNameLen := 0
				for _, name := range names {
					if len(name) > maxNameLen {
						maxNameLen = len(name)
					}
				}

				for _, name := range names {
					prop, _ := params[name].(map[string]any)
					typ, _ := prop["type"].(string)
					desc, _ := prop["description"].(string)

					optional := "optional"
					if requiredSet[name] {
						optional = "required"
					}

					line := fmt.Sprintf("  %-*s  (%s, %s)", maxNameLen, name, typ, optional)
					if desc != "" {
						line += " — " + desc
					}
					b.WriteString(line + "\n")
				}
			}

			return b.String(), nil
		}).
		Build()
}

// compactDescription returns a compact version of a tool description by
// taking the first sentence and truncating if needed.
func compactDescription(desc string) string {
	// Take first sentence
	if idx := strings.Index(desc, "."); idx > 0 {
		desc = desc[:idx+1]
	}
	// Truncate at reasonable length
	if len(desc) > 100 {
		desc = desc[:97] + "..."
	}
	return desc
}

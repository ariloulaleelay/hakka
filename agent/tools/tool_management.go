package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// list_tools — list all tools with short descriptions
// ---------------------------------------------------------------------------

type listToolsArgs struct {
	Tag string `json:"tag"`
}

// ListTools returns a tool that lists all registered tools with compact
// descriptions. Optionally filtered by tag.
func ListTools(r *agent.ToolRegistry, sm *agent.SessionManager) agent.Tool {
	return NewTool("list_tools", "List all tools with info snippets. Optionally filter by tag.").
		StringParam("tag", "Filter by tag (optional)", false).
		Tags("tool", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args listToolsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("list_tools: %w", err)
			}

			var schemas []agent.ToolSchema
			if args.Tag != "" {
				schemas = r.SchemasByTags(args.Tag)
			} else {
				schemas = r.Schemas()
			}

			if len(schemas) == 0 {
				if args.Tag != "" {
					return fmt.Sprintf("No tools with tag %q.", args.Tag), nil
				}
				return "No tools available.", nil
			}

			sort.Slice(schemas, func(i, j int) bool {
				return schemas[i].Name < schemas[j].Name
			})

			// Find longest name for alignment
			maxLen := 0
			for _, s := range schemas {
				if len(s.Name) > maxLen {
					maxLen = len(s.Name)
				}
			}

			var b strings.Builder
			for _, s := range schemas {
				snippet := compactDescription(s.Description)
				b.WriteString(fmt.Sprintf("  %-*s  %s\n", maxLen, s.Name, snippet))
			}
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// enable_tool — enable a tool for the current session
// ---------------------------------------------------------------------------

type enableToolArgs struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// EnableTool returns a tool that enables a tool for the current session.
func EnableTool(r *agent.ToolRegistry, sm *agent.SessionManager) agent.Tool {
	return NewTool("enable_tool", "Enable a tool for the current session.").
		StringParam("session_id", "Session ID or unique prefix", true).
		StringParam("name", "Name of the tool to enable", true).
		Tags("tool", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args enableToolArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("enable_tool: %w", err)
			}
			if args.Name == "" {
				return "", fmt.Errorf("enable_tool: name is required")
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("enable_tool: session_id is required")
			}

			// Validate tool exists
			if _, ok := r.Get(args.Name); !ok {
				return "", fmt.Errorf("unknown tool: %s", args.Name)
			}

			ns, err := sessionToolPreamble(ctx, "enable_tool")
			if err != nil {
				return "", err
			}

			session, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "enable_tool")
			if err != nil {
				return "", err
			}

			session.EnableTool(args.Name)
			if err := sm.Save(ctx, ns, session); err != nil {
				return "", fmt.Errorf("enable_tool: %w", err)
			}

			return fmt.Sprintf("Enabled tool: %s", args.Name), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// show_tool — show detailed tool information
// ---------------------------------------------------------------------------

type showToolArgs struct {
	Name string `json:"name"`
}

// ShowTool returns a tool that shows detailed information about a tool.
func ShowTool(r *agent.ToolRegistry) agent.Tool {
	return NewTool("show_tool", "Show detailed information about a specific tool including its parameters.").
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

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Tool: %s\n", t.Schema.Name))
			b.WriteString(fmt.Sprintf("Description: %s\n", t.Schema.Description))

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

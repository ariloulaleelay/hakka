package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/you/hakka/agent"
)

// newToolFromMCP creates an agent.Tool that proxies calls to an MCP server.
// The returned tool's handler forwards the call via the MCPClient and
// returns the plain-text result.
func newToolFromMCP(serverName string, tool mcpTool, client MCPClient) agent.Tool {
	schema := tool.toAgentSchema(serverName)
	prefixed := schema.Name

	return agent.Tool{
		Schema: schema,
		Tags:   []string{"mcp", serverName, "all"},
		ExecSnippet: func(args json.RawMessage) string {
			// Extract a brief summary from the arguments
			var argMap map[string]any
			if err := json.Unmarshal(args, &argMap); err != nil {
				return ""
			}
			// Show first argument value as snippet
			for _, v := range argMap {
				return fmt.Sprintf("%s=%v", prefixed, v)
			}
			return prefixed
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			result, err := client.CallTool(ctx, tool.Name, args)
			if err != nil {
				return "", fmt.Errorf("mcp tool %s: %w", prefixed, err)
			}
			return result, nil
		},
	}
}

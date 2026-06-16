// Package mcp implements a client for the Model Context Protocol (MCP).
// MCP is an open protocol by Anthropic that standardises how applications
// provide context and tools to LLMs. This package allows Hakka to connect
// to MCP servers, discover their tools, and register them dynamically.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 wire types
// ---------------------------------------------------------------------------

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError is a JSON-RPC 2.0 error object.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *jsonrpcError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// ---------------------------------------------------------------------------
// MCP protocol types
// ---------------------------------------------------------------------------

// mcpTool is the tool definition returned by an MCP server via tools/list.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toAgentSchema converts an MCP tool definition into an agent.ToolSchema.
// The tool name is prefixed with "<serverName>_" to avoid collisions
// between tools from different servers.
func (t mcpTool) toAgentSchema(serverName string) agent.ToolSchema {
	prefixed := fmt.Sprintf("%s_%s", serverName, t.Name)

	// Parse the input schema and convert it to a generic map
	var schemaMap map[string]any
	if err := json.Unmarshal(t.InputSchema, &schemaMap); err != nil || schemaMap == nil {
		schemaMap = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	// Sanitize the schema to fix issues like empty type strings ("")
	// that some MCP servers may return (e.g. "type": "").
	// Empty type strings are invalid JSON Schema and cause LLM API
	// errors like "Invalid schema for function 'X': '' is not valid
	// under any of the schemas listed in the 'anyOf' keyword".
	sanitizeSchema(schemaMap)

	return agent.ToolSchema{
		Name:        prefixed,
		Description: t.Description,
		Parameters:  schemaMap,
	}
}

// sanitizeSchema recursively walks a JSON Schema map and fixes common
// issues that can cause LLM API validation errors:
//   - "type": "" is replaced with "type": "string"
//
// Mutates the map in place.
func sanitizeSchema(m map[string]any) {
	// Fix empty type
	if t, ok := m["type"]; ok {
		if s, ok := t.(string); ok && s == "" {
			m["type"] = "string"
		}
	}

	// Recurse into properties
	if props, ok := m["properties"].(map[string]any); ok {
		for _, prop := range props {
			if propMap, ok := prop.(map[string]any); ok {
				sanitizeSchema(propMap)
			}
		}
	}

	// Recurse into items (for array schemas)
	if items, ok := m["items"].(map[string]any); ok {
		sanitizeSchema(items)
	}

	// Recurse into anyOf / allOf / oneOf
	for _, key := range []string{"anyOf", "allOf", "oneOf"} {
		if subs, ok := m[key].([]any); ok {
			for _, sub := range subs {
				if subMap, ok := sub.(map[string]any); ok {
					sanitizeSchema(subMap)
				}
			}
		}
	}
}

// mcpCallResult is the result from a tools/call request.
type mcpCallResult struct {
	Content []mcpContentItem `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// mcpContentItem is a single item in a tool call result.
type mcpContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Other types (image, resource, etc.) are not yet supported.
}

// toPlainText converts an MCP call result to a plain text string suitable
// for the agent tool output convention.
func (r mcpCallResult) toPlainText() string {
	var parts []string
	for _, item := range r.Content {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
		default:
			parts = append(parts, fmt.Sprintf("[MCP content type %q not rendered]", item.Type))
		}
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// MCPClient interface
// ---------------------------------------------------------------------------

// MCPClient is the interface for communicating with an MCP server.
type MCPClient interface {
	// Initialize performs the MCP initialization handshake.
	Initialize(ctx context.Context) error

	// ListTools returns the list of tools advertised by the server.
	ListTools(ctx context.Context) ([]mcpTool, error)

	// CallTool invokes a tool on the server with the given arguments.
	// Returns the plain-text result.
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)

	// Close terminates the connection to the server.
	Close() error
}

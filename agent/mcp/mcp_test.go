package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/you/hakka/agent"
)

// ---------------------------------------------------------------------------
// JSON-RPC message serialization tests
// ---------------------------------------------------------------------------

func TestJSONRPCEncodeDecode(t *testing.T) {
	// Test encoding a request
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var decoded jsonrpcRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded.Method != "tools/list" {
		t.Fatalf("expected method tools/list, got %q", decoded.Method)
	}
	if string(decoded.ID) != "1" {
		t.Fatalf("expected id '1', got %q", string(decoded.ID))
	}

	// Test encoding a response
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Result: json.RawMessage(`{
			"tools": [
				{
					"name": "echo",
					"description": "Echo back input",
					"inputSchema": {
						"type": "object",
						"properties": {
							"message": {"type": "string"}
						},
						"required": ["message"]
					}
				}
			]
		}`),
	}
	raw, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var decodedResp jsonrpcResponse
	if err := json.Unmarshal(raw, &decodedResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if string(decodedResp.ID) != "1" {
		t.Fatalf("expected id '1', got %q", string(decodedResp.ID))
	}

	// Parse the tools list from the result
	var toolsResult struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(decodedResp.Result, &toolsResult); err != nil {
		t.Fatalf("unmarshal tools result: %v", err)
	}
	if len(toolsResult.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolsResult.Tools))
	}
	if toolsResult.Tools[0].Name != "echo" {
		t.Fatalf("expected tool name 'echo', got %q", toolsResult.Tools[0].Name)
	}
}

// Test JSON-RPC error response
func TestJSONRPCErrorResponse(t *testing.T) {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Error: &jsonrpcError{
			Code:    -32601,
			Message: "Method not found",
		},
	}
	raw, _ := json.Marshal(resp)

	var decoded jsonrpcResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("expected error")
	}
	if decoded.Error.Code != -32601 {
		t.Fatalf("expected code -32601, got %d", decoded.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// MCP tool schema → agent.ToolSchema conversion tests
// ---------------------------------------------------------------------------

func TestMCPToolToAgentToolSchema(t *testing.T) {
	mcpTool := mcpTool{
		Name:        "echo",
		Description: "Echo back the input message",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"message": {"type": "string", "description": "The message to echo"}
			},
			"required": ["message"]
		}`),
	}

	schema := mcpTool.toAgentSchema("test")
	if schema.Name != "test_echo" {
		t.Fatalf("expected schema name 'test_echo', got %q", schema.Name)
	}
	if schema.Description != "Echo back the input message" {
		t.Fatalf("expected description %q, got %q", "Echo back the input message", schema.Description)
	}
	params := schema.Parameters
	if params == nil {
		t.Fatal("expected non-nil parameters")
	}
	if params["type"] != "object" {
		t.Fatalf("expected type 'object', got %v", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", params["properties"])
	}
	msgProp, ok := props["message"].(map[string]any)
	if !ok {
		t.Fatalf("expected message property, got %T", props["message"])
	}
	if msgProp["type"] != "string" {
		t.Fatalf("expected message type 'string', got %v", msgProp["type"])
	}
}

func TestSanitizeSchema_FixesEmptyType(t *testing.T) {
	t.Run("empty type becomes string", func(t *testing.T) {
		m := map[string]any{
			"type": "",
		}
		sanitizeSchema(m)
		if m["type"] != "string" {
			t.Fatalf("expected type 'string', got %v", m["type"])
		}
	})

	t.Run("non-empty type unchanged", func(t *testing.T) {
		m := map[string]any{
			"type": "integer",
		}
		sanitizeSchema(m)
		if m["type"] != "integer" {
			t.Fatalf("expected type 'integer', got %v", m["type"])
		}
	})

	t.Run("no type key unchanged", func(t *testing.T) {
		m := map[string]any{
			"description": "hello",
		}
		sanitizeSchema(m)
		if m["description"] != "hello" {
			t.Fatalf("expected description 'hello', got %v", m["description"])
		}
	})

	t.Run("nested properties with empty types", func(t *testing.T) {
		m := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"date": map[string]any{"type": ""},
				"count": map[string]any{"type": "integer"},
			},
		}
		sanitizeSchema(m)
		props := m["properties"].(map[string]any)
		if props["name"].(map[string]any)["type"] != "string" {
			t.Fatal("expected name type 'string', unchanged")
		}
		if props["date"].(map[string]any)["type"] != "string" {
			t.Fatalf("expected date type 'string' (fixed from empty), got %v", props["date"].(map[string]any)["type"])
		}
		if props["count"].(map[string]any)["type"] != "integer" {
			t.Fatal("expected count type 'integer', unchanged")
		}
	})

	t.Run("schema with empty types via toAgentSchema", func(t *testing.T) {
		// This is the exact scenario that caused the bug:
		// MCP server returns "type": "" for some properties.
		mcpTool := mcpTool{
			Name:        "GetPageDetails",
			Description: "Get Page Details",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"slug": {"type": "string", "description": "Page address"},
					"revision_date": {"type": "", "description": "Date for page content"}
				},
				"required": ["slug"]
			}`),
		}
		schema := mcpTool.toAgentSchema("wiki")
		params := schema.Parameters
		props := params["properties"].(map[string]any)
		revDate := props["revision_date"].(map[string]any)
		if revDate["type"] != "string" {
			t.Fatalf("expected revision_date type to be fixed to 'string', got %v", revDate["type"])
		}
		slug := props["slug"].(map[string]any)
		if slug["type"] != "string" {
			t.Fatalf("expected slug type 'string', got %v", slug["type"])
		}
	})
}

// ---------------------------------------------------------------------------
// Config parsing tests
// ---------------------------------------------------------------------------

func TestParseMCPConfig(t *testing.T) {
	jsonStr := `{
		"mcp_servers": {
			"fs": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
			},
			"web": {
				"url": "http://localhost:8080/mcp"
			}
		}
	}`
	var cfg struct {
		Servers map[string]ServerConfig `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.Servers))
	}

	fsServer, ok := cfg.Servers["fs"]
	if !ok {
		t.Fatal("expected 'fs' server")
	}
	if fsServer.Command != "npx" {
		t.Fatalf("expected command 'npx', got %q", fsServer.Command)
	}
	if len(fsServer.Args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(fsServer.Args))
	}

	webServer, ok := cfg.Servers["web"]
	if !ok {
		t.Fatal("expected 'web' server")
	}
	if webServer.URL != "http://localhost:8080/mcp" {
		t.Fatalf("expected URL 'http://localhost:8080/mcp', got %q", webServer.URL)
	}
}

// ---------------------------------------------------------------------------
// Integration test: connect to a fake MCP server over stdio
// ---------------------------------------------------------------------------

// TestStdioTransportWithFakeServer spawns a tiny Go program that acts as
// a minimal MCP server over stdio and verifies the client can discover
// tools and call them.
func TestStdioTransportWithFakeServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration test in short mode")
	}

	// Build a fake MCP server binary
	fakeServer := buildFakeMCPServer(t)

	// Configure the MCP server
	cfg := ServerConfig{
		Command: fakeServer,
		Args:    []string{},
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect to the server
	client, err := NewStdioClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// Initialize the session
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// List tools
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Fatalf("expected first tool 'echo', got %q", tools[0].Name)
	}

	// Call the echo tool
	result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hello world"}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Fatalf("expected result to contain 'hello world', got %q", result)
	}

	// Call the add tool
	result, err = client.CallTool(ctx, "add", json.RawMessage(`{"a": 2, "b": 3}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !strings.Contains(result, "5") {
		t.Fatalf("expected result to contain '5', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Dynamic Agent Tool tests
// ---------------------------------------------------------------------------

func TestDynamicMCPToolWrapper(t *testing.T) {
	// Simulate an MCP client that handles tool calls
	fakeClient := &fakeMCPClient{
		tools: []mcpTool{
			{
				Name:        "echo",
				Description: "Echo back input",
				InputSchema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"message": {"type": "string"}
					},
					"required": ["message"]
				}`),
			},
		},
		callResults: map[string]string{
			"echo": "hello world",
		},
	}

	// Create the dynamic agent tool
	agentTool := newToolFromMCP("fs", fakeClient.tools[0], fakeClient)
	if agentTool.Schema.Name != "fs_echo" {
		t.Fatalf("expected name 'fs_echo', got %q", agentTool.Schema.Name)
	}

	// Register it in a real ToolRegistry
	reg := agent.NewToolRegistry()
	reg.Register(agentTool)

	// Verify schema is correct
	schemas := reg.Schemas()
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
	if schemas[0].Name != "fs_echo" {
		t.Fatalf("expected schema name 'fs_echo', got %q", schemas[0].Name)
	}

	// Execute the tool
	result := reg.Execute(context.Background(), "fs_echo", `{"message":"hello world"}`)
	if result.IsError() {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if !strings.Contains(result.Output, "hello world") {
		t.Fatalf("expected result to contain 'hello world', got %q", result.Output)
	}
}

// ---------------------------------------------------------------------------
// Manager integration test
// ---------------------------------------------------------------------------

func TestManagerRegistersTools(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration test in short mode")
	}

	fakeServer := buildFakeMCPServer(t)

	// Create a manager with one server config
	mgr := NewManager()
	mgr.Add("demo", ServerConfig{
		Command: fakeServer,
		Args:    []string{},
	})

	// Create a tool registry
	reg := agent.NewToolRegistry()

	// Connect and register tools
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mgr.ConnectAll(ctx, reg); err != nil {
		t.Fatalf("connect all: %v", err)
	}
	defer mgr.CloseAll()

	// Check that tools are registered
	schemas := reg.Schemas()
	if len(schemas) < 2 {
		t.Fatalf("expected at least 2 tools registered, got %d", len(schemas))
	}

	// Find our MCP tools
	foundEcho := false
	foundAdd := false
	for _, s := range schemas {
		if s.Name == "demo_echo" {
			foundEcho = true
		}
		if s.Name == "demo_add" {
			foundAdd = true
		}
	}
	if !foundEcho {
		t.Fatal("expected 'demo_echo' tool to be registered")
	}
	if !foundAdd {
		t.Fatal("expected 'demo_add' tool to be registered")
	}

	// Execute an MCP tool through the registry
	result := reg.Execute(ctx, "demo_echo", `{"message":"hello from hakka"}`)
	if result.IsError() {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if !strings.Contains(result.Output, "hello from hakka") {
		t.Fatalf("expected 'hello from hakka' in result, got %q", result.Output)
	}

	// Execute the add tool
	result = reg.Execute(ctx, "demo_add", `{"a":10,"b":20}`)
	if result.IsError() {
		t.Fatalf("expected success, got error: %v", result.Err)
	}
	if !strings.Contains(result.Output, "30") {
		t.Fatalf("expected '30' in result, got %q", result)
	}
}

// TestStdioSequentialCalls verifies that multiple tool calls work in
// sequence without the subprocess being killed between calls. This
// guards against the bug where exec.CommandContext ties the subprocess
// to a short-lived context that gets cancelled after initialization.
func TestStdioSequentialCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration test in short mode")
	}

	fakeServer := buildFakeMCPServer(t)

	cfg := ServerConfig{
		Command: fakeServer,
		Args:    []string{},
	}

	// Use a context with generous timeout for each individual operation,
	// but the subprocess itself must NOT be tied to this context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := NewStdioClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// Initialize
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// List tools
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	// Now make 10 sequential calls to the echo tool. Each call must
	// succeed — if the subprocess died between calls, we'd get "broken
	// pipe" or "connection closed" errors.
	for i := 0; i < 10; i++ {
		msg := fmt.Sprintf("call number %d", i)
		result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"`+msg+`"}`))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !strings.Contains(result, msg) {
			t.Fatalf("call %d: expected result to contain %q, got %q", i, msg, result)
		}
	}

	// Also verify the add tool still works after many calls
	result, err := client.CallTool(ctx, "add", json.RawMessage(`{"a":100,"b":200}`))
	if err != nil {
		t.Fatalf("final add call: %v", err)
	}
	if !strings.Contains(result, "300") {
		t.Fatalf("expected result to contain '300', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fakeMCPClient implements MCPClient for testing.
type fakeMCPClient struct {
	tools       []mcpTool
	callResults map[string]string
}

func (f *fakeMCPClient) Initialize(ctx context.Context) error { return nil }
func (f *fakeMCPClient) ListTools(ctx context.Context) ([]mcpTool, error) {
	return f.tools, nil
}
func (f *fakeMCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, ok := f.callResults[name]
	if !ok {
		return "", &jsonrpcError{Code: -32602, Message: "unknown tool: " + name}
	}
	return result, nil
}
func (f *fakeMCPClient) Close() error { return nil }

// buildFakeMCPServer compiles a tiny Go program that acts as a minimal MCP
// server over stdio, following the MCP protocol (JSON-RPC 2.0).
func buildFakeMCPServer(t *testing.T) string {
	t.Helper()

	src := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type request struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

type response struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	Result  json.RawMessage ` + "`json:\"result,omitempty\"`" + `
	Error   *jsonError      ` + "`json:\"error,omitempty\"`" + `
}

type jsonError struct {
	Code    int    ` + "`json:\"code\"`" + `
	Message string ` + "`json:\"message\"`" + `
}

func respond(id json.RawMessage, result any) {
	raw, _ := json.Marshal(result)
	resp := response{JSONRPC: "2.0", ID: id, Result: raw}
	out, _ := json.Marshal(resp)
	fmt.Println(string(out))
}

func respondError(id json.RawMessage, code int, msg string) {
	resp := response{
		JSONRPC: "2.0", ID: id,
		Error: &jsonError{Code: code, Message: msg},
	}
	out, _ := json.Marshal(resp)
	fmt.Println(string(out))
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	initialized := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			respondError(json.RawMessage("null"), -32700, "Parse error")
			continue
		}

		switch req.Method {
		case "initialize":
			initialized = true
			respond(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "fake-mcp-server",
					"version": "1.0.0",
				},
			})

		case "tools/list":
			if !initialized {
				respondError(req.ID, -32000, "Not initialized")
				continue
			}
			respond(req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "Echo back the input message",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{"type": "string", "description": "The message to echo"},
							},
							"required": []string{"message"},
						},
					},
					{
						"name":        "add",
						"description": "Add two numbers together",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"a": map[string]any{"type": "number", "description": "First number"},
								"b": map[string]any{"type": "number", "description": "Second number"},
							},
							"required": []string{"a", "b"},
						},
					},
				},
			})

		case "tools/call":
			if !initialized {
				respondError(req.ID, -32000, "Not initialized")
				continue
			}
			var params struct {
				Name string          ` + "`json:\"name\"`" + `
				Args json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(req.Params, &params)

			switch params.Name {
			case "echo":
				var args struct {
					Message string ` + "`json:\"message\"`" + `
				}
				json.Unmarshal(params.Args, &args)
				result := fmt.Sprintf("Echo: %%s", args.Message)
				respond(req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": result},
					},
				})
			case "add":
				var args struct {
					A float64 ` + "`json:\"a\"`" + `
					B float64 ` + "`json:\"b\"`" + `
				}
				json.Unmarshal(params.Args, &args)
				sum := args.A + args.B
				result := fmt.Sprintf("%%d", int(sum))
				respond(req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": result},
					},
				})
			default:
				respondError(req.ID, -32602, "Unknown tool: "+params.Name)
			}

		default:
			respondError(req.ID, -32601, "Method not found: "+req.Method)
		}
	}
}
`
	// Write the source to a temp file and compile it
	dir := t.TempDir()
	srcPath := dir + "/fakemcp.go"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	binary := dir + "/fakemcp"
	cmd := exec.Command("go", "build", "-o", binary, srcPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compile fake server: %v", err)
	}
	return binary
}

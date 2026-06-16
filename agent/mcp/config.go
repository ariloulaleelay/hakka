package mcp

// ServerConfig describes how to connect to a single MCP server.
type ServerConfig struct {
	// Command is the executable to run for a stdio-based MCP server.
	// If set, the server is launched as a subprocess and communicates
	// over stdin/stdout.
	Command string `json:"command,omitempty"`

	// Args is the list of arguments to pass to the command.
	Args []string `json:"args,omitempty"`

	// Env is optional environment variables to set for the subprocess.
	// If nil, the parent process's environment is inherited.
	Env map[string]string `json:"env,omitempty"`

	// URL is the HTTP/SSE endpoint for an HTTP-based MCP server.
	// If set (and Command is empty), the client connects via SSE.
	URL string `json:"url,omitempty"`

	// Headers are optional HTTP headers for the SSE connection.
	Headers map[string]string `json:"headers,omitempty"`
}

// Config holds all MCP server configurations.
type Config struct {
	Servers map[string]ServerConfig `json:"mcp_servers"`
}

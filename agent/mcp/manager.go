package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ariloulaleelay/hakka/agent"
)

// Manager manages the lifecycle of multiple MCP servers and their tool
// registrations.
type Manager struct {
	mu      sync.Mutex
	servers map[string]ServerConfig
	clients map[string]MCPClient
	Logger  *slog.Logger
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{
		servers: make(map[string]ServerConfig),
		clients: make(map[string]MCPClient),
	}
}

// Add registers a server configuration. The server will be connected
// when ConnectAll is called.
func (m *Manager) Add(name string, cfg ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[name] = cfg
}

// ConnectAll connects to all configured MCP servers and registers their
// tools on the given ToolRegistry.
func (m *Manager) ConnectAll(ctx context.Context, reg *agent.ToolRegistry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := m.logger()

	for name, cfg := range m.servers {
		if _, exists := m.clients[name]; exists {
			log.Warn("mcp: server already connected", "server", name)
			continue
		}

		client, err := m.connectServer(ctx, cfg)
		if err != nil {
			return fmt.Errorf("mcp: connect %q: %w", name, err)
		}

		// Initialize the session
		if err := client.Initialize(ctx); err != nil {
			client.Close()
			return fmt.Errorf("mcp: initialize %q: %w", name, err)
		}

		// Discover tools
		tools, err := client.ListTools(ctx)
		if err != nil {
			client.Close()
			return fmt.Errorf("mcp: list tools %q: %w", name, err)
		}

		log.Info("mcp: connected and discovered tools",
			"server", name,
			"tool_count", len(tools))

		// Register each tool
		for _, toolDef := range tools {
			agentTool := newToolFromMCP(name, toolDef, client)
			reg.Register(agentTool)
			log.Debug("mcp: registered tool", "server", name, "tool", agentTool.Schema.Name)
		}

		m.clients[name] = client
	}

	return nil
}

// connectServer creates a client for the given server configuration.
func (m *Manager) connectServer(ctx context.Context, cfg ServerConfig) (MCPClient, error) {
	if cfg.Command != "" {
		return NewStdioClient(ctx, cfg)
	}
	if cfg.URL != "" {
		return nil, fmt.Errorf("SSE/HTTP transport not yet implemented")
	}
	return nil, fmt.Errorf("no command or url specified for MCP server")
}

// CloseAll disconnects all MCP servers.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := m.logger()

	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			log.Error("mcp: close error", "server", name, "error", err)
		} else {
			log.Debug("mcp: closed", "server", name)
		}
		delete(m.clients, name)
	}
}

func (m *Manager) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

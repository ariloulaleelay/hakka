package tools

import "github.com/you/hakka/agent"

// RegisterTelegramTools registers a restricted subset of tools safe for
// external (Telegram) users. Only tools with no filesystem or shell access
// are included. This prevents Telegram users from reading/writing files,
// executing shell commands, or accessing the user's Neovim instance.
func RegisterTelegramTools(r *agent.ToolRegistry) {
	// Only http_get is considered safe for external users — it makes
	// outbound HTTP requests but does not touch the local filesystem
	// or execute arbitrary commands.
	r.Register(HTTPGet())
}

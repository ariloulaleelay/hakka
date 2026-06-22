package tools

import "github.com/ariloulaleelay/hakka/agent"

// RegisterTelegramTools registers a restricted subset of tools safe for
// external (Telegram) users. Only tools with no filesystem or shell access
// are included. This prevents Telegram users from reading/writing files,
// executing shell commands, or accessing the user's Neovim instance.
func RegisterTelegramTools(r *agent.ToolRegistry) {
	// http_get makes outbound HTTP requests but does not touch the local
	// filesystem or execute arbitrary commands.
	r.Register(HTTPGet())
	// random is a pure computation tool with no side effects.
	r.Register(Random())
}

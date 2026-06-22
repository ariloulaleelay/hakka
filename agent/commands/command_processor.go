package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// CommandResult describes the outcome of processing a slash command.
type CommandResult struct {
	Handled bool
	Action  CommandAction
	Reply   string
	Session *agent.Session
	Error   error
}

// CommandAction describes what effect the command had on the session.
type CommandAction int

const (
	ActionNone          CommandAction = iota
	ActionReply                       // command produced a text reply
	ActionClearSession                // active session was deleted
	ActionSessionSwitch               // session was switched to a different one
	ActionSessionCreate               // a new session was created
)

// CommandProcessor handles slash-commands (/help, /model, /session, /tool, ...).
// It is a thin dispatcher that delegates to domain-specific handlers.
// It has no dependency on the LLM or tools — just session storage and a model router.
//
// Namespace is resolved with the following precedence:
//  1. From context (via event.NamespaceFromContext) — used by gateways that
//     serve multiple isolated namespaces (e.g. Telegram per-chat)
//  2. The configured Namespace field (set at construction time)
//
// Gateways serving multiple namespaces should set the namespace in the
// context rather than mutating the field, ensuring thread safety.
type CommandProcessor struct {
	Sessions     *agent.SessionManager
	Conv         *agent.Conversation // used for model binding; can be nil
	SystemPrompt string
	Namespace    string
	Tools        *agent.ToolRegistry // gateway's tool registry; used by /tool commands

	modelHandler   *ModelCommands
	sessionHandler *SessionCommands
	toolHandler    *ToolCommands
}

// New builds a CommandProcessor and its sub-handlers.
func New(sm *agent.SessionManager, conv *agent.Conversation, systemPrompt, namespace string) *CommandProcessor {
	cp := &CommandProcessor{
		Sessions:     sm,
		Conv:         conv,
		SystemPrompt: systemPrompt,
		Namespace:    namespace,
	}
	cp.modelHandler = NewModelCommands(sm, conv, namespace)
	cp.sessionHandler = NewSessionCommands(sm, conv, namespace)
	return cp
}

// SetTools attaches a tool registry, enabling /tool commands.
func (cp *CommandProcessor) SetTools(tools *agent.ToolRegistry) {
	cp.Tools = tools
	cp.toolHandler = NewToolCommands(cp.Sessions, tools, cp.Namespace)
}

// inheritCWD copies the ClientCWD from an existing session (if any) to a
// newly created session, so the new session doesn't default to the server's
// working directory. It is shared by handleStart and handleSessionCreate.
func inheritCWD(ctx context.Context, sm *agent.SessionManager, ns, prevSessionID string, session *agent.Session) {
	if prevSessionID == "" {
		return
	}
	prev, _, err := sm.Store.Get(ctx, ns, prevSessionID)
	if err != nil || prev == nil || prev.ClientCWD == "" {
		return
	}
	session.ClientCWD = prev.ClientCWD
}

// Execute attempts to interpret input as a slash-command. If it is one,
// the command is executed and a CommandResult is returned. If the input
// is not a command, Handled is false.
func (cp *CommandProcessor) Execute(ctx context.Context, sessionID, input string) CommandResult {
	// Promote the configured namespace into the context so that all
	// sub-handlers (SessionCommands, ToolCommands, ModelCommands) find
	// it via resolveNamespace(ctx) — context is the single source of
	// truth for namespace. Only promote if the context doesn't already
	// have a namespace (e.g. Telegram sets per-chat namespace earlier).
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, cp.Namespace)
	}
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return CommandResult{Handled: false}
	}
	parts := strings.Fields(trimmed)
	cmd := parts[0]
	// Strip @bot_username suffix so /help@my_bot is treated as /help.
	if idx := strings.Index(cmd, "@"); idx >= 0 {
		cmd = cmd[:idx]
	}

	// Try sub-handlers first
	if res := cp.trySubHandlers(ctx, sessionID, parts, cmd); res.Handled {
		return res
	}

	// Known built-in commands that don't belong to sub-handlers
	switch cmd {
	case "/help":
		return cp.handleHelp(ctx, sessionID, parts)
	case "/start":
		return cp.handleStart(ctx, sessionID, parts)
	default:
		return CommandResult{
			Handled: true,
			Action:  ActionReply,
			Reply:   fmt.Sprintf("unknown command: %s. Type /help for available commands.", cmd),
		}
	}
}

func (cp *CommandProcessor) trySubHandlers(ctx context.Context, sessionID string, parts []string, cmd string) CommandResult {
	if cp.modelHandler != nil {
		if cmd == "/model" || cmd == "/models" {
			return cp.modelHandler.Handle(ctx, sessionID, parts, cmd)
		}
	}
	if cp.sessionHandler != nil && cmd == "/session" {
		return cp.sessionHandler.Handle(ctx, sessionID, parts)
	}
	if cp.toolHandler != nil && cmd == "/tool" {
		return cp.toolHandler.Handle(ctx, sessionID, parts)
	}
	return CommandResult{Handled: false}
}

func (cp *CommandProcessor) handleHelp(_ context.Context, _ string, _ []string) CommandResult {
	helpText := `available commands:
  /help                         - Show this help menu
  /start                        - Start a fresh session with all tools enabled
  /model list                   - List available models
  /model show                   - Show current model
  /model switch <name>          - Switch to a different model
  /session list                 - List all sessions
  /session create               - Start a new session
  /session switch <id>          - Switch to an existing session
  /session delete <id>          - Delete a session (use "this" for current)
  /session info                 - Show current session details
  /session rename <name>        - Rename current session
  /session autorename           - Auto-generate a session name using LLM
  /tool list                    - List available tools with status
  /tool enable <name-or-#tag>...   - Enable a tool or all tools with a #tag
  /tool disable <name-or-#tag>...  - Disable a tool or all tools with a #tag`
	return CommandResult{Handled: true, Action: ActionReply, Reply: helpText}
}

// handleStart creates a fresh session and enables all registered tools.
// The new session inherits the ClientCWD from the current session (if any).
func (cp *CommandProcessor) handleStart(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := cp.Sessions.GetOrCreate(ctx, ns, "")
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	inheritCWD(ctx, cp.Sessions, ns, sessionID, session)

	// Enable all tools registered in the tool registry.
	if cp.Tools != nil {
		for _, schema := range cp.Tools.Schemas() {
			session.EnableTool(schema.Name)
		}
	}

	if err := cp.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	return CommandResult{
		Handled: true,
		Action:  ActionSessionCreate,
		Reply:   "started fresh session: " + session.ID + " with all tools enabled",
		Session: session,
	}
}

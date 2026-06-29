package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// CommandResult describes the outcome of processing a command.
type CommandResult struct {
	Handled bool
	Action  CommandAction
	Data    json.RawMessage // structured data for JSON-capable clients
	Cmd     string          // command name that produced this result
	Session *agent.Session
	Error   error
	Reply   string // fallback text (kept for backward compat, not used by JSON clients)
}

// CommandAction describes what effect the command had on the session.
type CommandAction int

const (
	ActionNone          CommandAction = iota
	ActionClearSession                // active session was deleted
	ActionGetSession                  // session was fetched and made active
	ActionSessionCreate               // a new session was created
	ActionContinue                    // trigger LLM without adding a user message
)

// CommandProcessor handles structured JSON commands (/help, /model, /session, /tool, ...).
// It delegates to domain-specific handlers. No text-based slash command parsing
// happens here — clients are responsible for mapping slash-commands to JSON.
type CommandProcessor struct {
	Sessions     *agent.SessionManager
	Conv         *agent.Conversation
	SystemPrompt string
	Namespace    string
	Tools        *agent.ToolRegistry

	modelHandler   *ModelCommands
	sessionHandler *SessionCommands
	toolHandler    *ToolCommands
}

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

func (cp *CommandProcessor) SetTools(tools *agent.ToolRegistry) {
	cp.Tools = tools
	cp.toolHandler = NewToolCommands(cp.Sessions, tools, cp.Namespace)
}

func inheritCWD(ctx context.Context, sm *agent.SessionManager, ns, prevSessionID string, session *agent.Session) {
	if prevSessionID == "" {
		return
	}
	prev, _, err := sm.Store.Get(ctx, ns, prevSessionID)
	if err != nil || prev == nil || prev.Read().ClientCWD == "" {
		return
	}
	session.SetClientCWD(prev.Read().ClientCWD)
}

// ExecuteJSON handles a structured JSON command from a JSON-capable
// client (web frontend). Returns structured data in CommandResult.Data.
func (cp *CommandProcessor) ExecuteJSON(ctx context.Context, sessionID, cmd string, params json.RawMessage) CommandResult {
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, cp.Namespace)
	}

	switch cmd {
	case "help":
		return cp.execHelp(ctx, sessionID)
	case "continue":
		return CommandResult{Handled: true, Action: ActionContinue, Cmd: cmd}
	case "cwd_set":
		return cp.execCWDSet(ctx, sessionID, params)
	case "start":
		return cp.execStart(ctx, sessionID)
	case "compact":
		return cp.execCompact(ctx, sessionID, params)

	// Session commands
	case "session_list":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "session_create":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "get_session":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "session_delete":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "session_info":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "session_rename":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "session_autorename":
		if cp.sessionHandler != nil {
			return cp.sessionHandler.HandleJSON(ctx, sessionID, cmd, params)
		}

	// Model commands
	case "model_list":
		if cp.modelHandler != nil {
			return cp.modelHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "model_switch":
		if cp.modelHandler != nil {
			return cp.modelHandler.HandleJSON(ctx, sessionID, cmd, params)
		}

	// Tool commands
	case "tool_list":
		if cp.toolHandler != nil {
			return cp.toolHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "tool_allow":
		if cp.toolHandler != nil {
			return cp.toolHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	case "tool_deny":
		if cp.toolHandler != nil {
			return cp.toolHandler.HandleJSON(ctx, sessionID, cmd, params)
		}
	}

	return CommandResult{
		Handled: true,
		Cmd:     cmd,
		Reply:   fmt.Sprintf("unknown command: %s", cmd),
	}
}

// --- JSON handlers ---

func (cp *CommandProcessor) execHelp(ctx context.Context, sessionID string) CommandResult {
	type cmdEntry struct {
		Cmd    string `json:"cmd"`
		Desc   string `json:"desc"`
		Params any    `json:"params,omitempty"`
	}
	helpJSON, _ := json.Marshal(map[string]any{
		"commands": []cmdEntry{
			{Cmd: "help", Desc: "Show this help menu"},
			{Cmd: "continue", Desc: "Continue the conversation (LLM responds without new input)"},
			{Cmd: "start", Desc: "Start a fresh session with all tools enabled"},
			{Cmd: "cwd_set", Desc: "Set working directory for the session", Params: map[string]string{"cwd": "/path/to/dir"}},
			{Cmd: "compact", Desc: "Set context soft limit in tokens", Params: map[string]string{"n": "int (0=off)"}},
			{Cmd: "session_list", Desc: "List all sessions"},
			{Cmd: "session_create", Desc: "Create a new session"},
			{Cmd: "get_session", Desc: "Fetch session data by ID", Params: map[string]string{"id": "session ID or prefix"}},
			{Cmd: "session_delete", Desc: "Delete a session", Params: map[string]string{"id": "session ID or 'this'"}},
			{Cmd: "session_info", Desc: "Show current session details"},
			{Cmd: "session_rename", Desc: "Rename current session", Params: map[string]string{"name": "new name"}},
			{Cmd: "session_autorename", Desc: "Auto-generate name using LLM"},
			{Cmd: "model_list", Desc: "List available models"},
			{Cmd: "model_switch", Desc: "Switch to a different model", Params: map[string]string{"name": "model name"}},
			{Cmd: "tool_list", Desc: "List available tools with status"},
			{Cmd: "tool_allow", Desc: "Allow (and enable) a tool or tag", Params: map[string]string{"name": "tool name or #tag"}},
			{Cmd: "tool_deny", Desc: "Deny (hide) a tool or tag", Params: map[string]string{"name": "tool name or #tag"}},
		},
	})
	return CommandResult{Handled: true, Cmd: "help", Data: helpJSON}
}

func (cp *CommandProcessor) execStart(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := cp.Sessions.GetOrCreate(ctx, ns, "")
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	inheritCWD(ctx, cp.Sessions, ns, sessionID, session)
	if cp.Tools != nil {
		for _, schema := range cp.Tools.Schemas() {
			session.EnableTool(schema.Name)
		}
	}
	if err := cp.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	data, _ := json.Marshal(map[string]any{
		"session": sessionToMap(session),
	})
	return CommandResult{
		Handled: true,
		Action:  ActionSessionCreate,
		Cmd:     "start",
		Data:    data,
		Session: session,
	}
}

func (cp *CommandProcessor) execCWDSet(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	var p struct {
		CWD string `json:"cwd"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.CWD == "" {
		return CommandResult{Handled: true, Cmd: "cwd_set", Reply: "error: please specify a path in 'cwd' field"}
	}
	session, err := cp.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "cwd_set", Error: err}
	}
	session.SetClientCWD(p.CWD)
	if err := cp.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "cwd_set", Error: err}
	}
	data, _ := json.Marshal(map[string]any{"cwd": p.CWD, "session_id": session.SessionID()})
	return CommandResult{Handled: true, Cmd: "cwd_set", Data: data, Session: session}
}

func (cp *CommandProcessor) execCompact(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		N int `json:"n"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}

	session, err := cp.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	if params == nil || len(params) == 0 || string(params) == "null" || string(params) == "{}" {
		// Show current value
		data, _ := json.Marshal(map[string]any{
			"compact_soft_limit": session.GetCompactSoftLimit(),
		})
		return CommandResult{Handled: true, Cmd: "compact", Data: data}
	}

	session.SetCompactSoftLimit(p.N)
	if err := cp.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"compact_soft_limit": p.N,
	})
	return CommandResult{Handled: true, Cmd: "compact", Data: data}
}

// sessionToMap helper.
func sessionToMap(s *agent.Session) map[string]any {
	if s == nil {
		return nil
	}
	d := s.Read()
	return map[string]any{
		"id":            d.ID,
		"name":          d.Name,
		"short_id":      shortID(d.ID),
		"message_count": len(d.Messages),
		"model":         s.GetModel(),
		"total_tokens":              s.TotalTokenUsage(),
		"estimated_context_tokens": s.GetEstimatedContextTokens(),
		"client_cwd":    d.ClientCWD,
		"created_at":    d.CreatedAt.Format(time.RFC3339),
		"updated_at":    formatTime(d.UpdatedAt, d.CreatedAt),
	}
}

func formatTime(t, fallback time.Time) string {
	if t.IsZero() {
		return fallback.Format(time.RFC3339)
	}
	return t.Format(time.RFC3339)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

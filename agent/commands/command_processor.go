package commands

import (
	"context"
	"encoding/json"
	"fmt"

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
	// InFlight indicates that the session has an active turn running.
	// Used by get_session to decide whether to append a "done" event
	// to the events replay.
	InFlight bool
	// SessionEvent, when non-nil, describes a session lifecycle event
	// that should be broadcast to all clients in the namespace.
	SessionEvent *SessionEvent
}

// CommandAction describes what effect the command had on the session.
type CommandAction int

const (
	ActionNone          CommandAction = iota
	ActionClearSession                // active session was deleted
	ActionGetSession                  // session was fetched and made active
	ActionSessionCreate               // a new session was created
	ActionSessionFork                 // a session was forked from a parent
	ActionContinue                    // trigger LLM without adding a user message
)

// ---------------------------------------------------------------------------
// Session event — broadcast to all clients in namespace
// ---------------------------------------------------------------------------

// SessionEventType identifies the kind of lifecycle event.
type SessionEventType string

const (
	SessionCreated SessionEventType = "session_create"
	SessionForked  SessionEventType = "session_fork"
	SessionRenamed SessionEventType = "renamed"
	SessionDeleted SessionEventType = "session_delete"
)

// SessionEvent describes a session lifecycle event to be broadcast.
type SessionEvent struct {
	Type      SessionEventType
	SessionID string
	Session   *agent.Session // for SessionCreated / SessionUpdated
	OldName   string         // for SessionRenamed
	NewName   string         // for SessionRenamed
}

// CommandProcessor handles structured JSON commands (/help, /model, /session,
// /tool, ...). No text-based slash command parsing happens here — clients
// are responsible for mapping slash-commands to JSON.
func sessionForCommand(sm *agent.SessionManager, ctx context.Context, namespace, id string) (*agent.Session, error) {
	return sm.Get(ctx, namespace, id)
}

type CommandProcessor struct {
	Sessions     *agent.SessionManager
	Conv         *agent.Conversation
	SystemPrompt string
	Namespace    string
	Tools        *agent.ToolRegistry

	registry       *CommandRegistry
	sessionHandler *SessionCommands // kept for SetSessionActiveChecker wiring
}

// SessionActiveChecker returns true if the given session ID has an
// active (in-flight) turn running. This is wired from the TurnTracker
// in the gateway layer so that session_list can report the in_flight flag.
type SessionActiveChecker func(sessionID string) bool

func New(sm *agent.SessionManager, conv *agent.Conversation, systemPrompt, namespace string) *CommandProcessor {
	reg := NewCommandRegistry()
	cp := &CommandProcessor{
		Sessions:     sm,
		Conv:         conv,
		SystemPrompt: systemPrompt,
		Namespace:    namespace,
		registry:     reg,
	}

	// Register top-level commands owned by CommandProcessor itself.
	reg.Register(Command{Name: "help", Description: "Show this help menu", Handler: cp.execHelp})
	reg.Register(Command{Name: "continue", Description: "Continue the conversation (LLM responds without new input)",
		Handler: func(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
			return CommandResult{Action: ActionContinue}
		},
	})
	reg.Register(Command{
		Name: "start", Description: "Start a fresh session with all tools enabled",
		Handler: cp.execStart,
	})
	reg.Register(Command{
		Name: "cwd_set", Description: "Set working directory for the session",
		Params:  map[string]string{"cwd": "/path/to/dir"},
		Handler: cp.execCWDSet,
	})
	reg.Register(Command{
		Name: "compact", Description: "Set context soft limit in tokens",
		Params:  map[string]string{"n": "int (0=off)"},
		Handler: cp.execCompact,
	})

	// Delegate model and session commands to sub-handlers.
	cp.sessionHandler = NewSessionCommands(sm, conv, namespace, reg)
	NewModelCommands(sm, conv, namespace, reg)

	return cp
}

func (cp *CommandProcessor) SetTools(tools *agent.ToolRegistry) {
	cp.Tools = tools
	NewToolCommands(cp.Sessions, tools, cp.Namespace, cp.registry)
}

// SetSessionActiveChecker sets the active checker on the session handler,
// allowing session_list to report whether each session has an in-flight turn.
func (cp *CommandProcessor) SetSessionActiveChecker(fn SessionActiveChecker) {
	if cp.sessionHandler != nil {
		cp.sessionHandler.ActiveChecker = fn
	}
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

	if result, ok := cp.registry.Execute(ctx, sessionID, cmd, params); ok {
		return result
	}

	return CommandResult{
		Handled: true,
		Cmd:     cmd,
		Reply:   fmt.Sprintf("unknown command: %s", cmd),
	}
}

// --- Help generation ---

func (cp *CommandProcessor) execHelp(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	type cmdEntry struct {
		Cmd     string `json:"cmd"`
		Desc    string `json:"desc"`
		Params  any    `json:"params,omitempty"`
		Display string `json:"display,omitempty"`
	}
	all := cp.registry.List()
	entries := make([]cmdEntry, 0, len(all))
	for _, c := range all {
		entry := cmdEntry{Cmd: c.Name, Desc: c.Description}
		if c.Display != "" {
			entry.Display = c.Display
		}
		if len(c.Params) > 0 {
			entry.Params = c.Params
		}
		entries = append(entries, entry)
	}
	helpJSON, _ := json.Marshal(map[string]any{
		"commands": entries,
	})
	return CommandResult{Handled: true, Cmd: "help", Data: helpJSON}
}

func (cp *CommandProcessor) execStart(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := cp.Sessions.Create(ctx, ns)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if cp.Conv != nil {
		cp.Conv.EnsureDefaultModel(ctx, session)
	}
	inheritCWD(ctx, cp.Sessions, ns, sessionID, session)
	if cp.Tools != nil {
		for _, schema := range cp.Tools.Schemas() {
			session.EnableTool(schema.Name)
		}
	}
	cwd := session.Read().ClientCWD
	model := session.GetModel()
	limit := session.GetCompactSoftLimit()
	enabled := session.Read().EnabledTools
	if err := cp.Sessions.Store.PatchMeta(ctx, ns, session.SessionID(), &agent.SessionMetaPatch{
		ClientCWD:        &cwd,
		Model:            &model,
		CompactSoftLimit: &limit,
		EnabledTools:     enabled,
	}); err != nil {
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
		SessionEvent: &SessionEvent{
			Type:      SessionCreated,
			SessionID: session.SessionID(),
			Session:   session,
		},
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
	session, err := sessionForCommand(cp.Sessions, ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "cwd_set", Error: err}
	}
	session.SetClientCWD(p.CWD)
	if err := cp.Sessions.Store.PatchMeta(ctx, ns, session.SessionID(), &agent.SessionMetaPatch{
		ClientCWD: &p.CWD,
	}); err != nil {
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

	session, err := sessionForCommand(cp.Sessions, ctx, ns, sessionID)
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
	if err := cp.Sessions.Store.PatchMeta(ctx, ns, session.SessionID(), &agent.SessionMetaPatch{
		CompactSoftLimit: &p.N,
	}); err != nil {
		return CommandResult{Handled: true, Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"compact_soft_limit": p.N,
	})
	return CommandResult{Handled: true, Cmd: "compact", Data: data}
}

// sessionToMap returns the canonical session metadata via agent.Session.Metadata().
// This is the single point of truth for session metadata across all commands.
func sessionToMap(s *agent.Session) map[string]any {
	if s == nil {
		return nil
	}
	return s.Metadata()
}

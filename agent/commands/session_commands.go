package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

const charsPerToken = 4

func estimateTokenCount(history []agent.Message) int {
	count := 0
	for _, m := range history {
		count += len(m.Content) / charsPerToken
	}
	return count
}

func shortestUniquePrefixes(sessions []*agent.Session) map[string]string {
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.SessionID()
	}
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		result[id] = shortestUniquePrefix(id, ids)
	}
	return result
}

func shortestUniquePrefix(id string, allIDs []string) string {
	for i := 1; i <= len(id); i++ {
		prefix := id[:i]
		matches := 0
		for _, other := range allIDs {
			if strings.HasPrefix(other, prefix) {
				matches++
				if matches > 1 {
					break
				}
			}
		}
		if matches == 1 {
			return prefix
		}
	}
	return id
}

type SessionCommands struct {
	Sessions      *agent.SessionManager
	Conv          *agent.Conversation
	NS            string
	ActiveChecker SessionActiveChecker
}

func NewSessionCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string, reg *CommandRegistry) *SessionCommands {
	sc := &SessionCommands{Sessions: sm, Conv: conv, NS: ns}

	reg.Register(Command{
		Name: "session_list", Description: "List all sessions", Display: "session list",
		Handler: sc.jsonSessionList,
	})
	reg.Register(Command{
		Name: "session_create", Description: "Create a new session", Display: "session create",
		Handler: sc.jsonSessionCreate,
	})
	reg.Register(Command{
		Name: "get_session", Description: "Fetch session data by ID", Display: "session get",
		Params:  map[string]string{"id": "session ID or prefix"},
		Handler: sc.jsonGetSession,
	})
	reg.Register(Command{
		Name: "session_delete", Description: "Delete a session", Display: "session delete",
		Params:  map[string]string{"id": "session ID or 'this'"},
		Handler: sc.jsonSessionDelete,
	})
	reg.Register(Command{
		Name: "session_info", Description: "Show current session details", Display: "session info",
		Handler: sc.jsonSessionInfo,
	})
	reg.Register(Command{
		Name: "session_rename", Description: "Rename current session", Display: "session rename",
		Params:  map[string]string{"name": "new name"},
		Handler: sc.jsonSessionRename,
	})
	reg.Register(Command{
		Name: "session_autorename", Description: "Auto-generate name using LLM", Display: "session autorename",
		Handler: sc.jsonSessionAutoRename,
	})
	reg.Register(Command{
		Name: "session_fork", Description: "Fork a session from a parent", Display: "session fork",
		Params: map[string]string{
			"id":         "parent session ID or prefix",
			"fork_point": "message ID to fork at (inclusive; omit for blank child)",
		},
		Handler: sc.jsonSessionFork,
	})

	return sc
}

func (sc *SessionCommands) jsonSessionList(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	sessions, err := sc.Sessions.List(ctx, ns)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_list", Error: err}
	}

	// Visible sessions (skip empty, except current)
	var visible []*agent.Session
	for _, s := range sessions {
		if len(s.Messages()) == 0 && s.SessionID() != sessionID {
			continue
		}
		visible = append(visible, s)
	}

	shortIDs := shortestUniquePrefixes(sessions)
	list := make([]map[string]any, 0, len(visible))
	for _, s := range visible {
		inFlight := false
		if sc.ActiveChecker != nil {
			inFlight = sc.ActiveChecker(s.SessionID())
		}
		entry := map[string]any{
			"id":            s.SessionID(),
			"short_id":      shortIDs[s.SessionID()],
			"name":          s.SessionName(),
			"in_flight":     inFlight,
			"created":       s.Read().CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at":    s.Read().UpdatedAt.Format("2006-01-02T15:04:05Z"),
			"message_count": len(s.Messages()),
			"current":       s.SessionID() == sessionID,
			"client_cwd":    s.Read().ClientCWD,
		}
		list = append(list, entry)
	}

	data, _ := json.Marshal(map[string]any{"sessions": list})
	return CommandResult{Handled: true, Cmd: "session_list", Data: data}
}

func (sc *SessionCommands) jsonSessionCreate(ctx context.Context, prevSessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, "")
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_create", Error: err}
	}
	if sc.Conv != nil {
		sc.Conv.EnsureDefaultModel(ctx, session)
	}
	inheritCWD(ctx, sc.Sessions, ns, prevSessionID, session)

	// Persist the inherited CWD — GetOrCreate saves the session
	// with os.Getwd() before inheritCWD runs, and without an explicit
	// save the store retains the wrong CWD.
	if err := sc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "session_create", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"session": session.Metadata(),
	})
	return CommandResult{
		Handled: true,
		Action:  ActionSessionCreate,
		Cmd:     "session_create",
		Data:    data,
		Session: session,
		SessionEvent: &SessionEvent{
			Type:      SessionCreated,
			SessionID: session.SessionID(),
			Session:   session,
		},
	}
}

func (sc *SessionCommands) jsonGetSession(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		ID string `json:"id"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.ID == "" {
		return CommandResult{Handled: true, Cmd: "get_session", Reply: "error: please specify a session ID"}
	}

	target, err := sc.Sessions.ResolveSessionID(ctx, ns, p.ID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "get_session", Reply: err.Error()}
	}

	session, ok, err := sc.Sessions.Get(ctx, ns, target)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "get_session", Error: err}
	}
	if !ok {
		return CommandResult{Handled: true, Cmd: "get_session", Reply: fmt.Sprintf("session not found: %s", p.ID)}
	}
	if err != nil {
		return CommandResult{Handled: true, Cmd: "get_session", Error: err}
	}

	// Ensure the session has a default model set. This handles both
	// new sessions created before the model was set at creation time
	// and sessions restored from DB before the model persistence fix.
	if sc.Conv != nil {
		sc.Conv.EnsureDefaultModel(ctx, session)
		sc.Sessions.Save(ctx, ns, session)
	}

	data, _ := json.Marshal(map[string]any{
		"session":  session.Metadata(),
		"messages": session.Messages(),
	})
	return CommandResult{
		Handled: true,
		Action:  ActionGetSession,
		Cmd:     "get_session",
		Data:    data,
		Session: session,
	}
}

func (sc *SessionCommands) jsonSessionDelete(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		ID string `json:"id"`
	}

	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.ID == "" {
		return CommandResult{Handled: true, Cmd: "session_delete", Reply: "error: please specify a session ID or 'this'"}
	}

	var target string
	var err error
	if p.ID == "this" {
		target = sessionID
	} else {
		target, err = sc.Sessions.ResolveSessionID(ctx, ns, p.ID)
		if err != nil {
			return CommandResult{Handled: true, Cmd: "session_delete", Reply: err.Error()}
		}
	}

	_, _, err = sc.Sessions.Get(ctx, ns, target)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_delete", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"deleted": target,
	})

	// Track whether the deleted session is the currently active one.
	wasCurrent := target == sessionID

	// Delete the session.
	if err := sc.Sessions.Drop(ctx, ns, target); err != nil {
		return CommandResult{Handled: true, Cmd: "session_delete", Error: err}
	}

	result := CommandResult{
		Handled: true,
		Action:  ActionClearSession,
		Cmd:     "session_delete",
		Data:    data,
		SessionEvent: &SessionEvent{
			Type:      SessionDeleted,
			SessionID: target,
		},
	}
	_ = wasCurrent
	return result
}

func (sc *SessionCommands) jsonSessionInfo(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_info", Error: err}
	}
	data, _ := json.Marshal(map[string]any{
		"session": session.Metadata(),
	})
	return CommandResult{Handled: true, Cmd: "session_info", Data: data, Session: session}
}

func (sc *SessionCommands) jsonSessionRename(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		Name string `json:"name"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Name == "" {
		return CommandResult{Handled: true, Cmd: "session_rename", Reply: "error: name must not be empty"}
	}

	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_rename", Error: err}
	}
	oldName := session.SessionName()
	session.SetSessionName(p.Name)
	if err := sc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "session_rename", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"id":   session.SessionID(),
		"name": p.Name,
	})
	return CommandResult{
		Handled: true,
		Cmd:     "session_rename",
		Data:    data,
		Session: session,
		SessionEvent: &SessionEvent{
			Type:      SessionRenamed,
			SessionID: session.SessionID(),
			OldName:   oldName,
			NewName:   p.Name,
		},
	}
}

func (sc *SessionCommands) jsonSessionAutoRename(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Error: err}
	}
	if sc.Conv == nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Reply: "auto-rename not available"}
	}

	oldName := session.SessionName()
	newName, err := sc.Conv.AutoRename(ctx, session)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"id":   session.SessionID(),
		"name": newName,
	})
	return CommandResult{
		Handled: true,
		Cmd:     "session_autorename",
		Data:    data,
		Session: session,
		SessionEvent: &SessionEvent{
			Type:      SessionRenamed,
			SessionID: session.SessionID(),
			OldName:   oldName,
			NewName:   newName,
		},
	}
}

// jsonSessionFork creates a child session by forking a parent at a given
// message. The child inherits parent settings (model, CWD, tools, skills,
// compact limit) and copies messages up to fork_point (inclusive).
func (sc *SessionCommands) jsonSessionFork(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		ID        string `json:"id"`
		ForkPoint string `json:"fork_point"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.ID == "" {
		return CommandResult{Handled: true, Cmd: "session_fork", Reply: "error: parent session ID is required"}
	}

	// Resolve parent.
	targetID, err := sc.Sessions.ResolveSessionID(ctx, ns, p.ID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_fork", Reply: err.Error()}
	}

	parent, ok, err := sc.Sessions.Get(ctx, ns, targetID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_fork", Error: err}
	}
	if !ok {
		return CommandResult{Handled: true, Cmd: "session_fork", Reply: fmt.Sprintf("parent session not found: %s", p.ID)}
	}

	parentData := parent.Read()

	// Fork.
	childData, err := parentData.ForkData(p.ForkPoint)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_fork", Reply: fmt.Sprintf("fork failed: %s", err.Error())}
	}

	// Assign new identity.
	childData.ID = agent.MakeUniqueID()
	childData.Namespace = ns
	childData.Name = "" // child gets its own name (or auto-rename later)

	child := agent.NewSessionFromData(&childData)

	// Persist.
	if err := sc.Sessions.Store.Put(ctx, ns, child); err != nil {
		return CommandResult{Handled: true, Cmd: "session_fork", Error: err}
	}

	// Ensure default model.
	if sc.Conv != nil {
		sc.Conv.EnsureDefaultModel(ctx, child)
	}

	data, _ := json.Marshal(map[string]any{
		"session": child.Metadata(),
	})
	return CommandResult{
		Handled: true,
		Action:  ActionSessionFork,
		Cmd:     "session_fork",
		Data:    data,
		Session: child,
		SessionEvent: &SessionEvent{
			Type:      SessionForked,
			SessionID: child.SessionID(),
			Session:   child,
		},
	}
}

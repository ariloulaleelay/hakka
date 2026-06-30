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

// SessionActiveChecker returns true if the given session ID has an
// active (in-flight) turn running. This is wired from the TurnTracker
// in the gateway layer so that session_list can report the in_flight flag.
type SessionActiveChecker func(sessionID string) bool

// SessionCommands handles session-related commands.
type SessionCommands struct {
	Sessions           *agent.SessionManager
	Conv               *agent.Conversation
	NS                 string
	ActiveChecker      SessionActiveChecker
}

func NewSessionCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string) *SessionCommands {
	return &SessionCommands{Sessions: sm, Conv: conv, NS: ns}
}

// HandleJSON handles a structured JSON session command.
func (sc *SessionCommands) HandleJSON(ctx context.Context, sessionID, cmd string, params json.RawMessage) CommandResult {
	switch cmd {
	case "session_list":
		return sc.jsonSessionList(ctx, sessionID)
	case "session_create":
		return sc.jsonSessionCreate(ctx, sessionID)
	case "get_session":
		return sc.jsonGetSession(ctx, sessionID, params)
	case "session_delete":
		return sc.jsonSessionDelete(ctx, sessionID, params)
	case "session_info":
		return sc.jsonSessionInfo(ctx, sessionID)
	case "session_rename":
		return sc.jsonSessionRename(ctx, sessionID, params)
	case "session_autorename":
		return sc.jsonSessionAutoRename(ctx, sessionID)
	}
	return CommandResult{Handled: false}
}

func (sc *SessionCommands) jsonSessionList(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	sessions, err := sc.Sessions.List(ctx, ns)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_list", Error: err}
	}

	// Visible sessions (skip empty, except current)
	var visible []*agent.Session
	for _, s := range sessions {
		if len(s.AllMessages()) == 0 && s.SessionID() != sessionID {
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
			"message_count": len(s.AllMessages()),
			"current":       s.SessionID() == sessionID,
			"client_cwd":    s.Read().ClientCWD,
		}
		list = append(list, entry)
	}

	data, _ := json.Marshal(map[string]any{"sessions": list})
	return CommandResult{Handled: true, Cmd: "session_list", Data: data}
}

func (sc *SessionCommands) jsonSessionCreate(ctx context.Context, prevSessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, "")
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_create", Error: err}
	}
	if sc.Conv != nil {
		sc.Conv.EnsureDefaultModel(ctx, session)
	}
	inheritCWD(ctx, sc.Sessions, ns, prevSessionID, session)

	data, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"id":         session.SessionID(),
			"short_id":   shortID(session.SessionID()),
			"name":       session.SessionName(),
			"client_cwd": session.Read().ClientCWD,
		},
	})
	return CommandResult{
		Handled: true,
		Action:  ActionSessionCreate,
		Cmd:     "session_create",
		Data:    data,
		Session: session,
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
		"session":  sessionToMap(session),
		"messages": session.AllMessages(),
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
		return CommandResult{Handled: true, Cmd: "session_delete", Reply: "error: please specify a session ID"}
	}

	if p.ID == "this" {
		if sessionID == "" {
			return CommandResult{Handled: true, Cmd: "session_delete", Reply: "error: no active session to delete"}
		}
		if err := sc.Sessions.Drop(ctx, ns, sessionID); err != nil {
			return CommandResult{Handled: true, Cmd: "session_delete", Error: err}
		}
		data, _ := json.Marshal(map[string]any{"deleted": sessionID, "active_cleared": true})
		return CommandResult{Handled: true, Action: ActionClearSession, Cmd: "session_delete", Data: data}
	}

	target, err := sc.Sessions.ResolveSessionID(ctx, ns, p.ID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_delete", Reply: err.Error()}
	}

	if err := sc.Sessions.Drop(ctx, ns, target); err != nil {
		return CommandResult{Handled: true, Cmd: "session_delete", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"deleted":        target,
		"active_cleared": target == sessionID,
	})
	if target == sessionID {
		return CommandResult{Handled: true, Action: ActionClearSession, Cmd: "session_delete", Data: data}
	}
	return CommandResult{Handled: true, Cmd: "session_delete", Data: data}
}

func (sc *SessionCommands) jsonSessionInfo(ctx context.Context, sessionID string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_info", Error: err}
	}
	if sc.Conv != nil {
		sc.Conv.EnsureDefaultModel(ctx, session)
		sc.Sessions.Save(ctx, ns, session)
	}

	data, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"id":                session.SessionID(),
			"name":              session.DisplayName(),
			"model":             sc.modelName(session),
			"message_count":     len(session.AllMessages()),
			"total_tokens":              session.TotalTokenUsage(),
			"total_cost":                session.TotalCost(),
			"estimated_context_tokens": session.GetEstimatedContextTokens(),
			"compact_soft_limit": session.GetCompactSoftLimit(),
			"estimated_context": estimateTokenCount(session.History()),
		},
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
	p.Name = strings.Trim(p.Name, `"'`)
	if p.Name == "" {
		return CommandResult{Handled: true, Cmd: "session_rename", Reply: "error: name cannot be empty"}
	}

	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_rename", Error: err}
	}
	session.SetSessionName(p.Name)
	if err := sc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Cmd: "session_rename", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"session": sessionToMap(session),
	})
	return CommandResult{Handled: true, Cmd: "session_rename", Data: data, Session: session}
}

func (sc *SessionCommands) jsonSessionAutoRename(ctx context.Context, sessionID string) CommandResult {
	if sc.Conv == nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Reply: "error: no conversation engine available"}
	}
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Error: err}
	}

	newName, renameErr := sc.Conv.AutoRename(ctx, session)
	if renameErr != nil {
		return CommandResult{Handled: true, Cmd: "session_autorename", Reply: "session auto-rename failed: " + renameErr.Error(), Session: session}
	}

	data, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"id":       session.SessionID(),
			"name":     newName,
			"short_id": shortID(session.SessionID()),
		},
	})
	return CommandResult{Handled: true, Cmd: "session_autorename", Data: data, Session: session}
}

func (sc *SessionCommands) modelName(session *agent.Session) string {
	if sc.Conv != nil {
		return sc.Conv.SessionModel(session)
	}
	return session.GetModel()
}

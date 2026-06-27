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
		ids[i] = s.ID
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

// SessionCommands handles session-related slash commands.
type SessionCommands struct {
	Sessions *agent.SessionManager
	Conv     *agent.Conversation
	NS       string
}

func NewSessionCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string) *SessionCommands {
	return &SessionCommands{Sessions: sm, Conv: conv, NS: ns}
}

// HandleJSON handles a structured JSON session command.
// cmd is e.g. "session_list", "session_create", etc.
func (sc *SessionCommands) HandleJSON(ctx context.Context, sessionID, cmd string, params json.RawMessage) CommandResult {
	switch cmd {
	case "session_list":
		return sc.jsonSessionList(ctx, sessionID)
	case "session_create":
		return sc.jsonSessionCreate(ctx, sessionID)
	case "session_switch":
		return sc.jsonSessionSwitch(ctx, sessionID, params)
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
		if len(s.Messages) == 0 && s.ID != sessionID {
			continue
		}
		visible = append(visible, s)
	}

	shortIDs := shortestUniquePrefixes(sessions)
	list := make([]map[string]any, 0, len(visible))
	for _, s := range visible {
		entry := map[string]any{
			"id":            s.ID,
			"short_id":      shortIDs[s.ID],
			"name":          s.Name,
			"created":       s.CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at":    s.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			"message_count": len(s.Messages),
			"current":       s.ID == sessionID,
			"client_cwd":    s.ClientCWD,
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
	inheritCWD(ctx, sc.Sessions, ns, prevSessionID, session)

	data, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"id":         session.ID,
			"short_id":   shortID(session.ID),
			"name":       session.Name,
			"client_cwd": session.ClientCWD,
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

func (sc *SessionCommands) jsonSessionSwitch(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	ns := event.NamespaceFromContext(ctx)

	var p struct {
		ID string `json:"id"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.ID == "" {
		return CommandResult{Handled: true, Cmd: "session_switch", Reply: "error: please specify a session ID"}
	}

	target, err := sc.Sessions.ResolveSessionID(ctx, ns, p.ID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_switch", Reply: err.Error()}
	}

	session, err := sc.Sessions.GetOrCreate(ctx, ns, target)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "session_switch", Error: err}
	}

	data, _ := json.Marshal(map[string]any{
		"session":  sessionToMap(session),
		"messages": session.AllMessages(),
	})
	return CommandResult{
		Handled: true,
		Action:  ActionSessionSwitch,
		Cmd:     "session_switch",
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
		"deleted":       target,
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

	data, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"id":                session.ID,
			"name":              session.DisplayName(),
			"model":             sc.modelName(session),
			"message_count":     len(session.Messages),
			"total_tokens":      session.TotalTokenUsage(),
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
	session.Name = p.Name
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
			"id":       session.ID,
			"name":     newName,
			"short_id": shortID(session.ID),
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

// --- Text-based handlers (keep for non-JSON clients) ---

func (sc *SessionCommands) Handle(ctx context.Context, sessionID string, parts []string) CommandResult {
	if len(parts) < 2 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "session usage:\n  /session create\n  /session list\n  /session delete <id>\n  /session delete this\n  /session switch <id>\n  /session info\n  /session rename <name>\n  /session autorename"}
	}

	handlers := map[string]func(context.Context, string, []string) CommandResult{
		"info":       sc.handleSessionInfo,
		"list":       sc.handleSessionList,
		"delete":     sc.handleSessionDelete,
		"switch":     sc.handleSessionSwitch,
		"create":     sc.handleSessionCreate,
		"rename":     sc.handleSessionRename,
		"autorename": sc.handleSessionAutoRename,
	}

	handler, exists := handlers[parts[1]]
	if !exists {
		return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("unknown session subcommand %q", parts[1])}
	}
	return handler(ctx, sessionID, parts)
}

func (sc *SessionCommands) handleSessionInfo(ctx context.Context, sessionID string, _ []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	msgs := len(session.Messages)
	totalTokens := session.TotalTokenUsage()
	contextTokens := estimateTokenCount(session.History())
	displayName := session.DisplayName()
	reply := fmt.Sprintf("Session ID: %s\nName: %s\nModel: %s\nMessages: %d\nCompact soft limit: %d tokens\nEst. Context Tokens: ~%d\nTotal Lifetime Tokens: %d",
		session.ID, displayName, sc.modelName(session), msgs, session.GetCompactSoftLimit(), contextTokens, totalTokens)
	return CommandResult{Handled: true, Action: ActionReply, Reply: reply, Session: session}
}

func (sc *SessionCommands) handleSessionList(ctx context.Context, sessionID string, _ []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	sessions, err := sc.Sessions.List(ctx, ns)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if len(sessions) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no sessions found"}
	}

	var visible []*agent.Session
	for _, session := range sessions {
		if len(session.Messages) == 0 && session.ID != sessionID {
			continue
		}
		visible = append(visible, session)
	}
	if len(visible) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no sessions found"}
	}

	shortIDs := shortestUniquePrefixes(sessions)

	lines := []string{"available sessions:"}
	for _, session := range visible {
		mark := "  "
		if session.ID == sessionID {
			mark = "* "
		}
		short := shortIDs[session.ID]
		created := session.CreatedAt.Format("2006-01-02 15:04")
		if session.Name != "" {
			lines = append(lines, fmt.Sprintf("%s%s  %s [%s] <%s>", mark, created, session.Name, short, session.ID))
		} else {
			lines = append(lines, fmt.Sprintf("%s%s  [%s] %s", mark, created, short, session.ID))
		}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: strings.Join(lines, "\n")}
}

func (sc *SessionCommands) handleSessionDelete(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a session ID to delete"}
	}
	if parts[2] == "this" {
		if sessionID == "" {
			return CommandResult{Handled: true, Action: ActionReply, Reply: "error: no active session to delete"}
		}
		if err := sc.Sessions.Drop(ctx, ns, sessionID); err != nil {
			return CommandResult{Handled: true, Error: err}
		}
		return CommandResult{Handled: true, Action: ActionClearSession, Reply: fmt.Sprintf("session %s deleted (active session cleared)", sessionID)}
	}
	target, err := sc.Sessions.ResolveSessionID(ctx, ns, parts[2])
	if err != nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: err.Error()}
	}
	if err := sc.Sessions.Drop(ctx, ns, target); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if target == sessionID {
		return CommandResult{Handled: true, Action: ActionClearSession, Reply: fmt.Sprintf("session %s deleted (active session cleared)", target)}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: fmt.Sprintf("session %s deleted", target)}
}

func (sc *SessionCommands) handleSessionSwitch(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a session ID to switch to"}
	}
	target, err := sc.Sessions.ResolveSessionID(ctx, ns, parts[2])
	if err != nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: err.Error()}
	}
	session, err := sc.Sessions.GetOrCreate(ctx, ns, target)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionSessionSwitch, Reply: "switched to session: " + target, Session: session}
}

func (sc *SessionCommands) handleSessionCreate(ctx context.Context, sessionID string, _ []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, "")
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	inheritCWD(ctx, sc.Sessions, ns, sessionID, session)
	return CommandResult{Handled: true, Action: ActionSessionCreate, Reply: "created and switched to session: " + session.ID, Session: session}
}

func (sc *SessionCommands) handleSessionRename(ctx context.Context, sessionID string, parts []string) CommandResult {
	ns := event.NamespaceFromContext(ctx)
	if len(parts) < 3 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a name for the session"}
	}
	name := strings.Join(parts[2:], " ")
	name = strings.Trim(name, `"'`)
	if name == "" {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: name cannot be empty"}
	}
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	session.Name = name
	if err := sc.Sessions.Save(ctx, ns, session); err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "session renamed to: " + name, Session: session}
}

func (sc *SessionCommands) handleSessionAutoRename(ctx context.Context, sessionID string, _ []string) CommandResult {
	if sc.Conv == nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "error: no conversation engine available"}
	}
	ns := event.NamespaceFromContext(ctx)
	session, err := sc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	name, renameErr := sc.Conv.AutoRename(ctx, session)
	if renameErr != nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "session auto-rename failed: " + renameErr.Error(), Session: session}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "session renamed to: " + name, Session: session}
}

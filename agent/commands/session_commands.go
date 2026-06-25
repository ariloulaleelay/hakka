package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// charsPerToken is a rough heuristic for estimating token count from
// plain text. A typical ratio is ~4 characters per token for English text.
const charsPerToken = 4

func estimateTokenCount(history []agent.Message) int {
	count := 0
	for _, m := range history {
		count += len(m.Content) / charsPerToken
	}
	return count
}

// ---------------------------------------------------------------------------
// Short ID helpers — compute and resolve shortest unique prefixes for session IDs.
// ---------------------------------------------------------------------------

// shortestUniquePrefixes computes the shortest prefix that uniquely identifies
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

// shortestUniquePrefix returns the shortest prefix of `id` that does not
// match any other string in `allIDs`.
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
	return id // fallback — full ID (rare with UUIDs)
}

// ---------------------------------------------------------------------------
// SessionCommands handles /session slash commands.
// ---------------------------------------------------------------------------

// SessionCommands handles session-related slash commands.
// Namespace is resolved from context at each operation, so the same
// handler can serve multiple isolated namespaces concurrently.
type SessionCommands struct {
	Sessions *agent.SessionManager
	Conv     *agent.Conversation // used for model binding; can be nil
	NS       string              // fallback namespace (used when context has none)
}

// NewSessionCommands builds a SessionCommands handler.
func NewSessionCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string) *SessionCommands {
	return &SessionCommands{Sessions: sm, Conv: conv, NS: ns}
}

// Handle dispatches to the appropriate sub-handler.
// Parts[0] is "/session", Parts[1] is the subcommand.
func (sc *SessionCommands) Handle(ctx context.Context, sessionID string, parts []string) CommandResult {
	if len(parts) < 2 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "session usage:\n  /session create\n  /session list\n  /session delete <id>\n  /session delete this\n  /session switch <id>\n  /session info\n  /session rename <name>\n  /session autorename"}
	}

	handlers := map[string]func(context.Context, string, []string) CommandResult{
		"info":   sc.handleSessionInfo,
		"list":   sc.handleSessionList,
		"delete": sc.handleSessionDelete,
		"switch": sc.handleSessionSwitch,
		"create": sc.handleSessionCreate,
		"rename": sc.handleSessionRename,
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

func (sc *SessionCommands) modelName(session *agent.Session) string {
	if sc.Conv != nil {
		return sc.Conv.SessionModel(session)
	}
	return session.GetModel()
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

	// Build display list — visible sessions
	var visible []*agent.Session
	for _, session := range sessions {
		// Hide empty sessions (no messages) from the list, except the
		// currently active session — this prevents cluttering the list
		// with sessions created by every :HakkaChat invocation.
		if len(session.Messages) == 0 && session.ID != sessionID {
			continue
		}
		visible = append(visible, session)
	}
	if len(visible) == 0 {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no sessions found"}
	}

	// Compute shortest unique prefixes over ALL sessions (including hidden)
	// so that typed prefixes always resolve unambiguously.
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
	// Support /session delete this — delete the current session.
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

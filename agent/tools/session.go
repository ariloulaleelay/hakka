package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// Tool argument types
// ---------------------------------------------------------------------------

type sessionListArgs struct {
	MaxResults int `json:"max_results"`
}

type sessionIdentifyArgs struct {
	SessionID string `json:"session_id"`
}

type sessionRenameArgs struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

type sessionReadArgs struct {
	SessionID   string `json:"session_id"`
	MaxMessages int    `json:"max_messages"`
	Role        string `json:"role"`
}

type sessionSearchArgs struct {
	Pattern    string `json:"pattern"`
	MaxResults int    `json:"max_results"`
}

type sessionSummarizeArgs struct {
	SessionID string `json:"session_id"`
	MaxTokens int    `json:"max_tokens"`
}

type sessionAskQuestionArgs struct {
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
}

// ---------------------------------------------------------------------------
// Shared preamble helpers for session tools.
//
// Every session tool handler starts with the same two steps:
//  1. Extract the namespace from context.
//  2. (Most) Resolve a session_id prefix to a full *Session.
//
// These helpers eliminate the ~80 lines of duplicated boilerplate across
// the seven handler functions below.
// ---------------------------------------------------------------------------

// namespaceFromContext returns the namespace from context, or an error if
// not set.
func namespaceFromContext(ctx context.Context) (string, error) {
	ns := event.NamespaceFromContext(ctx)
	if ns == "" {
		return "", fmt.Errorf("session namespace not available in context")
	}
	return ns, nil
}

// sessionToolPreamble extracts the namespace from context for a session
// tool. The toolName is used in error messages (e.g. "session_list").
func sessionToolPreamble(ctx context.Context, toolName string) (string, error) {
	ns, err := namespaceFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}
	return ns, nil
}

// resolveSessionFromTool resolves a session ID prefix to a full *Session.
// Used by session tools that accept a session_id parameter.
func resolveSessionFromTool(ctx context.Context, sm *agent.SessionManager, ns, prefix, toolName string) (*agent.Session, error) {
	sessionID, err := sm.ResolveSessionID(ctx, ns, prefix)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", toolName, err)
	}
	s, ok, err := sm.Store.Get(ctx, ns, sessionID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", toolName, err)
	}
	if !ok {
		return nil, fmt.Errorf("%s: session %q not found", toolName, prefix)
	}
	return s, nil
}

// unmarshalSessionArgs combines sessionToolPreamble + json.Unmarshal into
// one call. It extracts the namespace from context and unmarshals raw JSON
// into the provided args pointer. On failure it returns a formatted error.
func unmarshalSessionArgs(ctx context.Context, raw json.RawMessage, toolName string, args any) (string, error) {
	ns, err := sessionToolPreamble(ctx, toolName)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, args); err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}
	return ns, nil
}

// ---------------------------------------------------------------------------
// session_list — list all sessions in the current namespace
// ---------------------------------------------------------------------------

// SessionList returns a tool that lists all sessions with metadata.
func SessionList(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_list", "List all sessions with ID, name, message count, creation time, and model.").
		IntParam("max_results", "Maximum number of sessions to return (optional, default 50)", false).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionListArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_list", &args)
			if err != nil {
				return "", err
			}
			if args.MaxResults <= 0 {
				args.MaxResults = 50
			}

			sessions, err := sm.List(ctx, ns)
			if err != nil {
				return "", fmt.Errorf("session_list: %w", err)
			}

			if len(sessions) == 0 {
				return "No sessions found in this namespace.", nil
			}

			limit := args.MaxResults
			if limit > len(sessions) {
				limit = len(sessions)
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("%d session(s):\n", len(sessions)))
			for i, s := range sessions[:limit] {
				if i > 0 {
					b.WriteString("  ---\n")
				}
				msgCount := len(s.Messages())
				created := s.Read().CreatedAt.Format("2006-01-02 15:04")
				model := s.GetModel()
				if model == "" {
					model = "(default)"
				}
				if s.SessionName() != "" {
					b.WriteString(fmt.Sprintf("  %d. %s [%s] <%s> — %d msgs, model: %s\n",
						i+1, s.SessionName(), s.SessionID()[:8], created, msgCount, model))
				} else {
					b.WriteString(fmt.Sprintf("  %d. %s <%s> — %d msgs, model: %s\n",
						i+1, s.SessionID()[:8], created, msgCount, model))
				}
				// Show first user message
				for _, m := range s.Messages() {
					if m.Role == agent.RoleUser {
						summary := agent.Truncate(m.Content, 100)
						b.WriteString(fmt.Sprintf("     → %s\n", summary))
						break
					}
				}
			}
			if limit < len(sessions) {
				b.WriteString(fmt.Sprintf("  ... and %d more (use max_results to see more)\n", len(sessions)-limit))
			}
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_rename — rename a session
// ---------------------------------------------------------------------------

// SessionRename returns a tool that renames a session by ID or prefix.
func SessionRename(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_rename", "Rename a session. Specify session_id (full or unique prefix) and new name.").
		StringParam("session_id", "Session ID or unique prefix", true).
		StringParam("name", "New name for the session", true).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionRenameArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_rename", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}
			if args.Name == "" {
				return "", fmt.Errorf("name is required")
			}

			s, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "session_rename")
			if err != nil {
				return "", err
			}

			s.SetSessionName(args.Name)
			if err := sm.Store.Put(ctx, ns, s); err != nil {
				return "", fmt.Errorf("session_rename: %w", err)
			}

			return fmt.Sprintf("Session %s renamed to %q.", s.SessionID()[:8], args.Name), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_info — detailed session info
// ---------------------------------------------------------------------------

// SessionInfo returns a tool that shows detailed info about a session.
func SessionInfo(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_info", "Show detailed information about a session (messages, tokens, model, created time).").
		StringParam("session_id", "Session ID or unique prefix", true).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionIdentifyArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_info", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}

			s, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "session_info")
			if err != nil {
				return "", err
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Session ID:   %s\n", s.SessionID()))
			if s.SessionName() != "" {
				b.WriteString(fmt.Sprintf("Name:         %s\n", s.SessionName()))
			}
			b.WriteString(fmt.Sprintf("Namespace:    %s\n", s.Read().Namespace))
			b.WriteString(fmt.Sprintf("Created:      %s\n", s.Read().CreatedAt.Format("2006-01-02 15:04:05")))
			b.WriteString(fmt.Sprintf("Model:        %s\n", s.GetModel()))
			b.WriteString(fmt.Sprintf("Messages:     %d\n", len(s.Messages())))
			b.WriteString(fmt.Sprintf("Total Tokens: %d\n", s.TotalTokenUsage()))
			b.WriteString(fmt.Sprintf("Compact soft limit: %d tokens\n", s.GetCompactSoftLimit()))
			b.WriteString(fmt.Sprintf("Client CWD:   %s\n", s.Read().ClientCWD))

			if len(s.Messages()) > 0 {
				b.WriteString("\nMessages (first 10):\n")
				limit := 10
				if len(s.Messages()) < limit {
					limit = len(s.Messages())
				}
				for i, m := range s.Messages()[:limit] {
					content := agent.Truncate(m.Content, 200)
					b.WriteString(fmt.Sprintf("  [%d] %s: %s\n", i+1, m.Role, content))
					if len(m.ToolCalls) > 0 {
						for _, tc := range m.ToolCalls {
							b.WriteString(fmt.Sprintf("       → tool: %s\n", tc.Name))
						}
					}
				}
				if limit < len(s.Messages()) {
					b.WriteString(fmt.Sprintf("  ... (%d more messages)\n", len(s.Messages())-limit))
				}
			}
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_read — read messages from a session
// ---------------------------------------------------------------------------

// SessionRead returns a tool that reads messages from a session.
func SessionRead(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_read", "Read session. Use only when need exact session content, otherwise use session_ask_question.").
		StringParam("session_id", "Session ID or unique prefix", true).
		IntParam("max_messages", "Maximum number of recent messages to return (optional, default 100)", false).
		StringParam("role", "Filter by role: 'user', 'assistant', 'tool', 'system' (optional)", false).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionReadArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_read", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}
			if args.MaxMessages <= 0 {
				args.MaxMessages = 100
			}

			s, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "session_read")
			if err != nil {
				return "", err
			}

			messages := s.Messages()

			if args.Role != "" {
				filtered := make([]agent.Message, 0, len(messages))
				for _, m := range messages {
					if string(m.Role) == args.Role {
						filtered = append(filtered, m)
					}
				}
				messages = filtered
			}

			if len(messages) == 0 {
				return "No messages found.", nil
			}

			start := 0
			if len(messages) > args.MaxMessages {
				start = len(messages) - args.MaxMessages
			}
			display := messages[start:]

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Session %s — %d messages", s.SessionID()[:8], len(messages)))
			if start > 0 {
				b.WriteString(fmt.Sprintf(" (showing last %d, %d omitted)", len(display), start))
			}
			b.WriteString(":\n\n")

			for i, m := range display {
				label := fmt.Sprintf("[%d] %s", start+i+1, m.Role)
				if m.Name != "" {
					label += " (" + m.Name + ")"
				}
				b.WriteString(fmt.Sprintf("### %s\n%s\n", label, m.Content))
				if len(m.ToolCalls) > 0 {
					for _, tc := range m.ToolCalls {
						b.WriteString(fmt.Sprintf("  → tool_call %s: %s(%s)\n", tc.ID, tc.Name, tc.Arguments))
					}
				}
				b.WriteString("\n")
			}

			if start > 0 {
				b.WriteString(fmt.Sprintf("[TRUNCATED: %d earlier messages omitted]", start))
			}

			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_search — search across session messages
// ---------------------------------------------------------------------------

// SessionSearch returns a tool that searches messages across all sessions
// in the current namespace for a given text pattern.
func SessionSearch(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_search", "Search for a text pattern across all session messages. Returns matching messages with session info.").
		StringParam("pattern", "Text pattern to search for (case-insensitive substring match)", true).
		IntParam("max_results", "Maximum number of matches to return (optional, default 50)", false).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionSearchArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_search", &args)
			if err != nil {
				return "", err
			}
			if args.Pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			if args.MaxResults <= 0 {
				args.MaxResults = 50
			}

			sessions, err := sm.List(ctx, ns)
			if err != nil {
				return "", fmt.Errorf("session_search: %w", err)
			}

			pattern := strings.ToLower(args.Pattern)
			type match struct {
				sessionID   string
				sessionName string
				msgIndex    int
				role        agent.Role
				content     string
			}
			var matches []match

			for _, s := range sessions {
				for i, m := range s.Messages() {
					if strings.Contains(strings.ToLower(m.Content), pattern) {
						matches = append(matches, match{
							sessionID:   s.SessionID(),
							sessionName: s.SessionName(),
							msgIndex:    i + 1,
							role:        m.Role,
							content:     m.Content,
						})
						if len(matches) >= args.MaxResults {
							break
						}
					}
				}
				if len(matches) >= args.MaxResults {
					break
				}
			}

			if len(matches) == 0 {
				return fmt.Sprintf("No matches found for pattern %q.", args.Pattern), nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("%d match(es) for %q:\n\n", len(matches), args.Pattern))
			for _, m := range matches {
				name := m.sessionID[:8]
				if m.sessionName != "" {
					name = m.sessionName
				}
				b.WriteString(fmt.Sprintf("[%s #%d] %s:\n", name, m.msgIndex, m.role))
				b.WriteString(fmt.Sprintf("  %s\n", agent.Truncate(m.content, 300)))
				b.WriteString("\n")
			}
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_summarize — summarize a session's conversation
// ---------------------------------------------------------------------------

// SessionSummarize returns a tool that summarizes a session's conversation.
// If a Conversation is provided, it uses the configured LLM to generate
// the summary. Otherwise it produces a simple heuristic summary.
func SessionSummarize(sm *agent.SessionManager, conv *agent.Conversation) agent.Tool {
	return NewTool("session_summarize", "Summarize a session's conversation. Uses LLM when available, otherwise produces a statistical summary.").
		StringParam("session_id", "Session ID or unique prefix", true).
		IntParam("max_tokens", "Maximum tokens for the summary (optional, default 500)", false).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionSummarizeArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_summarize", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}
			if args.MaxTokens <= 0 {
				args.MaxTokens = 500
			}

			s, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "session_summarize")
			if err != nil {
				return "", err
			}

			if len(s.Messages()) == 0 {
				return "Session has no messages.", nil
			}

			// If we have a Conversation with an LLM adapter, use it for a
			// proper summary.
			if conv != nil && conv.Router() != nil {
				adapter := conv.Router().Adapter(s)
				if adapter != nil {
					summary, err := generateLLMSummary(ctx, adapter, s, args.MaxTokens)
					if err == nil {
						return summary, nil
					}
				}
			}

			return buildHeuristicSummary(s), nil
		}).
		Build()
}

// generateLLMSummary uses the LLM to generate a session summary.
func generateLLMSummary(ctx context.Context, adapter agent.LLMAdapter, s *agent.Session, maxTokens int) (string, error) {
	var b strings.Builder
	for _, m := range s.Messages() {
		switch m.Role {
		case agent.RoleUser:
			b.WriteString(fmt.Sprintf("User: %s\n", agent.Truncate(m.Content, 500)))
		case agent.RoleAssistant:
			if m.Content != "" {
				b.WriteString(fmt.Sprintf("Assistant: %s\n", agent.Truncate(m.Content, 500)))
			}
			for _, tc := range m.ToolCalls {
				b.WriteString(fmt.Sprintf("  [tool: %s]\n", tc.Name))
			}
		}
	}

	summaryMsgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "You are a helpful assistant that summarizes conversations."},
		{Role: agent.RoleUser, Content: fmt.Sprintf(
			"Summarize the following conversation in a few sentences. Focus on the main topics discussed, decisions made, and key information exchanged.\n\nConversation:\n%s", b.String(),
		)},
	}

	resp, err := adapter.Complete(ctx, summaryMsgs, nil, agent.CompleteOptions{MaxTokens: &maxTokens}, nil)
	if err != nil {
		return "", err
	}

	name := s.SessionName()
	if name == "" {
		name = s.SessionID()[:8]
	}
	return fmt.Sprintf("Summary for session %s:\n%s\n", name, resp.Message.Content), nil
}

// buildHeuristicSummary creates a summary from statistics.
func buildHeuristicSummary(s *agent.Session) string {
	var b strings.Builder

	name := s.SessionName()
	if name == "" {
		name = s.SessionID()[:8]
	}

	b.WriteString(fmt.Sprintf("Session: %s\n", name))
	b.WriteString(fmt.Sprintf("Created: %s\n", s.Read().CreatedAt.Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("Total messages: %d\n", len(s.Messages())))
	b.WriteString(fmt.Sprintf("Total tokens: %d\n", s.TotalTokenUsage()))

	userCount := 0
	assistantCount := 0
	toolCount := 0
	toolNames := make(map[string]int)
	for _, m := range s.Messages() {
		switch m.Role {
		case agent.RoleUser:
			userCount++
		case agent.RoleAssistant:
			assistantCount++
			for _, tc := range m.ToolCalls {
				toolCount++
				toolNames[tc.Name]++
			}
		case agent.RoleTool:
			toolCount++
		}
	}

	b.WriteString(fmt.Sprintf("User messages: %d\n", userCount))
	b.WriteString(fmt.Sprintf("Assistant messages: %d\n", assistantCount))
	if len(toolNames) > 0 {
		b.WriteString("Tools used:\n")
		for name, count := range toolNames {
			b.WriteString(fmt.Sprintf("  - %s: %d times\n", name, count))
		}
	}

	for _, m := range s.Messages() {
		if m.Role == agent.RoleUser {
			b.WriteString(fmt.Sprintf("\nFirst message: %s\n", agent.Truncate(m.Content, 200)))
			break
		}
	}

	for i := len(s.Messages()) - 1; i >= 0; i-- {
		if s.Messages()[i].Role == agent.RoleUser {
			b.WriteString(fmt.Sprintf("Last message: %s\n", agent.Truncate(s.Messages()[i].Content, 200)))
			break
		}
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// session_delete — delete a session
// ---------------------------------------------------------------------------

// SessionDelete returns a tool that deletes a session by ID or prefix.
func SessionDelete(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_delete", "Delete a session by its ID or unique prefix.").
		StringParam("session_id", "Session ID or unique prefix", true).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionIdentifyArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_delete", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}

			sessionID, err := sm.ResolveSessionID(ctx, ns, args.SessionID)
			if err != nil {
				return "", fmt.Errorf("session_delete: %w", err)
			}

			_, ok, err := sm.Store.Get(ctx, ns, sessionID)
			if err != nil {
				return "", fmt.Errorf("session_delete: %w", err)
			}
			if !ok {
				return "", fmt.Errorf("session %q not found", args.SessionID)
			}

			if err := sm.Store.Delete(ctx, ns, sessionID); err != nil {
				return "", fmt.Errorf("session_delete: %w", err)
			}
			return fmt.Sprintf("Session %s deleted.", sessionID[:8]), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_create — create a new empty session
// ---------------------------------------------------------------------------

// SessionCreate returns a tool that creates a new empty session in the
// current namespace.
func SessionCreate(sm *agent.SessionManager) agent.Tool {
	return NewTool("session_create", "Create a new empty session and return its ID.").
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			ns, err := sessionToolPreamble(ctx, "session_create")
			if err != nil {
				return "", err
			}

			session, err := sm.GetOrCreate(ctx, ns, "")
			if err != nil {
				return "", fmt.Errorf("session_create: %w", err)
			}
			return fmt.Sprintf("Created new session: %s", session.SessionID()), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// session_ask_question — ask a question about a session's conversation
// ---------------------------------------------------------------------------

// SessionAskQuestion returns a tool that asks a question about a session's
// conversation history. It builds a virtual message list (no persistence)
// with tool calls stripped, appends the question as a user message, and
// calls the LLM bound to that session to get an answer.
func SessionAskQuestion(sm *agent.SessionManager, conv *agent.Conversation) agent.Tool {
	return NewTool("session_ask_question", "Ask a question about a session's conversation.").
		StringParam("session_id", "Session ID or unique prefix", true).
		StringParam("question", "Your question about the session", true).
		Tags("session", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args sessionAskQuestionArgs
			ns, err := unmarshalSessionArgs(ctx, raw, "session_ask_question", &args)
			if err != nil {
				return "", err
			}
			if args.SessionID == "" {
				return "", fmt.Errorf("session_ask_question: session_id is required")
			}
			if args.Question == "" {
				return "", fmt.Errorf("session_ask_question: question is required")
			}

			s, err := resolveSessionFromTool(ctx, sm, ns, args.SessionID, "session_ask_question")
			if err != nil {
				return "", err
			}

			if conv == nil || conv.Router() == nil {
				return "", fmt.Errorf("session_ask_question: no LLM router available")
			}
			adapter := conv.Router().Adapter(s)
			if adapter == nil {
				return "", fmt.Errorf("session_ask_question: no LLM adapter available for session %s", s.SessionID()[:8])
			}

			msgs := buildAskQuestionMessages(s, args.Question)

			resp, err := adapter.Complete(ctx, msgs, nil, conv.Config().Options, nil)
			if err != nil {
				return "", fmt.Errorf("session_ask_question: LLM call failed: %w", err)
			}

			return resp.Message.Content, nil
		}).
		Build()
}

// buildAskQuestionMessages builds a message list from a session's history
// with tool calls stripped, then appends the question as a user message.
func buildAskQuestionMessages(session agent.SessionHistory, question string) []agent.Message {
	msgs := session.Messages()
	if sp := session.SystemPrompt(); sp != "" {
		msgs = append([]agent.Message{{Role: agent.RoleSystem, Content: sp}}, msgs...)
	}
	msgs = agent.StripToolCalls(msgs, false)

	msgs = append(msgs, agent.Message{
		Role:    agent.RoleUser,
		Content: question,
	})

	return msgs
}

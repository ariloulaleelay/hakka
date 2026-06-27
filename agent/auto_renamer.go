package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// Auto-rename helpers.
//
// These functions are extracted from Conversation so the orchestration
// type does not need to know about LLM adapters or naming-message
// construction — it focuses on the turn lifecycle instead.
// ---------------------------------------------------------------------------

// generateName calls the LLM to suggest a session name.
// Returns the sanitised name (or "" if no name could be generated) and any
// LLM or transport error. When adapter is nil it returns ("", nil) —
// callers distinguish this from an empty LLM response by checking the
// pre-condition themselves.
func generateName(ctx context.Context, adapter LLMAdapter, session SessionHistory, logger *slog.Logger) (string, error) {
	if adapter == nil {
		return "", nil
	}

	namingMsgs := buildNamingMessages(session)
	resp, err := adapter.Complete(ctx, namingMsgs, nil, CompleteOptions{})
	if err != nil {
		return "", err
	}

	name := strings.TrimSpace(resp.Message.Content)
	name = strings.Trim(name, `"'*#`)
	name = Truncate(name, 120)
	return name, nil
}

// StripToolCalls filters a raw message list for human-readable consumption
// by removing context_compactify messages and replacing assistant messages
// that only contain tool calls with a placeholder.
//
// When replaceToolsWithPlaceholder is true, tool-role messages are also
// replaced with the placeholder (as buildNamingMessages does). When false,
// they are dropped entirely (as buildAskQuestionMessages does).
func StripToolCalls(messages []Message, replaceToolsWithPlaceholder bool) []Message {
	msgs := make([]Message, 0, len(messages))
	for _, m := range messages {
		// Skip context_compactify messages entirely — they are engine
		// meta-tool artifacts that should never appear in human-readable
		// contexts (naming, ask-question). Without this, compactify-only
		// assistant messages would leak a misleading [TRUNCATED TOOL CALLS]
		// placeholder.
		if isCompactifyMessage(m) {
			continue
		}
		if m.Role == RoleTool {
			if replaceToolsWithPlaceholder {
				msgs = append(msgs, Message{
					Role:    RoleSystem,
					Content: "[TRUNCATED TOOL CALLS]",
				})
			}
			continue
		}
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			msgs = append(msgs, Message{
				Role:    RoleSystem,
				Content: "[TRUNCATED TOOL CALLS]",
			})
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// buildNamingMessages builds a short message list for the LLM naming task.
// It includes only user messages and assistant replies that are actual text
// responses (not tool-call messages). Tool-role messages and assistant
// tool-call-only messages are excluded because they don't contribute useful
// context for naming and can cause provider errors (empty content + nil
// tool_calls is an invalid assistant message).
//
// The naming instruction is placed as a system message at the END so it
// is the last thing the model sees before generating the name, preventing
// it from being buried by long conversations.
//
// Depends only on SessionHistory — the narrowest interface needed.
func buildNamingMessages(session SessionHistory) []Message {
	msgs := StripToolCalls(session.AllMessages(), true)
	// Drop genuine system messages but keep the [TRUNCATED TOOL CALLS] placeholders.
	filtered := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleUser || m.Role == RoleAssistant {
			filtered = append(filtered, m)
		} else if m.Role == RoleSystem && m.Content == "[TRUNCATED TOOL CALLS]" {
			filtered = append(filtered, m)
		}
	}
	// Append the naming instruction as the final user message so it is
	// the last thing the model sees.
	filtered = append(filtered, Message{
		Role:    RoleUser,
		Content: "Suggest name for this chat. One sentence. No quotes, no 'How about', just the name.",
	})
	return filtered
}

// notifyError fires the OnError hook and returns the error so callers can
// propagate it. Falls back to logging if no hook is configured.
func notifyError(sessionID string, err error, hooks Hooks, logger *slog.Logger) error {
	hooks.FireError(sessionID, err)
	if hooks.OnError == nil {
		logger.Error("llm complete error", "err", err, "session", sessionID)
	}
	return err
}

// sendEngineEvent sends an event to the optional event channel (non-blocking).
// If the channel is nil or full, the event is dropped silently.
func sendEngineEvent(events eventSender, ev event.EngineEvent) {
	if events == nil {
		return
	}
	events <- ev
}

// logCompactifySkipped logs a debug message when the LLM chose not to
// call context_compactify despite the soft limit being exceeded.
func logCompactifySkipped(logger *slog.Logger, sessionID string, iteration int, needCompactify bool, toolCalls []ToolCall) {
	if !needCompactify {
		return
	}
	names := make([]string, len(toolCalls))
	for j, tc := range toolCalls {
		names[j] = tc.Name
	}
	logger.Debug("tool-iteration compactify-needed-but-llm-called",
		"iteration", iteration,
		"session", sessionID,
		"calledTools", names,
	)
}

// recordLLMResponse appends the assistant message to the session,
// fires the LLM response hook, emits a UsageReported event, and
// updates token usage.
func recordLLMResponse(session SessionView, resp *llmStepResult, hooks Hooks, events eventSender) {
	msg := Message{Role: RoleAssistant, Content: resp.content, ToolCalls: resp.toolCalls, Usage: resp.usage}
	session.Append(msg)

	hooks.FireLLMResponse(session.SessionID(), &LLMResponse{Message: msg, Usage: resp.usage})
	if resp.usage != nil {
		session.AddTokenUsage(resp.usage.TotalTokens)
		sendEngineEvent(events, event.UsageReported{
			SessionID: session.SessionID(),
			Usage: event.UsageInfo{
				PromptTokens:     resp.usage.PromptTokens,
				CompletionTokens: resp.usage.CompletionTokens,
				TotalTokens:      resp.usage.TotalTokens,
			},
		})
	}
}

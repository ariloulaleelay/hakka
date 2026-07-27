package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
	"github.com/google/uuid"
)

// subagentNamespace is an ephemeral namespace used exclusively for subagent
// sessions. These sessions live in memory and are never persisted to any
// durable store visible to the user.
const subagentNamespace = "__subagent__"

// SubagentRun returns a tool that forks the current session and runs an
// autonomous subtask in a child LLM session, returning the result.
//
// The child session inherits:
//   - All messages from the parent (full conversation history)
//   - All enabled tools (except subagent_run, which is blocked to prevent
//     recursion)
//   - The parent's system prompt, CWD, and compact soft limit
//   - The parent's model
//   - Active skills from the parent
//
// The child runs in a fresh memory-backed store and is NOT persisted to
// any durable store visible to users. The parent gets back the final
// reply, token/cost stats, and a session ID (short) for reference.
func SubagentRun(router *agent.Router, tools *agent.ToolRegistry, cfg agent.EngineConfig, skills *agent.SkillRegistry) agent.Tool {
	return NewTool("subagent_run",
		"Run a subagent — fork the current session with a specific task. "+
			"The subagent inherits the full conversation history and all enabled tools "+
			"(except subagent_run itself, which is blocked to prevent recursion). "+
			"The subagent runs autonomously and returns its final result. "+
			"Use this for tasks that need their own LLM context, like refactoring a function "+
			"or analyzing a file while the main agent continues.").
		StringParam("task", "The task description for the subagent to execute. This is appended to the forked conversation as a user message.", true).
		IntParam("timeout_seconds", "Maximum execution time in seconds (default 900, max 3600)", false).
		Tags("developer", "agent", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Task           string `json:"task"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("subagent_run: %w", err)
			}
			if args.Task == "" {
				return "", fmt.Errorf("subagent_run: task is required")
			}
			if args.TimeoutSeconds <= 0 {
				args.TimeoutSeconds = 900
			}
			if args.TimeoutSeconds > 3600 {
				args.TimeoutSeconds = 3600
			}

			// Get the parent session from the context (injected by the engine
			// before calling the tool handler).
			parentView := event.SessionViewFromContext(ctx)
			if parentView == nil {
				return "", fmt.Errorf("subagent_run: no parent session available in context")
			}
			parentSession, ok := parentView.(*agent.Session)
			if !ok {
				return "", fmt.Errorf("subagent_run: parent session is not a *Session")
			}

			parentData := parentSession.Read()

			// ---------------------------------------------------------------
			// Fork the parent session into a child session
			//
			// IMPORTANT: The last assistant message may contain tool_calls
			// that are still in-flight (tool results not yet appended).
			// Forking them as-is would produce an invalid sequence for the
			// child's LLM provider ("tool_calls without matching results").
			// We strip those unresolved calls. If the message ends up empty,
			// we drop it entirely.
			// ---------------------------------------------------------------

			childData := agent.NewSessionData(subagentNamespace, parentData.SystemPrompt)
			childData.ID = uuid.New().String()
			childData.Messages = forkMessages(parentData.Messages)
			childData.ClientCWD = parentData.ClientCWD
			childData.CompactSoftLimit = parentData.CompactSoftLimit
			childData.Model = parentData.Model

			// Copy enabled tools, but block subagent_run to prevent recursion.
			childData.EnabledTools = make(map[string]bool, len(parentData.EnabledTools))
			for name, enabled := range parentData.EnabledTools {
				if enabled && name != "subagent_run" {
					childData.EnabledTools[name] = true
				}
			}

			childData.BlockedTools = map[string]bool{"subagent_run": true}

			// Copy active skills.
			if len(parentData.ActiveSkills) > 0 {
				childData.ActiveSkills = make([]string, len(parentData.ActiveSkills))
				copy(childData.ActiveSkills, parentData.ActiveSkills)
			}

			childSession := agent.NewSessionFromData(&childData)

			// ---------------------------------------------------------------
			// Set up an ephemeral child conversation
			// ---------------------------------------------------------------

			childStore := agent.NewMemoryStore()
			if err := childStore.Put(ctx, subagentNamespace, childSession); err != nil {
				return "", fmt.Errorf("subagent_run: save child session: %w", err)
			}

			childSM := agent.NewSessionManager(childStore, parentData.SystemPrompt)

			childConv := agent.NewConversation(childSM, router, tools, subagentNamespace, cfg)
			if skills != nil {
				childConv.SetSkills(skills)
			}

			// ---------------------------------------------------------------
			// Execute the subagent turn.
			//
			// IMPORTANT: We MUST override the namespace in the context
			// because the parent's context carries its own namespace
			// (e.g. "default"). If we don't, prepareWithInput will try to
			// look up the child session in the parent's namespace in the
			// child store, fail to find it, and create a brand-new empty
			// session — losing all forked history.
			// ---------------------------------------------------------------

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
			defer cancel()
			runCtx = event.ContextWithNamespace(runCtx, subagentNamespace)

			eventCh, err := childConv.Execute(runCtx, childSession.SessionID(), args.Task)
			if err != nil {
				return "", fmt.Errorf("subagent_run: execute: %w", err)
			}

			var reply string
			var totalTokens int
			var totalCost float64
			var messageCount int
			for evt := range eventCh {
				if tf, ok := evt.(event.TurnFinished); ok {
					if tf.Err != nil {
						return "", fmt.Errorf("subagent_run: %w", tf.Err)
					}
					reply = tf.Reply
					totalTokens = tf.TotalTokens
					totalCost = tf.TotalCost
					messageCount = tf.MessageCount
				}
			}

			// ---------------------------------------------------------------
			// Build the result
			// ---------------------------------------------------------------

			shortID := childSession.SessionID()
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}

			var b strings.Builder
			b.WriteString("Subagent result:\n")
			b.WriteString(reply)
			b.WriteString("\n\n")
			b.WriteString("---\n")
			b.WriteString(fmt.Sprintf("Session: %s\n", shortID))
			b.WriteString(fmt.Sprintf("Tokens: %d\n", totalTokens))
			b.WriteString(fmt.Sprintf("Cost: $%.6f\n", totalCost))
			b.WriteString(fmt.Sprintf("Messages: %d\n", messageCount))
			return b.String(), nil
		}).
		Build()
}

// forkMessages prepares a clean copy of parent messages for the child
// session. It strips unresolved tool calls (calls whose tool results
// haven't been appended yet) from the last assistant message. This
// is necessary because subagent_run itself is called as a tool — the
// engine records the assistant message with tool_calls before appending
// tool results. Forking that orphaned tool_calls message would produce
// an invalid sequence for the child's LLM provider.
func forkMessages(parentMsgs []agent.Message) []agent.Message {
	if len(parentMsgs) == 0 {
		return nil
	}

	// Build a set of all resolved tool call IDs.
	resolved := make(map[string]bool)
	for _, m := range parentMsgs {
		if m.Role == agent.RoleTool && m.ToolCallID != "" {
			resolved[m.ToolCallID] = true
		}
	}

	// Work on a copy.
	msgs := make([]agent.Message, len(parentMsgs))
	copy(msgs, parentMsgs)

	// Check the last message — if it's an assistant with unresolved tool
	// calls, strip them. If the message becomes empty, remove it entirely.
	last := len(msgs) - 1
	if msgs[last].Role == agent.RoleAssistant && len(msgs[last].ToolCalls) > 0 {
		var hasUnresolved bool
		for _, tc := range msgs[last].ToolCalls {
			if !resolved[tc.ID] {
				hasUnresolved = true
				break
			}
		}
		if hasUnresolved {
			filtered := make([]agent.ToolCall, 0, len(msgs[last].ToolCalls))
			for _, tc := range msgs[last].ToolCalls {
				if resolved[tc.ID] {
					filtered = append(filtered, tc)
				}
			}
			msgs[last].ToolCalls = filtered
			if msgs[last].Content == "" && len(filtered) == 0 {
				msgs = msgs[:last]
			}
		}
	}

	return msgs
}

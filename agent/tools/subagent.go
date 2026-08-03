package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// SubagentRun returns a tool that forks the current session and runs an
// autonomous subtask in a child LLM session, returning the result.
//
// The child session inherits:
//   - All messages from the parent (full conversation history)
//   - All enabled tools (except subagent_run, which is blocked to prevent
//     recursion)
//   - The parent's system prompt, CWD, model, compact soft limit, and
//     active skills
//
// The child is a REAL session persisted in the parent's store and
// namespace, linked to the parent via ParentID/ForkPoint. It is visible
// to session tools (session_list, session_read, ...) and survives
// restarts, so it can be inspected, continued, or deleted like any other
// session. The parent gets back the final reply, token/cost stats, and a
// short child session ID for reference.
func SubagentRun(sessions *agent.SessionManager, router *agent.Router, tools *agent.ToolRegistry, cfg agent.EngineConfig, skills *agent.SkillRegistry) agent.Tool {
	return NewTool("subagent_run",
		"Run a subagent — fork the current session with a specific task. "+
			"The subagent inherits the full conversation history and all enabled tools "+
			"(except subagent_run itself, which is blocked to prevent recursion). "+
			"The subagent runs autonomously in a persistent child session (visible in "+
			"session_list, linked to the parent via parent_id) and returns its final result. "+
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

			ns := event.NamespaceFromContext(ctx)
			if ns == "" {
				return "", fmt.Errorf("subagent_run: no namespace available in context")
			}

			parentData := parentSession.Read()

			// ---------------------------------------------------------------
			// Fork the parent session into a persistent child session.
			//
			// ForkData copies all parent messages up to the last message
			// (inclusive) and strips any in-flight tool calls whose results
			// have not been appended yet (e.g. subagent_run itself), so the
			// child always receives a valid message sequence. It also
			// establishes the lineage: ParentID + ForkPoint.
			// ---------------------------------------------------------------

			forkPoint := ""
			if n := len(parentData.Messages); n > 0 {
				forkPoint = parentData.Messages[n-1].ID
			}
			childData, err := parentData.ForkData(forkPoint)
			if err != nil {
				return "", fmt.Errorf("subagent_run: fork: %w", err)
			}

			// Defensive fallback: engine-managed sessions always assign IDs
			// to every message, so forkPoint is non-empty whenever there is
			// history. If a session was constructed manually without message
			// IDs, ForkData("") returns a blank child — copy the full history
			// instead so we never silently drop context. In this degraded
			// mode ForkPoint stays empty (there is no ID to reference).
			if forkPoint == "" && len(parentData.Messages) > 0 {
				childData.Messages = make([]agent.Message, len(parentData.Messages))
				copy(childData.Messages, parentData.Messages)
			}

			// New identity in the parent's namespace.
			childData.Namespace = ns
			childData.ID = agent.MakeUniqueID()
			childData.Name = ""

			// Block subagent_run on the child to prevent recursion. Mutate
			// the maps directly — DenyTool/DisableTool would fail when the
			// forked history already contains a subagent_run call (locked
			// tools), and replacing BlockedTools would drop the parent's
			// other blocked tools.
			if childData.BlockedTools == nil {
				childData.BlockedTools = make(map[string]bool)
			}
			childData.BlockedTools["subagent_run"] = true
			if childData.EnabledTools != nil {
				childData.EnabledTools["subagent_run"] = false
			}

			// ---------------------------------------------------------------
			// Inform the child that it is a subagent.
			//
			// The forked history may contain the parent's show_tool output
			// that describes subagent_run (or the parent's own requests to
			// run subagents), which can mislead the child into trying to
			// fork further subagents — only to get "denied" errors. An
			// explicit notice prevents that: the child learns it is ALREADY
			// a subagent and that subagent_run is blocked for it.
			//
			// The notice is deliberately a USER message, not a system
			// message: system content is part of the prompt-cache key
			// (Anthropic system string, OpenAI instructions, Gemini system
			// instruction), so a system notice would invalidate the cache
			// for the inherited prefix. As a user message appended AFTER
			// the forked history, the system + history prefix stays
			// byte-identical to the parent's last request and remains
			// cacheable; only the notice + task are new tokens. It is also
			// appended (not prepended) so compaction-range indices from the
			// parent stay aligned.
			// ---------------------------------------------------------------

			parentShort := parentSession.SessionID()
			if len(parentShort) > 8 {
				parentShort = parentShort[:8]
			}
			notice := agent.Message{
				ID:        agent.MakeUniqueID(),
				Role:      agent.RoleUser,
				Content:   fmt.Sprintf("You are a subagent — an autonomous child session forked from parent session %s to complete a specific task. You are ALREADY inside a subagent session: the subagent_run tool is permanently blocked for you and cannot be enabled, so do NOT attempt to call it or fork further subagents. The conversation history inherited from the parent session is provided as context only. Complete your assigned task with the tools available to you, then return a concise final result.", parentShort),
				Timestamp: time.Now().UnixMilli(),
			}
			childData.Messages = append(childData.Messages, notice)

			childSession := agent.NewSessionFromData(&childData)

			// ---------------------------------------------------------------
			// Persist the child BEFORE running the turn — it is a real
			// session in the parent's store, not an ephemeral in-memory fork.
			// ---------------------------------------------------------------

			if err := sessions.Save(ctx, ns, childSession); err != nil {
				return "", fmt.Errorf("subagent_run: save child session: %w", err)
			}

			// Notify all connected clients about the new child session so
			// they can update the session list without reconnecting.
			if sender := event.EventSenderFromContext(ctx); sender != nil {
				sender <- event.SessionCreated{
					SessionID: childSession.SessionID(),
					Session:   childSession.Metadata(),
				}
			}

			// ---------------------------------------------------------------
			// Set up the child conversation on the SAME store/namespace, so
			// the turn's saves land on the persisted child session.
			// ---------------------------------------------------------------

			childConv := agent.NewConversation(sessions, router, tools, ns, cfg)
			if skills != nil {
				childConv.SetSkills(skills)
			}

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
			defer cancel()
			runCtx = event.ContextWithNamespace(runCtx, ns)

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
			b.WriteString(fmt.Sprintf("Session: %s (persistent child of %s)\n", shortID, parentShort))
			b.WriteString(fmt.Sprintf("Tokens: %d\n", totalTokens))
			b.WriteString(fmt.Sprintf("Cost: $%.6f\n", totalCost))
			b.WriteString(fmt.Sprintf("Messages: %d\n", messageCount))
			return b.String(), nil
		}).
		Build()
}

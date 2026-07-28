package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// stepFunc abstracts how the LLM response is obtained. It is the single
// extension point that differentiates streaming from non-streaming turns.
// Implementations receive the event channel so streaming steps can emit
// TextDelta events directly.
type stepFunc func(ctx context.Context, msgs []Message, schemas []ToolSchema, events eventSender) (*llmStepResult, error)

// llmStepResult is the result of a single LLM call within the tool loop.
type llmStepResult struct {
	content      string
	toolCalls    []ToolCall
	usage        *Usage
	finishReason string
}

// turnRunner drives the LLM ↔ tool iteration loop for a single turn.
// It is the extracted "orchestration core" that Conversation delegates to.
//
// turnRunner owns:
//   - The tool iteration loop (run, runOneIteration)
//   - Default (non-streaming) step function creation (defaultStep)
//   - Compaction limit resolution (resolveSoftLimit)
//   - Compactify schema augmentation (augmentSchemasWithCompactify)
//
// It does NOT own:
//   - Session lifecycle (get/create, persist) — those stay in Conversation
//   - Event channel creation and turn lifecycle — those stay in Conversation
//   - Auto-rename — stays in Conversation
//   - Model binding — stays in Conversation
//
// This split follows the Single Responsibility Principle: turnRunner
// focuses on the mechanics of the LLM↔tool loop; Conversation focuses
// on session lifecycle and the public API for gateways.
type turnRunner struct {
	tools    *ToolRegistry
	toolExec *toolExecutor
	config   EngineConfig
	router   *Router
	logger   *slog.Logger
	skills   *SkillRegistry
}

// newTurnRunner builds a turnRunner from the shared engine dependencies.
// It is called by NewConversation and also by SetToolContext (when the
// tool executor needs to be rebuilt).
func newTurnRunner(tools *ToolRegistry, toolExec *toolExecutor, config EngineConfig, router *Router, logger *slog.Logger, skills *SkillRegistry) *turnRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &turnRunner{
		tools:    tools,
		toolExec: toolExec,
		config:   config,
		router:   router,
		logger:   logger,
		skills:   skills,
	}
}

// ---------------------------------------------------------------------------
// Public API (used by Conversation)
// ---------------------------------------------------------------------------

// run is the shared core that both Complete-based and Stream-based loops
// delegate to. The step function abstracts how the LLM response is
// obtained; run handles hook serialisation, recording the assistant
// response, firing events, tracking usage, and executing tools.
//
// On success it returns the final assistant text. On exhaustion of
// iterations it returns ErrMaxIterations.
//
// Schemas are computed fresh each iteration from the current session state.
// This catches mid-turn changes such as the LLM calling allow_tool or
// deny_tool during the same turn.
//
// saveFn is called after each iteration round to persist the session. It
// is provided by Conversation so turnRunner does not need to know about
// session persistence details.
//
// Tool handlers (e.g. allow_tool) mutate the session in-place via the
// context-injected session view — they NEVER fetch and save the session
// from the store independently. This means the engine's session pointer
// is never stale, and no reloadFn/mergeToolResults is needed.
func (r *turnRunner) run(
	ctx context.Context,
	session SessionView,
	events eventSender,
	step stepFunc,
	saveFn func(context.Context, SessionView) error,
) (string, error) {
	const maxAutoContinue = 3

	var turnMu sync.Mutex
	hooks := r.toolExec.SerialisedHooks(&turnMu)

	autoContinueCount := 0

	for i := 0; i < r.config.MaxToolIterations; i++ {
		schemas := r.tools.SchemasForSession(session)
		resp, needCompactify, err := r.runOneIteration(ctx, session, schemas, events, step, hooks, i)
		if err != nil {
			// Persist session state before propagating — runTurnWithStep
			// skips finishTurn when err != nil, but we still want to
			// preserve whatever messages were recorded before the failure.
			if saveErr := saveFn(ctx, session); saveErr != nil {
				r.logger.Error("failed to save session on iteration error",
					"session", session.SessionID(), "iteration", i, "error", saveErr)
			}
			return "", err
		}
		if resp == nil {
			continue // compactify-only round — loop again
		}
		if len(resp.toolCalls) == 0 {
			// Autocontinue hack: some providers (e.g. DeepSeek) return
			// finish_reason="stop" with empty content and no tool calls
			// after processing tool results — they "reason" silently and
			// stop. When ignore_stop_if_no_content is enabled for this
			// model, re-prompt automatically.
			if resp.content == "" && resp.finishReason == "stop" && autoContinueCount < maxAutoContinue {
				if profile, ok := r.router.GetProfile(session); ok {
					if profile.Hacks.IgnoreStopIfNoContent != nil && *profile.Hacks.IgnoreStopIfNoContent {
						autoContinueCount++
						r.logger.Warn("autocontinue: empty stop response, re-prompting",
							"session", session.SessionID(),
							"iteration", i,
							"auto_continue_round", autoContinueCount,
							"max_auto_continue", maxAutoContinue)
						continue
					}
				}
			}
			if resp.content == "" && resp.finishReason == "stop" && autoContinueCount >= maxAutoContinue {
				if profile, ok := r.router.GetProfile(session); ok {
					if profile.Hacks.IgnoreStopIfNoContent != nil && *profile.Hacks.IgnoreStopIfNoContent {
						r.logger.Warn("autocontinue: exhausted rounds, finishing",
							"session", session.SessionID(),
							"iteration", i,
							"auto_continue_round", autoContinueCount)
					}
				}
			}

			r.logger.Debug("tool-iteration-loop: text-only response, finishing turn",
				"session", session.SessionID(),
				"iteration", i,
				"finish_reason", resp.finishReason)
			return resp.content, nil
		}

		r.logger.Debug("tool-iteration-loop: tool calls detected, continuing",
			"session", session.SessionID(),
			"iteration", i,
			"finish_reason", resp.finishReason,
			"tool_calls", len(resp.toolCalls))

		logCompactifySkipped(r.logger, session.SessionID(), i, needCompactify, resp.toolCalls)

		// Capture the old session name before tool execution, so we can
		// detect if a tool (e.g. session_rename) changes it.
		oldName := session.SessionName()
		r.toolExec.ExecuteToolCalls(ctx, session, resp.toolCalls, events, hooks)

		// Emit SessionRenamed event if the LLM called session_rename on
		// the current session.
		emitSessionRenamedIfNeeded(events, session, oldName, resp.toolCalls)

		if saveErr := saveFn(ctx, session); saveErr != nil {
			r.logger.Error("failed to save session mid-turn",
				"session", session.SessionID(), "iteration", i, "error", saveErr)
			return "", fmt.Errorf("save session: %w", saveErr)
		}
	}

	r.logger.Warn("tool-iteration-loop: max iterations reached",
		"session", session.SessionID(),
		"max_iterations", r.config.MaxToolIterations)
	return "", ErrMaxIterations
}

// emitSessionRenamedIfNeeded checks whether any tool call was a
// session_rename targeting the current session, and if so emits a
// SessionRenamed event via the events channel.
func emitSessionRenamedIfNeeded(events eventSender, session SessionView, oldName string, calls []ToolCall) {
	for _, call := range calls {
		if call.Name == "session_rename" {
			var args struct {
				SessionID string `json:"session_id"`
				Name      string `json:"name"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				continue
			}
			if args.Name == "" {
				continue
			}
			// Check if the renamed session is the current one.
			// The tool accepts prefixes, so check both exact match and prefix.
			if args.SessionID == session.SessionID() || strings.HasPrefix(session.SessionID(), args.SessionID) {
				events <- event.SessionRenamed{
					SessionID: session.SessionID(),
					OldName:   oldName,
					NewName:   args.Name,
				}
			}
		}
	}
}

// defaultStep creates a non-streaming step function that uses
// adapter.Complete to obtain LLM responses.
func (r *turnRunner) defaultStep(session SessionView) stepFunc {
	return func(ctx context.Context, msgs []Message, schemas []ToolSchema, _ eventSender) (*llmStepResult, error) {
		adapter := r.router.Adapter(session)
		start := time.Now()
		opts := r.config.Options
		opts.SessionID = session.SessionID()
		resp, err := adapter.Complete(ctx, msgs, schemas, opts)
		if err != nil {
			return nil, err
		}
		if resp.Usage != nil {
			resp.Usage.Duration = time.Since(start)
		}
		return &llmStepResult{
			content:      resp.Message.Content,
			toolCalls:    resp.Message.ToolCalls,
			usage:        resp.Usage,
			finishReason: resp.FinishReason,
		}, nil
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// runOneIteration executes a single LLM-call+record cycle within the
// tool loop. It builds the compacted context, calls the step function,
// records the assistant response, and returns the result.
//
// Returns (nil, _, nil) when the LLM only called context_compactify —
// the caller should loop again so the newly-compacted context takes effect.
func (r *turnRunner) runOneIteration(
	ctx context.Context,
	session SessionView,
	schemas []ToolSchema,
	events eventSender,
	step stepFunc,
	hooks Hooks,
	iteration int,
) (*llmStepResult, bool, error) {
	softLimit := r.resolveSoftLimit(session)
	msgs, needCompactify, estimatedTokens := BuildCompactContext(session, softLimit, r.skills)

	// Store estimated context size before the LLM call so
	// clients can display current context utilisation.
	session.SetEstimatedContextTokens(estimatedTokens)

	turnSchemas := r.augmentSchemasWithCompactify(schemas, needCompactify)

	// Inject the tool list system message into the context so the LLM
	// knows what tools are available and their status.
	msgs = appendToolListMessage(msgs, r.tools, session)

	r.logger.Debug("tool-iteration context",
		"iteration", iteration,
		"session", session.SessionID(),
		"estTokens", estimatedTokens,
		"softLimit", softLimit,
		"needCompactify", needCompactify)

	resp, err := step(ctx, msgs, turnSchemas, events)
	if err != nil {
		return nil, needCompactify, notifyError(session.SessionID(), err, hooks, r.logger)
	}

	r.logger.Debug("tool-iteration-result",
		"iteration", iteration,
		"session", session.SessionID(),
		"finish_reason", resp.finishReason,
		"tool_calls", len(resp.toolCalls),
		"content_len", len(resp.content))

	// Enrich tool calls with exec snippets before recording into session
	// history, so that clients (web, nvim) can display human-readable
	// summaries when loading previously executed turns.
	for i := range resp.toolCalls {
		resp.toolCalls[i].ExecSnippet = r.tools.ExecSnippet(
			resp.toolCalls[i].Name,
			resp.toolCalls[i].Arguments,
		)
	}

	recordLLMResponse(session, resp, hooks, events)

	if len(resp.toolCalls) == 1 && resp.toolCalls[0].Name == ContextCompactifyToolName {
		return nil, needCompactify, nil
	}

	return resp, needCompactify, nil
}

// resolveSoftLimit returns the effective compaction soft limit for the
// session. The session's limit is set explicitly at session init time
// (see Conversation.EnsureDefaultModel). We keep a safety net for
// existing sessions that may have been created before this feature.
func (r *turnRunner) resolveSoftLimit(session SessionView) int {
	softLimit := session.GetCompactSoftLimit()
	if softLimit <= 0 {
		softLimit = r.config.CompactSoftLimit
	}
	return softLimit
}

// augmentSchemasWithCompactify returns a copy of schemas with
// context_compactify appended when compaction is needed and the
// schema is not already present.
func (r *turnRunner) augmentSchemasWithCompactify(schemas []ToolSchema, needCompactify bool) []ToolSchema {
	if !needCompactify {
		return schemas
	}
	for _, s := range schemas {
		if s.Name == ContextCompactifyToolName {
			return schemas // already present
		}
	}
	return append(schemas, contextCompactifySchema)
}

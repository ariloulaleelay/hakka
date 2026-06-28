package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// stepFunc abstracts how the LLM response is obtained. It is the single
// extension point that differentiates streaming from non-streaming turns.
// Implementations receive the event channel so streaming steps can emit
// TextDelta events directly.
type stepFunc func(ctx context.Context, msgs []Message, schemas []ToolSchema, events eventSender) (*llmStepResult, error)

// llmStepResult is the result of a single LLM call within the tool loop.
type llmStepResult struct {
	content   string
	toolCalls []ToolCall
	usage     *Usage
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
}

// newTurnRunner builds a turnRunner from the shared engine dependencies.
// It is called by NewConversation and also by SetToolContext (when the
// tool executor needs to be rebuilt).
func newTurnRunner(tools *ToolRegistry, toolExec *toolExecutor, config EngineConfig, router *Router, logger *slog.Logger) *turnRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &turnRunner{
		tools:    tools,
		toolExec: toolExec,
		config:   config,
		router:   router,
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// Public API (used by Conversation)
// ---------------------------------------------------------------------------

// run drives the LLM ↔ tool iteration loop using a caller-provided step
// function. It is the shared core that both Complete-based and Stream-based
// loops delegate to.
//
// The step function abstracts how the LLM response is obtained; run
// handles everything else: hook serialisation, recording the assistant
// response, firing events, tracking usage, and executing tools when the
// model requests them.
//
// On success it returns the final assistant text. On exhaustion of
// iterations it returns ErrMaxIterations. saveFn is called after each
// iteration round to persist the session — it is provided by Conversation
// so turnRunner does not need to know about session persistence details.
func (r *turnRunner) run(
	ctx context.Context,
	session SessionView,
	events eventSender,
	step stepFunc,
	saveFn func(context.Context, SessionView) error,
) (string, error) {
	schemas := r.tools.SchemasForSession(session)
	var turnMu sync.Mutex
	hooks := r.toolExec.SerialisedHooks(&turnMu)

	for i := 0; i < r.config.MaxToolIterations; i++ {
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
			return resp.content, nil
		}

		logCompactifySkipped(r.logger, session.SessionID(), i, needCompactify, resp.toolCalls)
		r.toolExec.ExecuteToolCalls(ctx, session, resp.toolCalls, events, hooks)

		if saveErr := saveFn(ctx, session); saveErr != nil {
			r.logger.Error("failed to save session mid-turn",
				"session", session.SessionID(), "iteration", i, "error", saveErr)
			return "", fmt.Errorf("save session: %w", saveErr)
		}
	}

	return "", ErrMaxIterations
}

// defaultStep creates a non-streaming step function that uses
// adapter.Complete to obtain LLM responses.
func (r *turnRunner) defaultStep(session SessionView) stepFunc {
	return func(ctx context.Context, msgs []Message, schemas []ToolSchema, _ eventSender) (*llmStepResult, error) {
		adapter := r.router.Adapter(session)
		resp, err := adapter.Complete(ctx, msgs, schemas, r.config.Options)
		if err != nil {
			return nil, err
		}
		return &llmStepResult{
			content:   resp.Message.Content,
			toolCalls: resp.Message.ToolCalls,
			usage:     resp.Usage,
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
	msgs, needCompactify, estimatedTokens := BuildCompactContext(session, softLimit)
	turnSchemas := r.augmentSchemasWithCompactify(schemas, needCompactify)

	r.logger.Debug("tool-iteration context",
		"iteration", iteration,
		"session", session.SessionID(),
		"estTokens", estimatedTokens,
		"softLimit", softLimit,
		"needCompactify", needCompactify)

	resp, err := step(ctx, msgs, turnSchemas, events)
	if err != nil {
		// Step failed — run() will save the session via saveFn before
		// propagating. We just fire the error hook and return here.
		return nil, needCompactify, notifyError(session.SessionID(), err, hooks, r.logger)
	}

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

	// If the LLM only called context_compactify (no real tool calls),
	// signal the caller to loop again so the newly-compacted context
	// takes effect immediately.
	if len(resp.toolCalls) == 1 && resp.toolCalls[0].Name == ContextCompactifyToolName {
		return nil, needCompactify, nil
	}

	return resp, needCompactify, nil
}

// resolveSoftLimit returns the effective compaction soft limit for the
// session. Falls back from session → engine config → global default.
func (r *turnRunner) resolveSoftLimit(session SessionView) int {
	softLimit := session.GetCompactSoftLimit()
	if softLimit <= 0 {
		softLimit = r.config.CompactSoftLimit
	}
	if softLimit <= 0 {
		softLimit = DefaultEngineConfig().CompactSoftLimit
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

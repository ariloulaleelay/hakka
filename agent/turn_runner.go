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

// turnRunner drives the LLM ↔ tool iteration loop for a single turn.
// Session lifecycle, auto-rename, and model binding stay in Conversation.
type turnRunner struct {
	tools    *ToolRegistry
	toolExec *toolExecutor
	config   EngineConfig
	router   *Router
	logger   *slog.Logger
	skills   *SkillRegistry
	store    SessionStore
}

// newTurnRunner builds a turnRunner from the shared engine dependencies.
func newTurnRunner(tools *ToolRegistry, toolExec *toolExecutor, config EngineConfig, router *Router, logger *slog.Logger, skills *SkillRegistry, store SessionStore) *turnRunner {
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
		store:    store,
	}
}

// executeTurn spawns a goroutine, runs the tool iteration loop,
// saves post-turn, triggers auto-rename, and emits TurnFinished.
func (r *turnRunner) executeTurn(
	ctx context.Context,
	session SessionView,
	renameFn func(context.Context, SessionView, eventSender),
) <-chan event.EngineEvent {
	eventCh := make(chan event.EngineEvent, 256)
	go func() {
		defer close(eventCh)

		reply, msgID, err := r.runLoop(ctx, session, eventCh)

		// Persist estimated context tokens post-turn (best-effort).
		ns := event.NamespaceFromContext(ctx)
		ect := session.GetEstimatedContextTokens()
		_ = r.store.PatchMeta(ctx, ns, session.SessionID(), &SessionMetaPatch{
			EstimatedContextTokens: &ect,
		})

		// Fetch quota from the provider after the turn (best-effort).
		r.fetchAndEmitQuota(ctx, session, eventCh)

		if err == nil {
			renameFn(ctx, session, eventCh)
		}

		eventCh <- event.TurnFinished{
			SessionID:              session.SessionID(),
			Reply:                  reply,
			Err:                    err,
			TotalTokens:            session.TotalTokenUsage(),
			TotalCost:              session.TotalCost(),
			MessageCount:           len(session.Messages()),
			EstimatedContextTokens: session.GetEstimatedContextTokens(),
			Model:                  session.GetModel(),
			MessageID:              msgID,
		}
	}()
	return eventCh
}

// runLoop is the tool iteration loop:
// buildLLMContext → call LLM → validateResponse → executeTools → save → repeat.
func (r *turnRunner) runLoop(
	ctx context.Context,
	session SessionView,
	events eventSender,
) (string, string, error) {
	const maxAutoContinue = 3

	var turnMu sync.Mutex
	hooks := r.toolExec.SerialisedHooks(&turnMu)
	autoContinueCount := 0

	// Accumulated deltas for AppendMessages since last save.
	var accumTokens int
	var accumCost float64

	// msgID is captured by onDelta and updated each iteration before
	// calling Complete. This way streaming deltas and the final TurnFinished
	// event all carry the same message ID.
	var msgID string

	// onDelta is a sidecar progress channel.
	// adapters that support streaming call it, others ignore it.
	// The LLMResponse.Message.Content remains the source of truth.
	onDelta := func(delta string) {
		select {
		case <-ctx.Done():
		case events <- event.TextDelta{SessionID: session.SessionID(), Delta: delta, MessageID: msgID}:
		}
	}

	for i := 0; i < r.config.MaxToolIterations; i++ {
		schemas := r.tools.SchemasForSession(session)
		msgs, needCompactify, estimatedTokens := r.buildLLMContext(session)
		session.SetEstimatedContextTokens(estimatedTokens)
		if needCompactify {
			schemas = r.augmentSchemasWithCompactify(schemas)
		}

		adapter := r.router.Adapter(session)
		start := time.Now()
		opts := r.config.Options
		opts.SessionID = session.SessionID()

		// Generate the message ID before calling Complete so that
		// streaming deltas and the final TurnFinished event all
		// carry the same ID.
		msgID = MakeUniqueID()

		resp, err := adapter.Complete(ctx, msgs, schemas, opts, onDelta)
		if err != nil {
			notifyError(session.SessionID(), err, hooks, r.logger)
			return "", msgID, err
		}
		if err := r.validateResponse(resp); err != nil {
			return "", msgID, notifyError(session.SessionID(), err, hooks, r.logger)
		}

		if resp.Usage != nil {
			resp.Usage.Duration = time.Since(start)
		}

		r.logger.Debug("tool-iteration-result",
			"iteration", i,
			"session", session.SessionID(),
			"finish_reason", resp.FinishReason,
			"tool_calls", len(resp.Message.ToolCalls),
			"content_len", len(resp.Message.Content))

		r.reportUsage(session, resp, events)
		assistantMessage := Message{
			ID:               msgID,
			Role:             RoleAssistant,
			Content:          resp.Message.Content,
			ToolCalls:        resp.Message.ToolCalls,
			Usage:            resp.Usage,
			FinishReason:     resp.FinishReason,
			ProviderMetadata: resp.Message.ProviderMetadata,
			Timestamp:        nowMillis(),
		}

		r.logger.Debug("tool-iteration-loop: tool calls detected, continuing",
			"session", session.SessionID(), "iteration", i,
			"finish_reason", resp.FinishReason, "tool_calls", len(resp.Message.ToolCalls))

		toolMessages := r.executeTools(ctx, session, resp.Message.ToolCalls, events, hooks)
		session.Append(assistantMessage)
		for _, toolMessage := range toolMessages {
			session.Append(toolMessage)
		}

		// Accumulate delta for AppendMessages.
		if resp.Usage != nil {
			accumTokens += resp.Usage.TotalTokens
			accumCost += resp.Usage.Cost
		}
		ns := event.NamespaceFromContext(ctx)
		allNew := append([]Message{assistantMessage}, toolMessages...)
		if err := r.store.AppendMessages(ctx, ns, session.SessionID(), allNew, accumTokens, accumCost); err != nil {
			r.logger.Error("failed to save session mid-turn", "session", session.SessionID(), "iteration", i, "error", err)
			return "", msgID, fmt.Errorf("save session: %w", err)
		}
		accumTokens = 0
		accumCost = 0

		if r.isEmptyStopResponse(resp) && r.isIgnoreStopOnNoContent(session) {
			if autoContinueCount < maxAutoContinue {
				autoContinueCount++
				r.logger.Warn("autocontinue: empty stop response, re-prompting",
					"session", session.SessionID(), "iteration", i,
					"auto_continue_round", autoContinueCount, "max_auto_continue", maxAutoContinue)
				continue
			} else {
				r.logger.Warn("autocontinue: exhausted rounds, finishing",
					"session", session.SessionID(), "iteration", i,
					"auto_continue_round", autoContinueCount)
				return resp.Message.Content, msgID, nil
			}
		}

		if r.isFinalResponse(resp) {
			return resp.Message.Content, msgID, nil
		}
	}

	r.logger.Warn("tool-iteration-loop: max iterations reached",
		"session", session.SessionID(), "max_iterations", r.config.MaxToolIterations)
	return "", msgID, ErrMaxIterations
}

// buildLLMContext compacts history, injects tool list, and returns messages,
// whether compaction is needed, and estimated token count.
func (r *turnRunner) buildLLMContext(session SessionView) ([]Message, bool, int) {
	softLimit := r.resolveSoftLimit(session)
	msgs, needCompactify, estimatedTokens := BuildCompactContext(session, softLimit, r.skills)
	msgs = appendToolListMessage(msgs, r.tools, session)
	return msgs, needCompactify, estimatedTokens
}

func (r *turnRunner) validateResponse(resp *LLMResponse) error {
	for i := range resp.Message.ToolCalls {
		if resp.Message.ToolCalls[i].Arguments == "" {
			resp.Message.ToolCalls[i].Arguments = "{}"
		}
	}
	for i := range resp.Message.ToolCalls {
		resp.Message.ToolCalls[i].ExecSnippet = r.tools.ExecSnippet(
			resp.Message.ToolCalls[i].Name,
			resp.Message.ToolCalls[i].Arguments,
		)
	}
	for _, tc := range resp.Message.ToolCalls {
		if !json.Valid([]byte(tc.Arguments)) {
			return fmt.Errorf("tool call %q has invalid (truncated?) JSON arguments", tc.Name)
		}
	}
	return nil
}

func (r *turnRunner) reportUsage(
	session SessionView,
	resp *LLMResponse,
	events eventSender,
	//hooks Hooks,
) {
	if events == nil {
		return
	}
	usage := resp.Usage
	if usage == nil {
		return
	}

	// TODO do we ever need this hook?
	// Seems to be unused
	// hooks.FireLLMResponse(session.SessionID(), &LLMResponse{Message: msg, Usage: usage})

	session.AddTokenUsage(usage.TotalTokens)
	session.AddCost(usage.Cost)
	events <- event.UsageReported{
		SessionID: session.SessionID(),
		Usage: event.UsageInfo{
			PromptTokens:           usage.PromptTokens,
			CompletionTokens:       usage.CompletionTokens,
			TotalTokens:            usage.TotalTokens,
			Duration:               usage.Duration,
			Cost:                   usage.Cost,
			TotalCost:              session.TotalCost(),
			EstimatedContextTokens: session.GetEstimatedContextTokens(),
		},
	}
}

func (r *turnRunner) executeTools(ctx context.Context, session SessionView, toolCalls []ToolCall, events eventSender, hooks Hooks) []Message {
	oldName := session.SessionName()
	result := r.toolExec.ExecuteToolCalls(ctx, session, toolCalls, events, hooks)
	r.detectConversationRename(events, session, oldName, toolCalls)
	return result
}

func (r *turnRunner) isFinalResponse(resp *LLMResponse) bool {
	if len(resp.Message.ToolCalls) == 0 {
		return true
	}
	return false
}

func (r *turnRunner) isEmptyStopResponse(resp *LLMResponse) bool {
	message := resp.Message
	if len(resp.Message.ToolCalls) == 0 && message.Content == "" && resp.FinishReason == "stop" {
		return true
	}
	return false
}

func (r *turnRunner) isIgnoreStopOnNoContent(session SessionView) bool {
	if profile, ok := r.router.GetProfile(session); ok {
		if profile.Hacks.IgnoreStopIfNoContent != nil && *profile.Hacks.IgnoreStopIfNoContent {
			return true
		}
	}
	return false
}

func (r *turnRunner) detectConversationRename(events eventSender, session SessionView, oldName string, calls []ToolCall) {
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

func (r *turnRunner) resolveSoftLimit(session SessionView) int {
	softLimit := session.GetCompactSoftLimit()
	if softLimit <= 0 {
		softLimit = r.config.CompactSoftLimit
	}
	return softLimit
}

func (r *turnRunner) augmentSchemasWithCompactify(schemas []ToolSchema) []ToolSchema {
	for _, s := range schemas {
		if s.Name == ContextCompactifyToolName {
			return schemas
		}
	}
	return append(schemas, contextCompactifySchema)
}

// fetchAndEmitQuota attempts to fetch quota info from the provider after a
// turn. Non-blocking best-effort — errors are logged but not surfaced.
func (r *turnRunner) fetchAndEmitQuota(ctx context.Context, session SessionView, events eventSender) {
	if events == nil {
		return
	}
	adapter := r.router.Adapter(session)
	qf, ok := adapter.(QuotaFetcher)
	if !ok {
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	info, err := qf.FetchQuota(fetchCtx)
	if err != nil {
		r.logger.Warn("quota fetch failed",
			"session", session.SessionID(),
			"model", session.GetModel(),
			"error", err,
		)
		return
	}
	if info == nil {
		return
	}

	events <- event.QuotaUpdated{
		Provider: session.GetModel(),
		Balance:  info.Balance,
		Currency: info.Currency,
	}
}

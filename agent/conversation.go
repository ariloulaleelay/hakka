package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// eventSender is an optional sink for EngineEvents. When set on a
// Conversation via Execute/Resume, the run loop sends typed events into
// the channel. The channel is closed when the turn finishes.
type eventSender chan<- event.EngineEvent

// stepFunc abstracts how the LLM response is obtained. It is the single
// extension point that differentiates streaming from non-streaming turns.
// Implementations receive the event channel so streaming steps can emit
// TextDelta events directly.
type stepFunc func(ctx context.Context, msgs []Message, schemas []ToolSchema, events eventSender) (*llmStepResult, error)

// TurnExecutor is the interface that both Conversation and StreamSession
// implement, allowing gateways to treat streaming and non-streaming turns
// uniformly.
type TurnExecutor interface {
	Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error)
}

// Conversation drives the LLM ↔ tool loop for a single conversational
// turn. It owns the orchestration — append user input, call the LLM,
// execute tools, loop — and knows nothing about transports, slash
// commands, or streaming.
//
// Namespace is used to isolate sessions from different gateways
// (e.g. "tcp", "ws", "tg:12345"). All store operations from this
// Conversation use this namespace, unless overridden via the context
// (see resolveNamespace). Gateways that serve multiple isolated namespaces
// (e.g. Telegram per-chat) should set the namespace in the context via
// event.ContextWithNamespace rather than mutating the field — this
// ensures thread safety when multiple chats are handled concurrently.
//
// Thread safety: Hook invocations (OnToolCall, OnToolResult, etc.) within
// a single turn are serialised so that concurrent tool goroutines do not
// interleave events on a shared writer. Different turns (even on different
// sessions) run independently and do not block each other.
//
// Conversation depends on SessionView (not the concrete Session type)
// to decouple orchestration policy from session data structures
// (Dependency Inversion Principle). Where possible, internal helpers
// accept narrower interfaces (e.g. BuildContext takes SessionHistory)
// to follow the Interface Segregation Principle.
type Conversation struct {
	Router    *Router
	Sessions  *SessionManager
	Tools     *ToolRegistry
	Config    EngineConfig
	Namespace string

	// ToolContext optionally enriches the context passed to every tool
	// invocation. Gateways set this when their tools need a transport-
	// aware ClientWriter (e.g. to talk back to Neovim). When nil, the
	// context is forwarded to handlers unchanged.
	ToolContext ToolContextDecorator
}

// NewConversation builds a Conversation. Zero-valued config fields are
// filled from DefaultEngineConfig() — the single source of truth for
// engine configuration defaults.
func NewConversation(sm *SessionManager, router *Router, tools *ToolRegistry, namespace string, cfg EngineConfig) *Conversation {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	def := DefaultEngineConfig()
	if cfg.MaxToolIterations <= 0 {
		cfg.MaxToolIterations = def.MaxToolIterations
	}
	if cfg.CompactSoftLimit <= 0 {
		cfg.CompactSoftLimit = def.CompactSoftLimit
	}
	return &Conversation{
		Router:    router,
		Sessions:  sm,
		Tools:     tools,
		Config:    cfg,
		Namespace: namespace,
	}
}

// resolveNamespace returns the namespace to use for store operations.
// It reads from context only — the single point of truth. Callers that
// do not find a namespace in the context have a programming error.
//
// Gateways serving multiple isolated namespaces set the namespace in
// the context via event.ContextWithNamespace. Entry-point methods on
// Conversation (Execute, Resume, AutoRename, BindSessionModel) promote
// the configured Namespace field into the context before any internal
// operation, so downstream code always finds it there.
func (conv *Conversation) resolveNamespace(ctx context.Context) string {
	return event.NamespaceFromContext(ctx)
}

// Execute runs one full user turn and returns an event channel. The
// channel emits typed EngineEvents and closes when the turn is complete.
// The final TurnFinished event carries the reply or error.
//
// Implements TurnExecutor.
func (conv *Conversation) Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error) {
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, conv.Namespace)
	}
	session, err := conv.prepareWithInput(ctx, sessionID, userInput)
	if err != nil {
		return nil, err
	}
	return conv.runTurnWithStep(ctx, session, conv.defaultStep(session)), nil
}

// Resume re-enters the tool loop on an existing session without
// appending a new user message. Useful after streaming detects a tool
// call and needs to drive the loop to completion.
func (conv *Conversation) Resume(ctx context.Context, sessionID string) (<-chan event.EngineEvent, error) {
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, conv.Namespace)
	}
	session, err := conv.sessionOrError(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return conv.runTurnWithStep(ctx, session, conv.defaultStep(session)), nil
}

// prepareWithInput gets or creates a session, appends a user message,
// persists it, and returns the session as a SessionView.
func (conv *Conversation) prepareWithInput(ctx context.Context, sessionID, userInput string) (SessionView, error) {
	ns := conv.resolveNamespace(ctx)
	session, err := conv.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	session.Append(Message{Role: RoleUser, Content: userInput})
	if err := conv.Sessions.Save(ctx, ns, session); err != nil {
		return nil, err
	}
	return session, nil
}

// sessionOrError gets or creates a session without modifying it.
func (conv *Conversation) sessionOrError(ctx context.Context, sessionID string) (SessionView, error) {
	return conv.Sessions.GetOrCreate(ctx, conv.resolveNamespace(ctx), sessionID)
}

// runTurnWithStep runs the tool loop in a background goroutine and returns
// an event channel. It is the shared core used by both Conversation.Execute
// and StreamSession.Execute, eliminating the duplication between the two.
//
// The step function defines how the LLM response is obtained (streaming or
// non-streaming); everything else (session persistence, auto-rename, event
// emission) is handled here once.
func (conv *Conversation) runTurnWithStep(ctx context.Context, session SessionView, step stepFunc) <-chan event.EngineEvent {
	eventCh := make(chan event.EngineEvent, 256)
	go func() {
		defer close(eventCh)
		reply, err := conv.runToolIterations(ctx, session, eventCh, step)

		if err == nil {
			if saveErr := conv.finishTurn(ctx, session); saveErr != nil {
				conv.Config.Logger.Error("failed to save session after turn",
					"session", session.SessionID(), "error", saveErr)
				err = fmt.Errorf("save session: %w", saveErr)
			} else {
				conv.autoRenameIfNeeded(ctx, session)
			}
		}

		eventCh <- event.TurnFinished{
			SessionID:   session.SessionID(),
			Reply:       reply,
			Err:         err,
			TotalTokens: session.TotalTokenUsage(),
		}
	}()
	return eventCh
}

// defaultStep creates a non-streaming step function that uses
// adapter.Complete to obtain LLM responses.
func (conv *Conversation) defaultStep(session SessionView) stepFunc {
	return func(ctx context.Context, msgs []Message, schemas []ToolSchema, _ eventSender) (*llmStepResult, error) {
		adapter := conv.adapterFor(session)
		resp, err := adapter.Complete(ctx, msgs, schemas, conv.Config.Options)
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

// SessionModel returns the name of the model currently bound to the
// session, or the registry default if none is set. Returns "" if no
// Router is configured.
func (conv *Conversation) SessionModel(s SessionView) string {
	if conv.Router == nil {
		return ""
	}
	return conv.Router.Current(s)
}

// BindSessionModel records the named model as the preferred one for the
// given session and persists the session.
func (conv *Conversation) BindSessionModel(ctx context.Context, sessionID, name string) (*Session, error) {
	if conv.Router == nil {
		return nil, errNoRouter
	}
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, conv.Namespace)
	}
	ns := conv.resolveNamespace(ctx)
	sess, err := conv.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	if err := conv.Router.Bind(sess, name); err != nil {
		return sess, err
	}
	if err := conv.Sessions.Save(ctx, ns, sess); err != nil {
		return sess, err
	}
	return sess, nil
}

var errNoRouter = &errMsg{"conversation: no router configured"}

type errMsg struct{ msg string }

func (e *errMsg) Error() string { return e.msg }

// adapterFor returns the LLM adapter to use for the given session.
func (conv *Conversation) adapterFor(s SessionView) LLMAdapter {
	if conv.Router == nil {
		return nil
	}
	return conv.Router.Adapter(s)
}

// BuildContext returns the full message history enriched with the CWD
// system message (if set). This is the method that produces the context
// sent to the LLM adapter. It is used by both Conversation and
// StreamSession so that CWD injection happens in one place.
//
// Depends only on SessionHistory — the narrowest interface needed.
func BuildContext(session SessionHistory) []Message {
	history := session.History()
	if cwdMsg := session.CWDMessage(); cwdMsg != nil {
		// Insert CWD message right after the system prompt (if any),
		// or at the beginning.
		insertAt := 0
		if len(history) > 0 && history[0].Role == RoleSystem {
			insertAt = 1
		}
		enriched := make([]Message, 0, len(history)+1)
		enriched = append(enriched, history[:insertAt]...)
		enriched = append(enriched, *cwdMsg)
		enriched = append(enriched, history[insertAt:]...)
		return enriched
	}
	return history
}

// llmStepResult is the result of a single LLM call within the tool loop.
type llmStepResult struct {
	content   string
	toolCalls []ToolCall
	usage     *Usage
}

// runToolIterations drives the LLM ↔ tool iteration loop using a
// caller-provided step function. This is the shared core that both
// Complete-based and Stream-based loops delegate to.
//
// The step function abstracts how the LLM response is obtained;
// runToolIterations handles everything else: hook serialisation,
// recording the assistant response, firing events, tracking usage,
// and executing tools when the model requests them.
//
// On success it returns the final assistant text. On exhaustion of
// iterations it returns ErrMaxIterations. The caller is responsible
// for persisting the session and emitting TurnFinished.
func (conv *Conversation) runToolIterations(
	ctx context.Context,
	session SessionView,
	events eventSender,
	step stepFunc,
) (string, error) {
	schemas := conv.Tools.SchemasForSession(session)
	var turnMu sync.Mutex
	hooks := conv.serialisedHooks(&turnMu)

	for i := 0; i < conv.Config.MaxToolIterations; i++ {
		var msgs []Message
		softLimit := session.GetCompactSoftLimit()
		if softLimit <= 0 {
			softLimit = conv.Config.CompactSoftLimit
		}
		if softLimit <= 0 {
			softLimit = DefaultEngineConfig().CompactSoftLimit
		}
		msgs, needCompactify := BuildCompactContext(session, softLimit)

		// When context is over soft limit, offer context_compactify alongside
		// all normal tools so the LLM can choose to compact.
		turnSchemas := schemas
		if needCompactify {
			found := false
			for _, s := range schemas {
				if s.Name == "context_compactify" {
					found = true
					break
				}
			}
			if !found {
				turnSchemas = append(schemas, contextCompactifySchema)
			}
		}

		// Log compaction state for debugging.
		estTokens := estimateTokens(msgs)
		conv.Config.Logger.Debug("tool-iteration context",
			"iteration", i,
			"session", session.SessionID(),
			"estTokens", estTokens,
			"softLimit", session.GetCompactSoftLimit(),
			"needCompactify", needCompactify,
			"schemaCount", len(turnSchemas))

		// Assert: if the last message is a compaction warning, context_compactify
		// MUST be in schemas. Otherwise the LLM is told to compact but can't.
		if needCompactify {
			hasCompactify := false
			for _, s := range turnSchemas {
				if s.Name == "context_compactify" {
					hasCompactify = true
					break
				}
			}
			if !hasCompactify {
				conv.Config.Logger.Error("BUG: needCompactify=true but context_compactify not in schemas",
					"iteration", i,
					"session", session.SessionID(),
					"schemaNames", schemaNames(turnSchemas),
				)
			}
		}

		resp, err := step(ctx, msgs, turnSchemas, events)
		if err != nil {
			// Save session before returning error — preserve work done so far.
			if saveErr := conv.finishTurn(ctx, session); saveErr != nil {
				conv.Config.Logger.Error("failed to save session on error",
					"session", session.SessionID(), "error", saveErr)
			}
			return "", conv.notifyError(session.SessionID(), err, hooks)
		}

		msg := Message{Role: RoleAssistant, Content: resp.content, ToolCalls: resp.toolCalls, Usage: resp.usage}
		session.Append(msg)

		llmResp := &LLMResponse{Message: msg, Usage: resp.usage}
		hooks.FireLLMResponse(session.SessionID(), llmResp)
		if resp.usage != nil {
			session.AddTokenUsage(resp.usage.TotalTokens)
			conv.sendEvent(events, event.UsageReported{
				SessionID: session.SessionID(),
				Usage: event.UsageInfo{
					PromptTokens:     resp.usage.PromptTokens,
					CompletionTokens: resp.usage.CompletionTokens,
					TotalTokens:      resp.usage.TotalTokens,
				},
			})
		}

		if len(resp.toolCalls) == 0 {
			return resp.content, nil
		}

		if needCompactify {
			names := make([]string, len(resp.toolCalls))
			for j, tc := range resp.toolCalls {
				names[j] = tc.Name
			}
			conv.Config.Logger.Debug("tool-iteration compactify-needed-but-llm-called",
				"iteration", i,
				"session", session.SessionID(),
				"calledTools", names,
			)
		}

		conv.executeToolCalls(ctx, session, resp.toolCalls, hooks, events)

		// Save after every completed tool round so progress is not lost.
		if saveErr := conv.finishTurn(ctx, session); saveErr != nil {
			conv.Config.Logger.Error("failed to save session mid-turn",
				"session", session.SessionID(), "iteration", i, "error", saveErr)
			return "", fmt.Errorf("save session: %w", saveErr)
		}
	}

	return "", ErrMaxIterations
}

// schemaNames returns the names of the given schemas for logging.
func schemaNames(schemas []ToolSchema) []string {
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	return names
}

// sendEvent sends an event to the optional event channel (non-blocking).
// If the channel is nil or full, the event is dropped silently.
func (conv *Conversation) sendEvent(events eventSender, ev event.EngineEvent) {
	if events == nil {
		return
	}
	events <- ev
}

// notifyError fires the OnError hook and returns the error so callers can
// propagate it. Falls back to logging if no hook is configured.
func (conv *Conversation) notifyError(sessionID string, err error, hooks Hooks) error {
	hooks.FireError(sessionID, err)
	if hooks.OnError == nil {
		conv.Config.Logger.Error("llm complete error", "err", err, "session", sessionID)
	}
	return err
}

// finishTurn persists the session.
func (conv *Conversation) finishTurn(ctx context.Context, session SessionView) error {
	return conv.Sessions.Save(ctx, conv.resolveNamespace(ctx), session)
}

// executeToolCalls runs every tool requested by the model, then appends
// all results to the session as tool-role messages.
func (conv *Conversation) executeToolCalls(ctx context.Context, session SessionView, calls []ToolCall, hooks Hooks, events eventSender) {
	results := conv.runToolsConcurrently(ctx, session, calls, hooks, events)
	conv.appendToolResults(session, calls, results)
}

// runToolsConcurrently fans out tool handlers across goroutines and
// collects results in order. The order matches the calls slice so the
// LLM sees results in the same sequence it requested them.
func (conv *Conversation) runToolsConcurrently(ctx context.Context, session SessionView, calls []ToolCall, hooks Hooks, events eventSender) []string {
	results := make([]string, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, toolCall ToolCall) {
			defer wg.Done()
			results[idx] = conv.runSingleTool(ctx, session, toolCall, hooks, events)
		}(i, call)
	}
	wg.Wait()
	return results
}

// runSingleTool resolves the exec snippet, fires the OnToolCall hook,
// invokes the handler, fires the OnToolResult hook, emits events, and
// returns the result string.
//
// If a ToolContextDecorator is configured on the Conversation, it is
// invoked once here to enrich the context (e.g. install a client
// writer) before the handler runs. The orchestration loop itself
// stays transport-agnostic.
func (conv *Conversation) runSingleTool(ctx context.Context, session SessionView, call ToolCall, hooks Hooks, events eventSender) string {
	// Defense-in-depth: check if the tool is enabled for this session.
	// Even if the LLM somehow calls a disabled tool (e.g. from context window),
	// we reject it here without invoking the handler.
	// context_compactify is a system meta-tool that is always allowed;
	// it only appears in schemas when the engine determines compaction is needed.
	if session != nil && !session.IsToolEnabled(call.Name) && call.Name != "context_compactify" {
		res := event.ErrorResult(errors.New("tool '" + call.Name + "' is disabled for this session"))
		hooks.FireToolCall(session.SessionID(), call)
		conv.sendEvent(events, event.ToolCallStarted{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		hooks.FireToolResult(session.SessionID(), call, res)
		conv.sendEvent(events, event.ToolCallFinished{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments, Result: res, Err: res.Err})
		return res.ForLLM()
	}

	call.ExecSnippet = conv.Tools.ExecSnippet(call.Name, call.Arguments)
	hooks.FireToolCall(session.SessionID(), call)
	conv.sendEvent(events, event.ToolCallStarted{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments, ExecSnippet: call.ExecSnippet})

	if conv.ToolContext != nil {
		if decorated := conv.ToolContext.Decorate(ctx, session.SessionID(), events); decorated != nil {
			ctx = decorated
		}
	}

	result := conv.Tools.Execute(ctx, call.Name, call.Arguments)
	hooks.FireToolResult(session.SessionID(), call, result)
	conv.sendEvent(events, event.ToolCallFinished{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments, Result: result, Err: result.Err, ExecSnippet: call.ExecSnippet})
	return result.ForLLM()
}

// appendToolResults adds tool-role messages to the session, one per call,
// preserving the order of the original calls slice.
func (conv *Conversation) appendToolResults(session SessionView, calls []ToolCall, results []string) {
	for i, call := range calls {
		session.Append(Message{
			Role:       RoleTool,
			Content:    results[i],
			ToolCallID: call.ID,
			Name:       call.Name,
		})
	}
}

// serialisedHooks wraps EngineConfig.Hooks so that every callback is
// invoked under the provided mutex. This serialises hook invocations
// across concurrent tool goroutines, preventing torn writes when hooks
// write to a shared transport connection.
func (conv *Conversation) serialisedHooks(turnMu *sync.Mutex) Hooks {
	base := conv.Config.Hooks
	return Hooks{
		OnToolCall: func(sid string, call ToolCall) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireToolCall(sid, call)
		},
		OnToolResult: func(sid string, call ToolCall, res event.ToolResult) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireToolResult(sid, call, res)
		},
		OnLLMResponse: func(sid string, r *LLMResponse) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireLLMResponse(sid, r)
		},
		OnError: func(sid string, err error) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireError(sid, err)
		},
	}
}

// ---------------------------------------------------------------------------
// Auto-rename — uses the LLM to generate a short session name after
// the user has had 2+ exchanges (user messages).
// ---------------------------------------------------------------------------

// generateName calls the LLM to suggest a session name.
// Returns the sanitised name (or "" if no name could be generated) and any
// LLM or transport error. When the adapter is unavailable it returns ("", nil)
// — callers distinguish this from an empty LLM response by checking the
// pre-condition themselves.
func (conv *Conversation) generateName(ctx context.Context, session SessionView) (string, error) {
	adapter := conv.adapterFor(session)
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

// AutoRename forces an LLM-generated session name, regardless of whether
// the session already has a name or how many messages it contains.
// Returns the generated name and a nil error on success, or ("", err) on
// failure. The session is automatically persisted if a name was generated.
//
// This is the public entry point used by the /session autorename command.
func (conv *Conversation) AutoRename(ctx context.Context, session SessionView) (string, error) {
	if conv.adapterFor(session) == nil {
		return "", fmt.Errorf("no LLM adapter available for this session")
	}

	name, err := conv.generateName(ctx, session)
	if err != nil {
		conv.Config.Logger.Warn("session auto-rename LLM call failed",
			"session", session.SessionID(), "error", err)
		return "", fmt.Errorf("LLM call failed: %w", err)
	}
	if name == "" {
		conv.Config.Logger.Warn("session auto-rename returned empty name",
			"session", session.SessionID())
		return "", fmt.Errorf("LLM returned an empty name")
	}

	session.SetSessionName(name)
	if saveErr := conv.Sessions.Save(ctx, conv.resolveNamespace(ctx), session); saveErr != nil {
		conv.Config.Logger.Warn("session auto-rename save failed",
			"session", session.SessionID(), "error", saveErr)
		return "", fmt.Errorf("save failed: %w", saveErr)
	}
	conv.Config.Logger.Info("session auto-renamed",
		"session", session.SessionID(), "name", name)
	return name, nil
}

// autoRenameIfNeeded generates a session name using the LLM if the session
// is unnamed and has had at least 2 user messages. This is the automatic
// variant — it skips silently if the pre-conditions are not met or the LLM
// call fails. For a manual forced rename use AutoRename instead.
func (conv *Conversation) autoRenameIfNeeded(ctx context.Context, session SessionView) {
	if session.SessionName() != "" {
		return // already named
	}

	// Count user messages (excluding system and tool messages).
	userCount := 0
	for _, m := range session.AllMessages() {
		if m.Role == RoleUser {
			userCount++
		}
	}
	if userCount < 2 {
		return // not enough conversation history yet
	}

	name, err := conv.generateName(ctx, session)
	if err != nil {
		conv.Config.Logger.Warn("session auto-rename failed",
			"session", session.SessionID(), "error", err)
		return
	}
	if name == "" {
		return
	}

	session.SetSessionName(name)
	if saveErr := conv.Sessions.Save(ctx, conv.resolveNamespace(ctx), session); saveErr != nil {
		conv.Config.Logger.Warn("session auto-rename save failed",
			"session", session.SessionID(), "error", saveErr)
	}
	conv.Config.Logger.Info("session auto-renamed",
		"session", session.SessionID(), "name", name)
}

// StripToolCalls filters a raw message list for human-readable consumption
// by removing internal messages and replacing assistant messages that only
// contain tool calls with a placeholder.
//
// When replaceToolsWithPlaceholder is true, tool-role messages are also
// replaced with the placeholder (as buildNamingMessages does). When false,
// they are dropped entirely (as buildAskQuestionMessages does).
func StripToolCalls(messages []Message, replaceToolsWithPlaceholder bool) []Message {
	msgs := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Internal {
			continue
		}
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
		Content: "Suggest name for this chat. One sentence.",
	})
	return filtered
}


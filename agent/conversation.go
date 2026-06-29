package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// eventSender is an optional sink for EngineEvents. When set on a
// Conversation via Execute, the run loop sends typed events into
// the channel. The channel is closed when the turn finishes.
type eventSender chan<- event.EngineEvent

// TurnExecutor is the interface that both Conversation and StreamSession
// implement, allowing gateways to treat streaming and non-streaming turns
// uniformly.
type TurnExecutor interface {
	Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error)
}

// Conversation drives the LLM ↔ tool loop for a single conversational
// turn. It is the public-facing facade that gateways interact with;
// the heavy lifting (iteration loop, compaction, tool execution) is
// delegated to turnRunner.
//
// Conversation owns:
//   - Session lifecycle (get/create, append user input, persist)
//   - Turn lifecycle (background goroutine, event channel, finalisation)
//   - Auto-rename (LLM-generated session names)
//   - Model binding (per-session model selection)
//   - Tool context decoration (transport-aware ClientWriter)
//
// It delegates to turnRunner for:
//   - The LLM ↔ tool iteration loop
//   - Default (non-streaming) step function creation
//   - Compaction limit resolution and schema augmentation
//
// Namespace is used to isolate sessions from different gateways
// (e.g. "default", "tg:12345"). All store operations from this
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

	// turnRunner is the extracted orchestration core that handles the
	// LLM ↔ tool iteration loop. Created by NewConversation and
	// rebuilt by SetToolContext.
	turnRunner *turnRunner

	// toolExec is the extracted service that handles tool execution,
	// concurrent fan-out, hook serialisation, and event emission for
	// tool calls. It is populated by NewConversation and rebuilt by
	// SetToolContext.
	toolExec *toolExecutor
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
	conv := &Conversation{
		Router:    router,
		Sessions:  sm,
		Tools:     tools,
		Config:    cfg,
		Namespace: namespace,
		toolExec:  newToolExecutor(tools, nil, cfg.Hooks),
	}
	conv.turnRunner = newTurnRunner(tools, conv.toolExec, cfg, router, cfg.Logger)
	return conv
}

// EnsureDefaultModel sets the registry default model on the session if
// none is set yet. This ensures every session has a meaningful model
// from creation, so GetModel() never returns "".
func (conv *Conversation) EnsureDefaultModel(ctx context.Context, session SessionView) {
	if session.GetModel() != "" {
		return
	}
	if conv.Router == nil {
		return
	}
	defaultModel := conv.Router.Current(session)
	if defaultModel == "" {
		return
	}
	session.SetModel(defaultModel)
}

// SetToolContext installs a transport-aware decorator used to enrich the
// context passed to every tool invocation (e.g. to install a ClientWriter
// for Neovim communication). It rebuilds both toolExec and turnRunner
// so the decorator takes effect immediately.
//
// Gateways that need a ToolContextDecorator should call this after
// NewConversation instead of setting the exported field directly.
func (conv *Conversation) SetToolContext(tc ToolContextDecorator) {
	conv.ToolContext = tc
	conv.toolExec = newToolExecutor(conv.Tools, tc, conv.Config.Hooks)
	conv.turnRunner = newTurnRunner(conv.Tools, conv.toolExec, conv.Config, conv.Router, conv.Config.Logger)
}

// resolveNamespace returns the namespace to use for store operations.
// It reads from context only — the single point of truth. Callers that
// do not find a namespace in the context have a programming error.
//
// Gateways serving multiple isolated namespaces set the namespace in
// the context via event.ContextWithNamespace. Entry-point methods on
// Conversation (Execute, AutoRename, BindSessionModel) promote
// the configured Namespace field into the context before any internal
// operation, so downstream code always finds it there.
func (conv *Conversation) resolveNamespace(ctx context.Context) string {
	return event.NamespaceFromContext(ctx)
}

// Execute runs one full user turn and returns an event channel. The
// channel emits typed EngineEvents and closes when the turn is complete.
// The final TurnFinished event carries the reply or error.
//
// When userInput is empty, no user message is appended to the session —
// the LLM picks up from the existing context. This is used by /continue
// after network crashes.
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
	return conv.runTurnWithStep(ctx, session, conv.turnRunner.defaultStep(session)), nil
}

// prepareWithInput gets or creates a session. If userInput is non-empty,
// it appends a user message and persists. Returns the session as SessionView.
func (conv *Conversation) prepareWithInput(ctx context.Context, sessionID, userInput string) (SessionView, error) {
	ns := conv.resolveNamespace(ctx)
	session, err := conv.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	conv.EnsureDefaultModel(ctx, session)
	if userInput != "" {
		session.Append(Message{Role: RoleUser, Content: userInput})
		if err := conv.Sessions.Save(ctx, ns, session); err != nil {
			return nil, err
		}
	}
	return session, nil
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

		// saveFn is the persistence callback that turnRunner calls after
		// every iteration round. We provide it here so turnRunner does
		// not need to know about session persistence.
		saveFn := func(ctx context.Context, session SessionView) error {
			return conv.finishTurn(ctx, session)
		}

		reply, err := conv.turnRunner.run(ctx, session, eventCh, step, saveFn)

		if err == nil {
			if saveErr := saveFn(ctx, session); saveErr != nil {
				conv.Config.Logger.Error("failed to save session after turn",
					"session", session.SessionID(), "error", saveErr)
				err = fmt.Errorf("save session: %w", saveErr)
			} else {
				oldName := session.SessionName()
				conv.autoRenameIfNeeded(ctx, session)
				newName := session.SessionName()
				if newName != "" && newName != oldName {
					eventCh <- event.SessionRenamed{
						SessionID: session.SessionID(),
						OldName:   oldName,
						NewName:   newName,
					}
				}
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

// BuildContext returns the full message history enriched with the CWD
// system message (if set) and the tool list system message (if a
// ToolRegistry is available via the context or conversation).
//
// This is the method that produces the context sent to the LLM adapter.
// It is used by both Conversation and StreamSession so that CWD injection
// and tool listing happen in one place.
//
// Depends only on SessionHistory — the narrowest interface needed.
func BuildContext(session SessionHistory) []Message {
	return buildContext(session, nil)
}

// buildContext is the internal implementation that optionally receives a
// ToolRegistry reference. When tools is non-nil, a tool list system message
// is injected.
func buildContext(session SessionHistory, tools *ToolRegistry) []Message {
	history := session.History()
	if cwdMsg := session.CWDMessage(); cwdMsg != nil {
		insertAt := 0
		if len(history) > 0 && history[0].Role == RoleSystem {
			insertAt = 1
		}
		enriched := make([]Message, 0, len(history)+2)
		enriched = append(enriched, history[:insertAt]...)
		enriched = append(enriched, *cwdMsg)
		// Inject tool list after CWD if we have a registry
		if tools != nil {
			if auth, ok := session.(SessionToolAuth); ok {
				if toolMsg := BuildToolListMessage(tools, auth); toolMsg != "" {
					enriched = append(enriched, Message{Role: RoleSystem, Content: toolMsg})
				}
			}
		}
		enriched = append(enriched, history[insertAt:]...)
		return enriched
	}
	return history
}

// finishTurn persists the session.
func (conv *Conversation) finishTurn(ctx context.Context, session SessionView) error {
	return conv.Sessions.Save(ctx, conv.resolveNamespace(ctx), session)
}

// ---------------------------------------------------------------------------
// Auto-rename — uses the LLM to generate a short session name after
// the user has had 2+ exchanges (user messages).
// ---------------------------------------------------------------------------

// AutoRename forces an LLM-generated session name, regardless of whether
// the session already has a name or how many messages it contains.
// Returns the generated name and a nil error on success, or ("", err) on
// failure. The session is automatically persisted if a name was generated.
//
// This is the public entry point used by the /session autorename command.
func (conv *Conversation) AutoRename(ctx context.Context, session SessionView) (string, error) {
	adapter := conv.Router.Adapter(session)
	if adapter == nil {
		return "", fmt.Errorf("no LLM adapter available for this session")
	}

	name, err := generateName(ctx, adapter, session, conv.Config.Logger)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}
	if name == "" {
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

	userCount := 0
	for _, m := range session.AllMessages() {
		if m.Role == RoleUser {
			userCount++
		}
	}
	if userCount < 2 {
		return // not enough conversation history yet
	}

	adapter := conv.Router.Adapter(session)
	if adapter == nil {
		return
	}

	name, err := generateName(ctx, adapter, session, conv.Config.Logger)
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

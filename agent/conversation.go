package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// eventSender is an optional sink for EngineEvents. When set on a
// Conversation via Execute, the run loop sends typed events into
// the channel. The channel is closed when the turn finishes.
type eventSender chan<- event.EngineEvent

// TurnExecutor is the interface that Conversation implements, allowing
// gateways to run turns uniformly.
type TurnExecutor interface {
	Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error)
}

// Conversation is the public facade that gateways use to run turns and
// manage sessions. It delegates the LLM ↔ tool iteration loop to
// turnRunner and tool execution to toolExecutor.
//
// See AGENT.md §"Brief Architecture" for the full component split.
type Conversation struct {
	router    *Router
	sessions  *SessionManager
	tools     *ToolRegistry
	config    EngineConfig
	namespace string

	// toolContext optionally enriches the context passed to every tool
	// invocation. Gateways set this when their tools need a transport-
	// aware ClientWriter (e.g. to talk back to Neovim). When nil, the
	// context is forwarded to handlers unchanged.
	toolContext ToolContextDecorator

	// mu guards the mutable fields below (toolExec, turnRunner) when
	// SetToolContext is called concurrently with Execute. In practice
	// SetToolContext is called once at startup, so contention is zero.
	mu sync.Mutex

	// turnRunner is the extracted orchestration core that handles the
	// LLM ↔ tool iteration loop. Created by NewConversation and
	// rebuilt by SetToolContext.
	turnRunner *turnRunner

	// toolExec is the extracted service that handles tool execution,
	// concurrent fan-out, hook serialisation, and event emission for
	// tool calls. It is populated by NewConversation and rebuilt by
	// SetToolContext.
	toolExec *toolExecutor

	// skills is the global skill registry used to resolve loaded skill
	// names to content for context injection.
	skills *SkillRegistry
}

func (conv *Conversation) Sessions() *SessionManager { return conv.sessions }
func (conv *Conversation) Router() *Router             { return conv.router }
func (conv *Conversation) Config() EngineConfig        { return conv.config }
func (conv *Conversation) Namespace() string           { return conv.namespace }
func (conv *Conversation) Tools() *ToolRegistry        { return conv.tools }

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
		router:    router,
		sessions:  sm,
		tools:     tools,
		config:    cfg,
		namespace: namespace,
		toolExec:  newToolExecutor(tools, nil, cfg.Hooks),
		skills:    nil,
	}
	conv.turnRunner = newTurnRunner(tools, conv.toolExec, cfg, router, cfg.Logger, nil)
	return conv
}

// EnsureDefaultModel sets the registry default model on the session if
// none is set yet, and ensures the compact soft limit is populated.
//
// For new sessions (no model), we set both model and limit.
// For existing sessions (model already set, e.g. from DB), we ensure
// the compact soft limit is populated for compatibility with sessions
// created before this feature existed.
func (conv *Conversation) EnsureDefaultModel(ctx context.Context, session SessionView) {
	if session.GetModel() == "" {
		if conv.router == nil {
			return
		}
		defaultModel := conv.router.Current(session)
		if defaultModel == "" {
			return
		}
		session.SetModel(defaultModel)
	}

	// Always ensure the compact soft limit is set, even for existing
	// sessions that may have been created before this feature.
	if conv.router != nil && session.GetCompactSoftLimit() <= 0 {
		if profile, ok := conv.router.GetProfile(session); ok && profile.CompactSoftLimit > 0 {
			session.SetCompactSoftLimit(profile.CompactSoftLimit)
		} else {
			session.SetCompactSoftLimit(conv.config.CompactSoftLimit)
		}
	}
}

// ensureCompactSoftLimit sets the compact soft limit from the session's
// current model. Used when the model binding changes (e.g. BindSessionModel).
// It always overrides the current value, unlike EnsureDefaultModel which
// only sets it when <= 0.
func (conv *Conversation) ensureCompactSoftLimit(session SessionView) {
	if conv.router == nil {
		return
	}
	if profile, ok := conv.router.GetProfile(session); ok && profile.CompactSoftLimit > 0 {
		session.SetCompactSoftLimit(profile.CompactSoftLimit)
	} else {
		session.SetCompactSoftLimit(conv.config.CompactSoftLimit)
	}
}

// SetToolContext installs a transport-aware decorator used to enrich the
// context passed to every tool invocation (e.g. to install a ClientWriter
// for Neovim communication). It rebuilds both toolExec and turnRunner
// so the decorator takes effect immediately.
//
// Thread safety: SetToolContext acquires the internal mutex so it is
// safe to call concurrently with any accessor method. However, calling
// it while a turn is running (Execute in-flight) will race on the
// toolExec and turnRunner pointers. In practice gateways call this
// once at startup before any Execute call.
//
// Gateways that need a ToolContextDecorator should call this after
// NewConversation instead of mutating internal fields directly.
func (conv *Conversation) SetToolContext(tc ToolContextDecorator) {
	conv.mu.Lock()
	defer conv.mu.Unlock()
	conv.toolContext = tc
	conv.toolExec = newToolExecutor(conv.tools, tc, conv.config.Hooks)
	conv.turnRunner = newTurnRunner(conv.tools, conv.toolExec, conv.config, conv.router, conv.config.Logger, conv.skills)
}

// SetSkills installs a skill registry that the conversation uses to
// resolve loaded skill names to content for context injection. Must be
// called before Execute, not safe for concurrent use with running turns.
func (conv *Conversation) SetSkills(skills *SkillRegistry) {
	conv.mu.Lock()
	defer conv.mu.Unlock()
	conv.skills = skills
	conv.turnRunner = newTurnRunner(conv.tools, conv.toolExec, conv.config, conv.router, conv.config.Logger, skills)
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
// The engine always provides an onDelta callback to the adapter; the
// adapter decides whether to stream progress deltas or ignore it.
func (conv *Conversation) Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error) {
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, conv.namespace)
	}
	saveFn := func(ctx context.Context, session SessionView) error {
		return conv.sessions.Save(ctx, conv.resolveNamespace(ctx), session)
	}
	renameFn := func(ctx context.Context, s SessionView, events eventSender) {
		oldName := s.SessionName()
		conv.autoRenameIfNeeded(ctx, s)
		newName := s.SessionName()
		if newName != "" && newName != oldName {
			events <- event.SessionRenamed{
				SessionID: s.SessionID(),
				OldName:   oldName,
				NewName:   newName,
			}
		}
	}
	session, err := conv.prepareWithInput(ctx, sessionID, userInput)
	if err != nil {
		return nil, err
	}
	return conv.turnRunner.executeTurn(ctx, session, saveFn, renameFn), nil
}

// prepareWithInput gets or creates a session. If userInput is non-empty,
// it appends a user message and persists. Returns the session as SessionView.
func (conv *Conversation) prepareWithInput(ctx context.Context, sessionID, userInput string) (SessionView, error) {
	ns := conv.resolveNamespace(ctx)
	session, err := conv.sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	conv.EnsureDefaultModel(ctx, session)
	if userInput != "" {
		session.Append(Message{Role: RoleUser, Content: userInput, Timestamp: nowMillis()})
		if err := conv.sessions.Save(ctx, ns, session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

// SessionModel returns the model name, the registry default if none is set,
// or "" when no Router is configured.
func (conv *Conversation) SessionModel(s SessionView) string {
	if conv.router == nil {
		return ""
	}
	return conv.router.Current(s)
}

// BindSessionModel records the named model as the preferred one for the
// given session, updates the compact soft limit from the new model's
// profile, and persists the session.
func (conv *Conversation) BindSessionModel(ctx context.Context, sessionID, name string) (*Session, error) {
	if conv.router == nil {
		return nil, errNoRouter
	}
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, conv.namespace)
	}
	ns := conv.resolveNamespace(ctx)
	sess, err := conv.sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	if err := conv.router.Bind(sess, name); err != nil {
		return sess, err
	}
	// Update compact soft limit from the new model's profile
	conv.ensureCompactSoftLimit(sess)
	if err := conv.sessions.Save(ctx, ns, sess); err != nil {
		return sess, err
	}
	return sess, nil
}

var errNoRouter = &errMsg{"conversation: no router configured"}

type errMsg struct{ msg string }

func (e *errMsg) Error() string { return e.msg }

// Is enables errors.Is to match errMsg sentinels by message content.
func (e *errMsg) Is(target error) bool {
	t, ok := target.(*errMsg)
	if !ok {
		return false
	}
	return e.msg == t.msg
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
	adapter := conv.router.Adapter(session)
	if adapter == nil {
		return "", fmt.Errorf("no LLM adapter available for this session")
	}

	name, err := generateName(ctx, adapter, session, conv.config.Logger)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}
	if name == "" {
		return "", fmt.Errorf("LLM returned an empty name")
	}

	session.SetSessionName(name)
	if saveErr := conv.sessions.Save(ctx, conv.resolveNamespace(ctx), session); saveErr != nil {
		conv.config.Logger.Warn("session auto-rename save failed",
			"session", session.SessionID(), "error", saveErr)
		return "", fmt.Errorf("save failed: %w", saveErr)
	}
	conv.config.Logger.Info("session auto-renamed",
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
	for _, m := range session.Messages() {
		if m.Role == RoleUser {
			userCount++
		}
	}
	if userCount < 2 {
		return // not enough conversation history yet
	}

	adapter := conv.router.Adapter(session)
	if adapter == nil {
		return
	}

	name, err := generateName(ctx, adapter, session, conv.config.Logger)
	if err != nil {
		conv.config.Logger.Warn("session auto-rename failed",
			"session", session.SessionID(), "error", err)
		return
	}
	if name == "" {
		return
	}

	session.SetSessionName(name)
	if saveErr := conv.sessions.Save(ctx, conv.resolveNamespace(ctx), session); saveErr != nil {
		conv.config.Logger.Warn("session auto-rename save failed",
			"session", session.SessionID(), "error", saveErr)
	}
	conv.config.Logger.Info("session auto-renamed",
		"session", session.SessionID(), "name", name)
}

package gateways

import (
	"context"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// TurnHandler — shared orchestration logic for all transport gateways.
//
// Gateways perform transport-specific frame parsing and then delegate to
// TurnHandler for all shared logic: command handling, session/CWD
// resolution, chat/stream turns, and event-to-frame conversion.
//
// Turn lifecycles are managed by the namespace hub's shared turnTracker.
// The hub broadcasts all engine events (delta, tool, usage, done, req,
// renamed) to every connected client in the namespace. Clients filter by
// session_id on the receiving side.
//
// All text-based slash command handling is performed by clients.
// The server only accepts structured JSON commands via the "command" field.
// ---------------------------------------------------------------------------

// TurnHandler encapsulates the turn orchestration logic shared by all
// gateways.
type TurnHandler struct {
	Conv      *agent.Conversation
	Cmd       *commands.CommandProcessor
	Namespace string

	hub *NamespaceHub
}

// NewTurnHandler builds a TurnHandler from the standard engine components.
func NewTurnHandler(conv *agent.Conversation, cmd *commands.CommandProcessor, namespace string) *TurnHandler {
	h := &TurnHandler{
		Conv:      conv,
		Cmd:       cmd,
		Namespace: namespace,
		hub:       NewNamespaceHub(namespace),
	}
	if cmd != nil {
		cmd.SetSessionActiveChecker(func(sessionID string) bool {
			return h.Turns().Get(sessionID) != nil
		})
	}
	return h
}

// Hub returns the namespace hub. Used by gateways to register/unregister
// transport connections.
func (h *TurnHandler) Hub() *NamespaceHub { return h.hub }

// SetHub replaces the namespace hub with a shared instance. This must be
// called before any turns are started. Used at wiring time to share a
// single hub across multiple gateway transports for the same namespace
// (e.g. standalone WS + webfront).
func (h *TurnHandler) SetHub(hub *NamespaceHub) {
	h.hub = hub
	if h.Cmd != nil {
		h.Cmd.SetSessionActiveChecker(func(sessionID string) bool {
			return hub.Turns().Get(sessionID) != nil
		})
	}
}

// Turns returns the hub's shared turn tracker.
func (h *TurnHandler) Turns() *turnTracker { return h.hub.Turns() }

// CancelSession cancels an in-flight request for the given session.
// Returns true if a running request was found and cancelled, false if
// there was no active request for that session.
func (h *TurnHandler) CancelSession(sessionID string) bool {
	return h.hub.Turns().Cancel(sessionID)
}

// HandleRequest processes a single user request.
//
// The request type (chat vs command) is determined by the "type" field:
//   - "cmd": structured JSON command (no LLM invocation)
//   - "chat": chat/stream turn (LLM invocation)
//   - "": legacy fallback: if Command is set, treat as cmd; otherwise chat
//
// The writer is automatically subscribed to the namespace hub so it
// receives all broadcast events.
func (h *TurnHandler) HandleRequest(ctx context.Context, req FrameRequest, w frameWriter, responseReader *InProcessResponseReader) {
	// Ensure the writer is registered with the hub.
	h.hub.Subscribe(w)

	// Determine if this is a command or chat request.
	isCmd := req.Type == "cmd"
	if req.Type == "" {
		// Legacy: command field presence determines type.
		isCmd = req.Command != nil && h.Cmd != nil
	}

	if isCmd {
		h.handleJSONCommand(ctx, req, w, responseReader)
		return
	}

	// Chat request. Use the session ID from the request as-is;
	// CWD is set via the cwd_set command, not inline on chat frames.
	sessionID := req.SessionID

	// Check for an active turn for this session.
	if active := h.Turns().Get(sessionID); active != nil {
		if req.Input == "" {
			// Empty input on active turn — nothing to do.
			return
		}
		// Non-empty input while turn is active: cancel old, start new.
		h.hub.Turns().Cancel(sessionID)
	}

	// Start a new turn.
	h.startNewTurn(ctx, w, responseReader, req, sessionID, req.Input)
}

// startNewTurn creates a new turn context (from context.Background so
// client disconnect doesn't cancel it), enriches it, launches the
// executor, and registers the turn with the tracker.
func (h *TurnHandler) startNewTurn(ctx context.Context, w frameWriter, responseReader *InProcessResponseReader, req FrameRequest, sessionID, input string) {
	// The turn context is derived from context.Background() so the turn
	// survives client disconnection. Only explicit cancellation
	// (CancelSession) or natural completion will stop it.
	turnCtx, turnCancel := context.WithCancel(context.Background())

	// Enrich context with client communication channels, namespace, and CWD for tools.
	turnCtx = event.ContextWithNamespace(turnCtx, h.Namespace)
	turnCtx = clientCtx(turnCtx, w, responseReader, sessionID)
	turnCtx = enrichCtxWithCWD(turnCtx, h.Conv, sessionID)

	h.handleWithEngine(turnCtx, w, sessionID, input, turnCancel)
}

// handleJSONCommand processes a structured JSON command request.
func (h *TurnHandler) handleJSONCommand(ctx context.Context, req FrameRequest, w frameWriter, responseReader *InProcessResponseReader) {
	cmdReq := req.Command

	// Use the session ID from the request as-is.
	// CWD is set via the cwd_set command, not inline on command frames.
	sessionID := req.SessionID

	reqCtx := event.ContextWithNamespace(ctx, h.Namespace)
	reqCtx = clientCtx(reqCtx, w, responseReader, sessionID)
	reqCtx = enrichCtxWithCWD(reqCtx, h.Conv, sessionID)

	cmdRes := h.Cmd.ExecuteJSON(reqCtx, sessionID, cmdReq.Cmd, cmdReq.Params)

	// Check in_flight status for get_session so writeCommandResult can
	// decide whether to append a "done" event to the events replay.
	if cmdReq.Cmd == "get_session" && cmdRes.Session != nil {
		cmdRes.InFlight = h.Turns().Get(cmdRes.Session.SessionID()) != nil
	}

	// Continue command: trigger the LLM without adding a user message.
	// Skip writeCommandResult — the turn's own done frame arrives via
	// hub broadcast and serves as the response.
	if cmdRes.Action == commands.ActionContinue {
		if sessionID != "" {
			h.startNewTurn(ctx, w, responseReader, req, sessionID, "")
			return
		}
		// No session: fall through to writeCommandResult which will
		// emit a done frame (ack / error indicator).
	}

	writeCommandResult(w, cmdRes)

	// Broadcast session lifecycle events to all clients.
	if cmdRes.SessionEvent != nil {
		fr := sessionEventFrame(*cmdRes.SessionEvent)
		h.hub.BroadcastExcept(fr, w) // requester already got direct response
	}
}

// handleWithEngine runs a turn using the Conversation, registers it
// with the hub's turn tracker, and returns immediately. The turn's
// events are broadcast to all hub subscribers by the tracker's
// fan-out goroutine.
func (h *TurnHandler) handleWithEngine(ctx context.Context, w frameWriter, sessionID, input string, cancel context.CancelFunc) {
	eventCh, err := h.Conv.Execute(ctx, sessionID, input)
	if err != nil {
		_ = w.Write(FrameResponse{Type: "error", SessionID: sessionID, Error: err.Error()})
		return
	}

	// Register the turn with the hub's shared tracker. The fan-out
	// goroutine broadcasts all events to the hub; no per-client
	// subscription needed.
	_ = h.hub.Turns().Start(sessionID, eventCh, cancel)
}

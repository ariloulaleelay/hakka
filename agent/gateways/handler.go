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
// Turn lifecycles are managed by turnTracker, which decouples the turn
// from any single client connection:
//   - When a client disconnects mid-turn, the turn continues running in
//     the background. The subscriber is silently removed from the fan-out.
//   - A reconnecting client (or a client that fetches an in-flight session
//     via get_session) can subscribe to the running turn and receive all
//     subsequent events.
//   - Explicit cancellation via CancelSession still works.
//
// Note: all text-based slash command handling is performed by clients.
// The server only accepts structured JSON commands via the "command" field.
// ---------------------------------------------------------------------------

// TurnHandler encapsulates the turn orchestration logic shared by all
// gateways.
type TurnHandler struct {
	Conv      *agent.Conversation
	Streamer  *agent.StreamSession
	Cmd       *commands.CommandProcessor
	Namespace string

	// turns manages active turn lifecycles per session. It replaces the
	// old cancelFuncs sync.Map — the fan-out goroutine in each activeTurn
	// distributes events to all connected clients for that session.
	turns *turnTracker
}

// NewTurnHandler builds a TurnHandler from the standard engine components.
// It also wires the session active checker on the command processor so that
// session_list can report in-flight sessions via the turn tracker.
func NewTurnHandler(conv *agent.Conversation, streamer *agent.StreamSession, cmd *commands.CommandProcessor, namespace string) *TurnHandler {
	tt := &turnTracker{}
	if cmd != nil {
		cmd.SetSessionActiveChecker(func(sessionID string) bool {
			return tt.Get(sessionID) != nil
		})
	}
	return &TurnHandler{
		Conv:      conv,
		Streamer:  streamer,
		Cmd:       cmd,
		Namespace: namespace,
		turns:     tt,
	}
}

// Turns returns the underlying turnTracker, used by gateways for
// session discovery (in_flight status) and subscription management.
func (h *TurnHandler) Turns() *turnTracker { return h.turns }

// CancelSession cancels an in-flight request for the given session.
// Returns true if a running request was found and cancelled, false if
// there was no active request for that session.
func (h *TurnHandler) CancelSession(sessionID string) bool {
	return h.turns.Cancel(sessionID)
}

// executor returns the TurnExecutor to use for the given request.
// Streaming requests use StreamSession; non-streaming use Conversation.
func (h *TurnHandler) executor(stream bool) agent.TurnExecutor {
	if stream {
		return h.Streamer
	}
	return h.Conv
}

// HandleRequest processes a single user request.
//
// The request type (chat vs command) is determined by the "type" field:
//   - "cmd": structured JSON command (no LLM invocation)
//   - "chat": chat/stream turn (LLM invocation)
//   - "": legacy fallback: if Command is set, treat as cmd; otherwise chat
//
// If there is already an active turn for this session (e.g. from a
// previous client that disconnected), the new writer is subscribed to
// the existing turn instead of starting a new one.
func (h *TurnHandler) HandleRequest(ctx context.Context, req FrameRequest, w frameWriter, responseReader *InProcessResponseReader) {
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
	if active := h.turns.Get(sessionID); active != nil {
		if req.Input == "" {
			// Empty input means reconnection subscription.
			active.ReplaceSubscriber(ctx, w)
		} else {
			// Non-empty input while turn is active: cancel old, start new.
			h.turns.Cancel(sessionID)
			h.startNewTurn(ctx, w, responseReader, req, sessionID, req.Input)
		}
		return
	}

	// No active turn — start a new one.
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

	h.handleWithEngine(turnCtx, w, sessionID, input, h.executor(req.Stream), turnCancel)
}

// handleJSONCommand processes a structured JSON command request.
//
// Special case: when the command is "get_session" and the target
// session has an active turn, the new writer is subscribed to the
// running turn after the command response is sent. This allows a
// reconnecting web client to send get_session and immediately
// start receiving streaming events.
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
		cmdRes.InFlight = h.turns.Get(cmdRes.Session.SessionID()) != nil
	}

	writeCommandResult(w, cmdRes)

	// After get_session, subscribe the new writer to the target
	// session's active turn if one exists.
	if cmdReq.Cmd == "get_session" && cmdRes.Session != nil {
		targetID := cmdRes.Session.SessionID()
		if active := h.turns.Get(targetID); active != nil {
			active.ReplaceSubscriber(ctx, w)
		}
	}

	// Continue command: trigger the LLM without adding a user message.
	// The command processor returns ActionContinue but doesn't invoke
	// the LLM — that's the handler's responsibility.
	// Only start a turn if we have a session to continue.
	//
	// For /continue, use the session's streaming preference rather than
	// the request's (command frames have no stream field, always false).
	if cmdRes.Action == commands.ActionContinue && sessionID != "" {
		if session, _, err := h.Conv.Sessions().Get(context.Background(), h.Namespace, sessionID); err == nil {
			req.Stream = session.Read().Streaming
		}
		h.startNewTurn(ctx, w, responseReader, req, sessionID, "")
	}
}

// handleWithEngine runs a turn using the given executor, registers it
// with the turn tracker, and subscribes the calling writer.
//
// Unlike the old behaviour (which cancelled the turn on write failure),
// this method uses the turnTracker's fan-out. On write failure the
// subscriber is silently removed, but the turn continues for any
// remaining subscribers. Reconnecting clients can subscribe later.
func (h *TurnHandler) handleWithEngine(ctx context.Context, w frameWriter, sessionID, input string, exec agent.TurnExecutor, cancel context.CancelFunc) {
	eventCh, err := exec.Execute(ctx, sessionID, input)
	if err != nil {
		_ = w.Write(FrameResponse{Type: "error", SessionID: sessionID, Error: err.Error()})
		return
	}

	// Register the turn with the tracker. The fan-out goroutine will
	// distribute events to all subscribers, including this one.
	at := h.turns.Start(sessionID, eventCh, cancel)

	// Subscribe this writer and block until the turn finishes, the
	// writer fails (client disconnects), or the context is cancelled.
	// On write failure we do NOT cancel the turn — other subscribers
	// may still be connected, or a client may reconnect later.
	at.Subscribe(ctx, w)
}

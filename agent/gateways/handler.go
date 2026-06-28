package gateways

import (
	"context"
	"log/slog"
	"sync"

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
// Both streaming and non-streaming turns use the TurnExecutor interface,
// which is implemented by both agent.Conversation and agent.StreamSession.
// The StreamSession transparently handles tool-call fallback internally,
// so both paths produce the same uniform event channel.
// ---------------------------------------------------------------------------

// TurnHandler encapsulates the turn orchestration logic shared by all
// gateways.
type TurnHandler struct {
	Conv      *agent.Conversation
	Streamer  *agent.StreamSession
	Cmd       *commands.CommandProcessor
	Namespace string

	// cancelFuncs maps sessionID -> context.CancelFunc for active
	// in-flight requests. CancelSession can be called from any
	// connection to abort a running request for a given session.
	cancelFuncs sync.Map
}

// NewTurnHandler builds a TurnHandler from the standard engine components.
func NewTurnHandler(conv *agent.Conversation, streamer *agent.StreamSession, cmd *commands.CommandProcessor, namespace string) *TurnHandler {
	return &TurnHandler{Conv: conv, Streamer: streamer, Cmd: cmd, Namespace: namespace}
}

// CancelSession cancels an in-flight request for the given session.
// Returns true if a running request was found and cancelled, false if
// there was no active request for that session.
func (h *TurnHandler) CancelSession(sessionID string) bool {
	val, ok := h.cancelFuncs.LoadAndDelete(sessionID)
	if !ok {
		return false
	}
	cancel, _ := val.(context.CancelFunc)
	if cancel == nil {
		return false
	}
	cancel()
	return true
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
// If the request has a Command field, it is handled as a structured JSON
// command (no LLM invocation). Otherwise, it is treated as a chat/stream
// request (with optional text-based slash command interception for
// non-JSON clients like Telegram).
func (h *TurnHandler) HandleRequest(ctx context.Context, req FrameRequest, w frameWriter, responseReader *InProcessResponseReader) {
	// JSON command: structured command from a JSON-capable client.
	if req.Command != nil && h.Cmd != nil {
		h.handleJSONCommand(ctx, req, w, responseReader)
		return
	}

	// Text-based input: try slash command interception (for non-JSON clients).
	input := req.Input
	if h.Cmd != nil {
		cmdRes := h.Cmd.Execute(ctx, req.SessionID, req.Input)
		if cmdRes.Handled {
			if cmdRes.Action == commands.ActionContinue {
				input = ""
			} else {
				if handled, ok := writeCommandResult(w, cmdRes, req.Stream, false); handled {
					if !ok {
						// Write failed — client disconnected, nothing more to do.
					}
					return
				}
			}
		}
	}

	// Resolve session and set CWD *before* the turn starts, so that
	// session.History() already contains the correct directory hint.
	sessionID := h.resolveSessionAndCWD(ctx, req)

	// Create a cancellable context for this session's request. The
	// cancel func is registered so it can be triggered from another
	// connection (e.g. a cancel frame).
	reqCtx, reqCancel := context.WithCancel(ctx)
	h.cancelFuncs.Store(sessionID, reqCancel)
	defer h.cancelFuncs.Delete(sessionID)

	// Enrich context with client communication channels, namespace, and CWD for tools.
	reqCtx = event.ContextWithNamespace(reqCtx, h.Namespace)
	reqCtx = clientCtx(reqCtx, w, responseReader, sessionID)
	reqCtx = enrichCtxWithCWD(reqCtx, h.Conv, req)

	h.handleWithEngine(reqCtx, w, sessionID, input, h.executor(req.Stream), reqCancel)
}

// handleJSONCommand processes a structured JSON command request.
// Does NOT create a session — if no session_id is provided, the command
// runs without a session context (appropriate for session_list, help, etc.).
func (h *TurnHandler) handleJSONCommand(ctx context.Context, req FrameRequest, w frameWriter, responseReader *InProcessResponseReader) {
	cmdReq := req.Command

	// Resolve session for commands that have an explicit session_id
	// (e.g. session_switch, session_info). When session_id is empty,
	// skip resolution — commands like session_list must never create one.
	sessionID := req.SessionID
	if sessionID != "" {
		sessionID = h.resolveSessionAndCWD(ctx, req)
	}

	reqCtx := event.ContextWithNamespace(ctx, h.Namespace)
	reqCtx = clientCtx(reqCtx, w, responseReader, sessionID)
	reqCtx = enrichCtxWithCWD(reqCtx, h.Conv, req)

	cmdRes := h.Cmd.ExecuteJSON(reqCtx, sessionID, cmdReq.Cmd, cmdReq.Params)
	writeCommandResult(w, cmdRes, false, true)
}

// handleWithEngine runs a turn using the given executor and processes
// events until completion. Both streaming and non-streaming executors
// produce the same uniform channel of typed EngineEvents.
func (h *TurnHandler) handleWithEngine(ctx context.Context, w frameWriter, sessionID, input string, exec agent.TurnExecutor, cancel context.CancelFunc) {
	eventCh, err := exec.Execute(ctx, sessionID, input)
	if err != nil {
		_ = w.Write(FrameResponse{Error: err.Error()})
		return
	}
	for evt := range eventCh {
		if !processEvent(w, evt) {
			// Write failed — client disconnected. Cancel the request context
			// so in-flight LLM calls and tool executions are aborted.
			cancel()
			return
		}
	}
}

// resolveSessionAndCWD ensures the session exists and its ClientCWD is
// set from the request. Returns the (possibly new) session ID.
func (h *TurnHandler) resolveSessionAndCWD(ctx context.Context, req FrameRequest) string {
	if req.Cwd == "" || h.Conv == nil {
		return req.SessionID
	}
	session, err := h.Conv.Sessions.GetOrCreate(ctx, h.Namespace, req.SessionID)
	if err != nil || session == nil {
		return req.SessionID
	}
	if session.Read().ClientCWD != req.Cwd {
		session.SetClientCWD(req.Cwd)
		if saveErr := h.Conv.Sessions.Save(ctx, h.Namespace, session); saveErr != nil {
			slog.Warn("failed to persist session CWD",
				"session", session.SessionID(), "cwd", req.Cwd, "error", saveErr)
		}
	}
	return session.SessionID()
}

package gateways

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
)

type frameWriter interface {
	Write(FrameResponse) error
	// ConnKey returns a unique identifier for the underlying transport
	// connection (e.g. "tcp:0x14000123400" or "ws:0x14000567800").
	// This is used by ReplaceSubscriber to deduplicate subscribers
	// from the same connection when session_switch is called while a
	// stream is in-flight.
	ConnKey() string
}

type writerFunc func(FrameResponse) error

func (fn writerFunc) Write(r FrameResponse) error { return fn(r) }
func (fn writerFunc) ConnKey() string              { return "" }

type gwClientWriter struct {
	writer    frameWriter
	sessionID string
}

func (g *gwClientWriter) WriteFrame(f event.Frame) error {
	resp := FrameResponse{
		SessionID: g.sessionID,
		Event:     f.Event,
		Data:      f.Data,
	}
	if f.ClientReq != nil {
		resp.ClientReq = f.ClientReq
	}
	return g.writer.Write(resp)
}

func (g *gwClientWriter) ConnKey() string { return g.writer.ConnKey() }

// writeCommandResult writes a CommandResult as a frame.
// When the result has structured Data (e.g. tool_list, session_list),
// it emits event: "command_result" with structured payload, regardless
// of the isJSON flag — TCP and WebSocket clients can render this.
// For text-only clients (Telegram), it falls back to Reply text.
func writeCommandResult(w frameWriter, res commands.CommandResult, stream bool, isJSON bool) (handled, ok bool) {
	if !res.Handled {
		return false, true
	}
	resp := FrameResponse{Done: true}
	if res.Session != nil {
		resp.SessionID = res.Session.SessionID()
	}

	if res.Error != nil {
		resp.Error = res.Error.Error()
		return true, w.Write(resp) == nil
	}

	// Continue action: no response frame, caller proceeds to LLM.
	if res.Action == commands.ActionContinue {
		return true, true
	}

	// Structured data path: emit as event: "command_result" whenever
	// the result has Data. This works for both JSON-capable clients
	// (Neovim, web UI) and text-based gateways (TCP, WebSocket) that
	// receive text slash commands — they all understand event frames.
	// Telegram is unaffected because it has its own command handler
	// and never calls writeCommandResult.
	if res.Data != nil {
		resp.Event = "command_result"
		resp.Cmd = res.Cmd
		json.Unmarshal(res.Data, &resp.Data)
		resp.Done = true
		return true, w.Write(resp) == nil
	}

	// For session switch/create, emit event frame.
	if res.Action == commands.ActionSessionSwitch || res.Action == commands.ActionSessionCreate {
		eventType := "session_switch"
		if res.Action == commands.ActionSessionCreate {
			eventType = "session_create"
		}
		data := map[string]any{}
		if res.Action == commands.ActionSessionSwitch && res.Session != nil {
			data["messages"] = res.Session.AllMessages()
		}
		data["session"] = sessionToMap(res.Session)
		if err := w.Write(FrameResponse{SessionID: resp.SessionID, Event: eventType, Data: data}); err != nil {
			return true, false
		}
		// For JSON clients, also send command_result with the same data (no text reply).
		if isJSON && res.Data != nil {
			resp.Event = "command_result"
			resp.Cmd = res.Cmd
			json.Unmarshal(res.Data, &resp.Data)
			resp.Done = true
			return true, w.Write(resp) == nil
		}
		// For non-JSON clients, fall through to send the text reply.
	}

	// Text fallback for non-JSON clients (Telegram, TCP).
	if stream {
		if res.Reply != "" {
			if err := w.Write(FrameResponse{SessionID: resp.SessionID, Delta: res.Reply}); err != nil {
				return true, false
			}
		}
		return true, w.Write(FrameResponse{SessionID: resp.SessionID, Done: true}) == nil
	}
	resp.Output = res.Reply
	return true, w.Write(resp) == nil
}

// sessionToMap converts a session to a map for JSON serialization.
func sessionToMap(s *agent.Session) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"id":             s.SessionID(),
		"name":           s.SessionName(),
		"short_id":       shortID(s.SessionID()),
		"message_count":  len(s.AllMessages()),
		"model":          s.GetModel(),
		"total_tokens":   s.TotalTokenUsage(),
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// sessionListToMap converts a list of sessions for JSON serialization.
func sessionListToMap(sessions []*agent.Session, currentID string, allSessions []*agent.Session) []map[string]any {
	shortIDs := make(map[string]string)
	if len(allSessions) == 0 {
		allSessions = sessions
	}
	for _, s := range allSessions {
		shortIDs[s.SessionID()] = shortID(s.SessionID())
	}

	result := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		m := sessionToMap(s)
		if s.SessionName() == "" {
			m["name"] = ""
		}
		m["short_id"] = shortIDs[s.SessionID()]
		m["current"] = s.SessionID() == currentID
		result = append(result, m)
	}
	return result
}

func processEvent(w frameWriter, evt event.EngineEvent) bool {
	switch e := evt.(type) {
	case event.ToolCallStarted:
		// Parse Arguments (JSON string) into structured args.
		var args json.RawMessage
		if e.Arguments != "" {
			args = json.RawMessage(e.Arguments)
		}
		return writeFrame(w, FrameResponse{
			SessionID:   e.SessionID,
			Event:       "tool",
			Tool:        e.Name,
			Status:      "start",
			Args:        args,
			ExecSnippet: e.ExecSnippet,
		})
	case event.ToolCallFinished:
		status := "ok"
		if e.Err != nil {
			status = "err"
		} else if e.Result.IsError() {
			status = "err"
		}
		var args json.RawMessage
		if e.Arguments != "" {
			args = json.RawMessage(e.Arguments)
		}
		return writeFrame(w, FrameResponse{
			SessionID:   e.SessionID,
			Event:       "tool",
			Tool:        e.Name,
			Status:      status,
			Args:        args,
			ExecSnippet: e.ExecSnippet,
		})
	case event.UsageReported:
		return writeFrame(w, FrameResponse{
			SessionID: e.SessionID,
			Event:     "meta",
			Data: map[string]any{
				"prompt_tokens":     e.Usage.PromptTokens,
				"completion_tokens": e.Usage.CompletionTokens,
				"total_tokens":      e.Usage.TotalTokens,
			},
		})
	case event.TextDelta:
		return writeFrame(w, FrameResponse{SessionID: e.SessionID, Delta: e.Delta})
	case event.ClientRequestSent:
		return writeFrame(w, FrameResponse{
			SessionID: e.SessionID,
			Event:     "vim_request",
			ClientReq: &event.ClientRequest{
				RequestID: e.RequestID,
				Command:   e.Command,
			},
		})
	case event.TurnFinished:
		sid := e.SessionID
		if e.Err != nil {
			if errors.Is(e.Err, context.Canceled) {
				return writeFrame(w, FrameResponse{
					SessionID: sid,
					Error:     "request cancelled",
					Done:      true,
				})
			}
			return writeFrame(w, FrameResponse{SessionID: sid, Error: e.Err.Error()})
		}
		return writeFrame(w, FrameResponse{
			SessionID: sid,
			Output:    e.Reply,
			Done:      true,
		})
	case event.SessionRenamed:
		return writeFrame(w, FrameResponse{
			SessionID: e.SessionID,
			Event:     "session_renamed",
			Data: map[string]any{
				"session_id": e.SessionID,
				"old_name":   e.OldName,
				"name":       e.NewName,
			},
		})
	}
	return true
}

func writeFrame(w frameWriter, r FrameResponse) bool {
	return w.Write(r) == nil
}

func clientCtx(ctx context.Context, w frameWriter, responseReader *InProcessResponseReader, sessionID string) context.Context {
	cw := &gwClientWriter{writer: w, sessionID: sessionID}
	ctx = event.ContextWithClient(ctx, cw, responseReader)
	return ctx
}

func enrichCtxWithCWD(ctx context.Context, conv *agent.Conversation, req FrameRequest) context.Context {
	cwd := req.Cwd
	if cwd == "" && req.SessionID != "" && conv != nil {
		session, err := conv.Sessions.GetOrCreate(ctx, conv.Namespace, req.SessionID)
		if err == nil && session != nil && session.Read().ClientCWD != "" {
			cwd = session.Read().ClientCWD
		}
	}
	if cwd != "" {
		ctx = event.ContextWithCWD(ctx, cwd)
	}
	return ctx
}

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
	// connection (e.g. "ws:0x14000567800").
	// This is used by ReplaceSubscriber to deduplicate subscribers
	// from the same connection when get_session is called while a
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

// writeCommandResult writes a CommandResult as a frame to the client.
//
// All commands are expected to carry structured Data. The function
// converts them to event:"command_result" frames. Session lifecycle
// events (get_session, session_create) additionally emit a dedicated
// event frame.
func writeCommandResult(w frameWriter, res commands.CommandResult) (handled, ok bool) {
	if !res.Handled {
		return false, true
	}

	resp := FrameResponse{Done: true}
	if res.Session != nil {
		resp.SessionID = res.Session.SessionID()
	}

	if res.Action == commands.ActionContinue {
		// Send a minimal done frame so the client knows the command
		// was handled and can proceed (e.g. Neovim waits for a response).
		return true, w.Write(FrameResponse{Done: true, SessionID: resp.SessionID}) == nil
	}

	if res.Error != nil {
		resp.Error = res.Error.Error()
		return true, w.Write(resp) == nil
	}

	// Structured data path: emit as event: "command_result".
	if res.Data != nil {
		resp.Event = "command_result"
		resp.Cmd = res.Cmd
		json.Unmarshal(res.Data, &resp.Data)
		resp.Done = true
		return true, w.Write(resp) == nil
	}

	// Session lifecycle events — emit dedicated event frame.
	if res.Action == commands.ActionGetSession || res.Action == commands.ActionSessionCreate {
		eventType := "get_session"
		if res.Action == commands.ActionSessionCreate {
			eventType = "session_create"
		}
		data := map[string]any{}
		if res.Action == commands.ActionGetSession && res.Session != nil {
			data["messages"] = res.Session.AllMessages()
		}
		data["session"] = sessionToMap(res.Session)
		return true, w.Write(FrameResponse{SessionID: resp.SessionID, Event: eventType, Data: data}) == nil
	}

	// Fallback: send reply text if available, or a minimal done frame.
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
		"total_tokens":              s.TotalTokenUsage(),
		"estimated_context_tokens": s.GetEstimatedContextTokens(),
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
			Data: map[string]any{
				"result": e.Result.ForLLM(),
			},
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
	case event.ContextEstimated:
		return writeFrame(w, FrameResponse{
			SessionID: e.SessionID,
			Event:     "meta",
			Data: map[string]any{
				"estimated_context_tokens": e.EstimatedTokens,
			},
		})
	case event.TextDelta:
		return writeFrame(w, FrameResponse{SessionID: e.SessionID, Delta: e.Delta})
	case event.ClientRequestSent:
		return writeFrame(w, FrameResponse{
			SessionID: e.SessionID,
			Event:     "client_request",
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

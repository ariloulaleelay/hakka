package gateways

import (
	"context"
	"errors"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/event"
)

type frameWriter interface {
	Write(FrameResponse) error
}

type writerFunc func(FrameResponse) error

func (fn writerFunc) Write(r FrameResponse) error { return fn(r) }

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

func writeCommandResult(w frameWriter, res commands.CommandResult, stream bool) (handled, ok bool) {
	if !res.Handled {
		return false, true
	}
	resp := FrameResponse{Done: true}
	if res.Session != nil {
		resp.SessionID = res.Session.ID
	}
	if res.Error != nil {
		resp.Error = res.Error.Error()
		return true, w.Write(resp) == nil
	}
	if res.Action == commands.ActionSessionSwitch || res.Action == commands.ActionSessionCreate {
		eventType := "session_switch"
		if res.Action == commands.ActionSessionCreate {
			eventType = "session_create"
		}
		// Include session messages in the event data so the client can
		// load full conversation history when switching sessions.
		data := map[string]any{}
		if res.Action == commands.ActionSessionSwitch && res.Session != nil {
			data["messages"] = res.Session.AllMessages()
		}
		if err := w.Write(FrameResponse{SessionID: resp.SessionID, Event: eventType, Data: data}); err != nil {
			return true, false
		}
	}
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

func processEvent(w frameWriter, evt event.EngineEvent) bool {
	switch e := evt.(type) {
	case event.ToolCallStarted:
		return writeFrame(w, FrameResponse{
			SessionID:   e.SessionID,
			Event:       "tool",
			Tool:        e.Name,
			Status:      "start",
			ExecSnippet: e.ExecSnippet,
		})
	case event.ToolCallFinished:
		status := "ok"
		if e.Err != nil {
			status = "err"
		} else if e.Result.IsError() {
			status = "err"
		}
		return writeFrame(w, FrameResponse{
			SessionID:   e.SessionID,
			Event:       "tool",
			Tool:        e.Name,
			Status:      status,
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
	}
	return true
}

func writeFrame(w frameWriter, r FrameResponse) bool {
	return w.Write(r) == nil
}

func emitStreamMeta(w frameWriter, sessionID string, session *agent.Session) error {
	promptTokens := 0
	for _, msg := range session.History() {
		promptTokens += len(msg.Content) / 4
	}
	return w.Write(FrameResponse{
		SessionID: sessionID,
		Event:     "meta",
		Data: map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": 0,
			"total_tokens":      session.TotalTokenUsage(),
		},
	})
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
		if err == nil && session != nil && session.ClientCWD != "" {
			cwd = session.ClientCWD
		}
	}
	if cwd != "" {
		ctx = event.ContextWithCWD(ctx, cwd)
	}
	return ctx
}

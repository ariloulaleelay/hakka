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
	ConnKey() string
}

type writerFunc func(FrameResponse) error

func (fn writerFunc) Write(r FrameResponse) error { return fn(r) }
func (fn writerFunc) ConnKey() string              { return "" }

// gwClientWriter implements event.ClientWriter for transport gateways.
// It converts engine frames into wire frames.
type gwClientWriter struct {
	writer    frameWriter
	sessionID string
}

func (g *gwClientWriter) WriteFrame(f event.Frame) error {
	resp := FrameResponse{
		Type:      "req",
		SessionID: g.sessionID,
	}
	if f.ClientReq != nil {
		resp.ClientReq = f.ClientReq
	}
	return g.writer.Write(resp)
}

func (g *gwClientWriter) ConnKey() string { return g.writer.ConnKey() }

// ---------------------------------------------------------------------------
// Command result handling
// ---------------------------------------------------------------------------

// writeCommandResult converts a CommandResult to the appropriate wire frame.
// Returns (handled, ok) where handled means the result was consumed,
// and ok means the write succeeded.
func writeCommandResult(w frameWriter, res commands.CommandResult) (handled, ok bool) {
	if !res.Handled {
		return false, true
	}

	sessionID := ""
	if res.Session != nil {
		sessionID = res.Session.SessionID()
	}

	if res.Action == commands.ActionContinue {
		// Send a minimal done frame so the client knows the command was handled.
		return true, w.Write(FrameResponse{Type: "done", SessionID: sessionID}) == nil
	}

	if res.Error != nil {
		return true, w.Write(FrameResponse{Type: "done", SessionID: sessionID, Error: res.Error.Error()}) == nil
	}

	// Session lifecycle events — emit type:"result" with structured data.
	if res.Action == commands.ActionGetSession || res.Action == commands.ActionSessionCreate {
		data := map[string]any{}
		if res.Action == commands.ActionGetSession && res.Session != nil {
			data["messages"] = res.Session.AllMessages()
		}
		data["session"] = sessionToMap(res.Session)

		eventType := "get_session"
		if res.Action == commands.ActionSessionCreate {
			eventType = "session_create"
		}
		return true, w.Write(FrameResponse{
			Type:    "session",
			SessionID: sessionID,
			SessionEvent: eventType,
			Data:    data,
		}) == nil
	}

	if res.Data != nil {
		parsed := make(map[string]any)
		json.Unmarshal(res.Data, &parsed)
		return true, w.Write(FrameResponse{
			Type:      "result",
			SessionID: sessionID,
			Cmd:       res.Cmd,
			Data:      parsed,
		}) == nil
	}

	// Fallback: send reply text if available, or a minimal done frame.
	return true, w.Write(FrameResponse{Type: "done", SessionID: sessionID, Output: res.Reply}) == nil
}

// ---------------------------------------------------------------------------
// Session ↔ map conversion
// ---------------------------------------------------------------------------

func sessionToMap(s *agent.Session) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"id":                      s.SessionID(),
		"name":                    s.SessionName(),
		"short_id":                shortID(s.SessionID()),
		"message_count":           len(s.AllMessages()),
		"model":                   s.GetModel(),
		"total_tokens":            s.TotalTokenUsage(),
		"total_cost":              s.TotalCost(),
		"estimated_context_tokens": s.GetEstimatedContextTokens(),
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

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

// ---------------------------------------------------------------------------
// Engine event → wire frame conversion
// ---------------------------------------------------------------------------

func processEvent(w frameWriter, evt event.EngineEvent) bool {
	switch e := evt.(type) {
	case event.ToolCallStarted:
		var args json.RawMessage
		if e.Arguments != "" {
			args = json.RawMessage(e.Arguments)
		}
		return writeFrame(w, FrameResponse{
			Type:      "tool",
			SessionID: e.SessionID,
			Tool:      e.Name,
			ID:        e.ID,
			Status:    "start",
			Args:      args,
			Snippet:   e.ExecSnippet,
		})

	case event.ToolCallFinished:
		status := "ok"
		errMsg := ""
		if e.Err != nil {
			status = "err"
			errMsg = e.Err.Error()
		} else if e.Result.IsError() {
			status = "err"
			errMsg = e.Result.ForLLM()
		}
		var args json.RawMessage
		if e.Arguments != "" {
			args = json.RawMessage(e.Arguments)
		}
		fr := FrameResponse{
			Type:      "tool",
			SessionID: e.SessionID,
			Tool:      e.Name,
			ID:        e.ID,
			Status:    status,
			Args:      args,
			Snippet:   e.ExecSnippet,
		}
		if status == "ok" {
			fr.ToolResult = e.Result.ForLLM()
		} else {
			fr.Error = errMsg
		}
		return writeFrame(w, fr)

	case event.UsageReported:
		pt := e.Usage.PromptTokens
		ct := e.Usage.CompletionTokens
		tt := e.Usage.TotalTokens
		et := e.Usage.EstimatedContextTokens
		fr := FrameResponse{
			Type:             "usage",
			SessionID:        e.SessionID,
			PromptTokens:     &pt,
			CompletionTokens: &ct,
			TotalTokens:      &tt,
			EstimatedTokens:  &et,
		}
		if d := e.Usage.Duration; d > 0 {
			ns := d.Nanoseconds()
			fr.DurationNS = &ns
		}
		if c := e.Usage.Cost; c > 0 {
			fr.Cost = &c
		}
		tc := e.Usage.TotalCost
		fr.TotalCost = &tc
		return writeFrame(w, fr)

	case event.ContextEstimated:
		// v2: estimated context is embedded in the UsageReported frame.
		// Skip this event — clients get the info in the next "usage" frame.
		return true

	case event.TextDelta:
		return writeFrame(w, FrameResponse{
			Type:      "delta",
			SessionID: e.SessionID,
			Text:      e.Delta,
		})

	case event.ClientRequestSent:
		return writeFrame(w, FrameResponse{
			Type:      "req",
			SessionID: e.SessionID,
			ClientReq: &event.ClientRequest{
				RequestID: e.RequestID,
				Command:   e.Command,
			},
		})

	case event.TurnFinished:
		sid := e.SessionID
		stats := &TurnStats{
			TotalTokens:            e.TotalTokens,
			TotalCost:              e.TotalCost,
			MessageCount:           e.MessageCount,
			EstimatedContextTokens: e.EstimatedContextTokens,
			Model:                  e.Model,
		}

		if e.Err != nil {
			if errors.Is(e.Err, context.Canceled) {
				return writeFrame(w, FrameResponse{
					Type:      "done",
					SessionID: sid,
					Cancelled: true,
					Stats:     stats,
				})
			}
			return writeFrame(w, FrameResponse{
				Type:      "done",
				SessionID: sid,
				Error:     e.Err.Error(),
				Stats:     stats,
			})
		}
		return writeFrame(w, FrameResponse{
			Type:      "done",
			SessionID: sid,
			Output:    e.Reply,
			Stats:     stats,
		})

	case event.SessionRenamed:
		return writeFrame(w, FrameResponse{
			Type:         "session",
			SessionID:    e.SessionID,
			SessionEvent: "renamed",
			OldName:      e.OldName,
			Name:         e.NewName,
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
		session, err := conv.Sessions().GetOrCreate(ctx, conv.Namespace(), req.SessionID)
		if err == nil && session != nil && session.Read().ClientCWD != "" {
			cwd = session.Read().ClientCWD
		}
	}
	if cwd != "" {
		ctx = event.ContextWithCWD(ctx, cwd)
	}
	return ctx
}

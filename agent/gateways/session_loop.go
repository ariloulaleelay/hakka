package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
		resp.RequestID = f.ClientReq.RequestID
		resp.Command = f.ClientReq.Command
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

	// Session lifecycle events — emit type:"session" with top-level fields.
	if res.Action == commands.ActionGetSession || res.Action == commands.ActionSessionCreate {
		sessionMap := sessionToMap(res.Session)

		eventType := "get_session"
		if res.Action == commands.ActionSessionCreate {
			eventType = "session_create"
		}

		fr := FrameResponse{
			Type:      "session",
			SessionID: sessionID,
			Event:     eventType,
			Session:   sessionMap,
		}
		if res.Action == commands.ActionGetSession && res.Session != nil {
			fr.Messages = messagesToMap(res.Session.Messages())
			// Build events replay from stored messages.
			events := messagesToEvents(res.Session.Messages())
			if !res.InFlight {
				events = append(events, map[string]any{"type": "done"})
			}
			fr.Events = events
		}
		return true, w.Write(fr) == nil
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
	fr := FrameResponse{Type: "done", SessionID: sessionID}
	if res.Reply != "" {
		fr.Text = res.Reply
	}
	return true, w.Write(fr) == nil
}

// ---------------------------------------------------------------------------
// Session ↔ map conversion
// ---------------------------------------------------------------------------

// sessionToMap returns the canonical session metadata via agent.Session.Metadata().
// This ensures get_session, session_info, session_create, welcome, and
// type:"session" frames all use the same field names and values.
func sessionToMap(s *agent.Session) map[string]any {
	if s == nil {
		return nil
	}
	return s.Metadata()
}

func messagesToMap(msgs []agent.Message) []map[string]any {
	if len(msgs) == 0 {
		return nil
	}
	result := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		result[i] = map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		}
	}
	return result
}

// messagesToEvents converts stored messages into a replay-friendly event
// sequence that mirrors the live wire protocol. Each message type maps to
// one or more typed events:
//
//	role:"user"                → {"type":"chat", "text":"..."}
//	role:"assistant"           → {"type":"delta", "text":"..."} + tool starts + usage
//	role:"tool"                → {"type":"tool", status:"ok"|"err", ...}
//
// The caller is responsible for appending a final {"type":"done"} event
// when the session is not in flight.
func messagesToEvents(msgs []agent.Message) []map[string]any {
	if len(msgs) == 0 {
		return nil
	}
	events := make([]map[string]any, 0, len(msgs)*2)

	for _, m := range msgs {
		switch m.Role {
		case agent.RoleUser:
			events = append(events, map[string]any{
				"type": "chat",
				"text": m.Content,
			})

		case agent.RoleAssistant:
			// Assistant thinking / final text
			if m.Content != "" {
				events = append(events, map[string]any{
					"type": "delta",
					"text": m.Content,
				})
			}

			// Tool calls from this assistant turn
			for _, tc := range m.ToolCalls {
				var args any
				if tc.Arguments != "" {
					var parsed any
					if err := json.Unmarshal([]byte(tc.Arguments), &parsed); err == nil {
						args = parsed
					}
				}
				events = append(events, map[string]any{
					"type":    "tool",
					"id":      tc.ID,
					"tool":    tc.Name,
					"status":  "start",
					"args":    args,
					"snippet": tc.ExecSnippet,
				})
			}

			// Usage info for this LLM call
			if m.Usage != nil {
				usageEvt := map[string]any{
					"type":              "usage",
					"prompt_tokens":     m.Usage.PromptTokens,
					"completion_tokens": m.Usage.CompletionTokens,
					"total_tokens":      m.Usage.TotalTokens,
				}
				if m.Usage.Duration > 0 {
					usageEvt["duration_ns"] = m.Usage.Duration.Nanoseconds()
				}
				if m.Usage.Cost > 0 {
					usageEvt["cost"] = m.Usage.Cost
				}
				events = append(events, usageEvt)
			}

		case agent.RoleTool:
			status := "ok"
			result := m.Content
			if strings.HasPrefix(m.Content, "Error: ") {
				status = "err"
			}
			evt := map[string]any{
				"type":   "tool",
				"id":     m.ToolCallID,
				"tool":   m.Name,
				"status": status,
			}
			if status == "ok" {
				evt["result"] = result
			} else {
				// Strip the "Error: " prefix for the error field value
				// to match the wire protocol (which sends raw error text).
				evt["error"] = strings.TrimPrefix(result, "Error: ")
			}
			events = append(events, evt)
		}
	}

	return events
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sessionListToMap(sessions []*agent.Session, currentID string, allSessions []*agent.Session) []map[string]any {
	result := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		m := s.Metadata()
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
			RequestID: e.RequestID,
			Command:   e.Command,
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
			Text:      e.Reply,
			Stats:     stats,
		})

	case event.SessionRenamed:
		return writeFrame(w, FrameResponse{
			Type:      "session",
			SessionID: e.SessionID,
			Event:     "renamed",
			OldName:   e.OldName,
			Name:      e.NewName,
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

func enrichCtxWithCWD(ctx context.Context, conv *agent.Conversation, sessionID string) context.Context {
	if sessionID == "" || conv == nil {
		return ctx
	}
	session, err := conv.Sessions().GetOrCreate(ctx, conv.Namespace(), sessionID)
	if err == nil && session != nil && session.Read().ClientCWD != "" {
		ctx = event.ContextWithCWD(ctx, session.Read().ClientCWD)
	}
	return ctx
}

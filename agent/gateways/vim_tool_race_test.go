package gateways

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// vimToolRaceAdapter returns a single assistant turn that requests a
// vim_list_buffers tool call, then a final text reply.
type vimToolRaceAdapter struct {
	calls int
}

func (a *vimToolRaceAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	a.calls++
	if a.calls == 1 {
		return &agent.LLMResponse{
			Message: agent.Message{
				Role: agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{
					{ID: "call-vim", Name: "vim_list_buffers", Arguments: `{}`},
				},
			},
			FinishReason: "tool_calls",
		}, nil
	}
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *vimToolRaceAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
}

func TestVimToolRace(t *testing.T) {
	adapter := &vimToolRaceAdapter{}
	sm := agent.NewSessionManager(nil, "")

	rr := NewInProcessResponseReader()
	defer rr.Stop()

	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "vim_list_buffers"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			cw := event.ClientFromContext(ctx)
			rr := event.ResponseReaderFromContext(ctx)
			if cw == nil || rr == nil {
				return "", nil
			}
			if err := cw.WriteFrame(event.Frame{
				Event: "vim_request",
				ClientReq: &event.ClientRequest{
					RequestID: "req-1",
					Command:   `return vim.api.nvim_list_bufs()`,
				},
			}); err != nil {
				return "", err
			}
			resp := rr.AwaitResponse(ctx, "req-1")
			if resp == nil {
				return `{"error":"no response"}`, nil
			}
			return `{"ok":true}`, nil
		},
	})

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	writeFrame := func(v FrameResponse) error {
		return enc.Encode(v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	cfg := agent.EngineConfig{MaxToolIterations: 4}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)

	cw := &gwClientWriter{writer: writerFunc(writeFrame), sessionID: "sess-1"}
	ctx = event.ContextWithClient(ctx, cw, rr)

	rr.Deliver(&event.ClientResponse{
		RequestID: "req-1",
		Result:    json.RawMessage(`[{"nr":1,"name":"test.go","ft":"go"}]`),
	})

	session, err := conv.Sessions.GetOrCreate(ctx, "tcp", "sess-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	session.EnableTool("vim_list_buffers")
	if err := conv.Sessions.Save(ctx, "tcp", session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	eventCh, err := conv.Execute(ctx, "sess-1", "list buffers")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var broken bool
	for evt := range eventCh {
		if broken {
			continue
		}
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("engine error: %v", te.Err)
			}
			if te.Reply != "done" {
				t.Fatalf("unexpected reply: %q", te.Reply)
			}
		}
		if !processEvent(writerFunc(writeFrame), evt) {
			broken = true
		}
	}

	if ctx.Err() != nil {
		t.Fatalf("request context was cancelled: %v", ctx.Err())
	}

	var (
		starts    int
		results   int
		vimFrames int
	)
	dec := json.NewDecoder(&buf)
	for {
		var frame FrameResponse
		err := dec.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("TORN JSON FRAME ON THE WIRE: %v", err)
		}
		switch frame.Event {
		case "vim_request":
			vimFrames++
		}
		switch frame.Status {
		case "start":
			starts++
		case "ok", "err":
			results++
		}
	}

	t.Logf("frames decoded: starts=%d results=%d vim_requests=%d", starts, results, vimFrames)
	if starts != 1 {
		t.Fatalf("expected 1 tool start frame, got %d", starts)
	}
	if results != 1 {
		t.Fatalf("expected 1 tool result frame, got %d", results)
	}
	if vimFrames != 1 {
		t.Fatalf("expected 1 vim_request frame, got %d", vimFrames)
	}
}

func TestVimToolRace_TryManyTimes(t *testing.T) {
	const iterations = 5
	for i := 0; i < iterations; i++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			TestVimToolRace(t)
		})
		time.Sleep(time.Millisecond)
	}
}

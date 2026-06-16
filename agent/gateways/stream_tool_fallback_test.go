package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/commands"
)

// TestTCPGatewayStreamToolFallbackDeliversTextAsDelta verifies that the
// streaming tool loop delivers the final text as Delta frames on the wire.
func TestTCPGatewayStreamToolFallbackDeliversTextAsDelta(t *testing.T) {
	adapter := &twoStageAdapter{}
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "example_tool", Description: "test"},
		Tags:   []string{"all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok":true}`, nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 3}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)

	session, _ := sm.GetOrCreate(context.Background(), ns, "")
	session.EnableTool("example_tool")
	sm.Save(context.Background(), ns, session)

	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintf(conn, `{"session_id":%q,"input":"hello","stream":true}`+"\n", session.ID); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var buf strings.Builder
	var done bool
	for !done {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		buf.WriteString(frame.Delta)
		done = frame.Done
	}

	if buf.String() == "" {
		t.Fatal("delta text is empty — streaming tool loop lost the final response")
	}
	if !strings.Contains(buf.String(), "final answer") {
		t.Fatalf("expected delta to contain 'final answer', got: %q", buf.String())
	}
}

// TestTCPGatewayStreamWithRealToolCallsDeliversFinalTextAsDelta exercises
// the full streaming → tool call → tool execution → final text path with
// a real tool registered.
func TestTCPGatewayStreamWithRealToolCallsDeliversFinalTextAsDelta(t *testing.T) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()

	reg.Register("default", &streamThenToolThenTextAdapter{
		streamPartial: "Let me ",
		toolResponse: agent.LLMResponse{
			Message: agent.Message{
				Role: agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{{
					ID: "tc1", Name: "reverse", Arguments: `{"input":"world"}`,
				}},
			},
		},
		finalResponse: agent.LLMResponse{
			Message:      agent.Message{Role: agent.RoleAssistant, Content: "result is dlrow"},
			FinishReason: "stop",
		},
	})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "reverse", Description: "reverse a string"},
		Tags:   []string{"test", "all"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Input string `json:"input"` }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			runes := []rune(args.Input)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return fmt.Sprintf(`{"reversed":%q}`, string(runes)), nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 5}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)

	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	session, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	defer sm.Drop(context.Background(), ns, session.ID)
	session.EnableTool("reverse")
	if err := sm.Save(context.Background(), ns, session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if _, err := fmt.Fprintf(conn, `{"session_id":%q,"input":"reverse world","stream":true}`+"\n", session.ID); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var (
		deltaBuf strings.Builder
		done     bool
		toolSeen bool
	)
	for !done {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Event == "tool" {
			toolSeen = true
		}
		deltaBuf.WriteString(frame.Delta)
		done = frame.Done
	}

	if !toolSeen {
		t.Fatal("expected tool event frames during streaming tool execution")
	}
	if deltaBuf.String() == "" {
		t.Fatal("delta text is empty — final LLM response lost after tool execution")
	}
	final := deltaBuf.String()
	if !strings.Contains(final, "Let me") {
		t.Fatalf("expected delta to contain streamed partial text 'Let me', got: %q", final)
	}
	if !strings.Contains(final, "result is dlrow") {
		t.Fatalf("expected delta to contain final text 'result is dlrow', got: %q", final)
	}
}

// streamThenToolThenTextAdapter simulates:
// Stream() 1st call → partial text, then tool call
// Stream() 2nd call → final text + Done
type streamThenToolThenTextAdapter struct {
	streamPartial   string
	toolResponse    agent.LLMResponse
	finalResponse   agent.LLMResponse
	streamCallCount int
}

func (a *streamThenToolThenTextAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &a.finalResponse, nil
}

func (a *streamThenToolThenTextAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.streamCallCount++
	ch := make(chan agent.StreamResult, 4)
	if a.streamCallCount == 1 {
		if a.streamPartial != "" {
			ch <- agent.StreamResult{Delta: a.streamPartial}
		}
		ch <- agent.StreamResult{ToolCalls: a.toolResponse.Message.ToolCalls}
	} else {
		ch <- agent.StreamResult{Delta: a.finalResponse.Message.Content}
		ch <- agent.StreamResult{Done: true}
	}
	close(ch)
	return ch, nil
}

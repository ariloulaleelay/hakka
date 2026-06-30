package gateways

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
)

type fakeAdapter struct {
	reply string
}

func (f *fakeAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: f.reply},
		FinishReason: "stop",
	}, nil
}

func (f *fakeAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult, 2)
	ch <- agent.StreamResult{Delta: f.reply}
	ch <- agent.StreamResult{Done: true}
	close(ch)
	return ch, nil
}

func newGatewayComponents(reply string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	return newNamedGatewayComponents(reply, "tcp")
}

// newNamedGatewayComponents creates components with a specific namespace.
// Used by tests that need to match gateway namespaces (e.g. "tcp" vs "ws").
func newNamedGatewayComponents(reply, ns string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", &fakeAdapter{reply: reply})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)
	return conv, streamer, cmd
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func dialRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s: %v", addr, lastErr)
	return nil
}

// twoStageAdapter returns tool calls on the first Stream call and text on
// the second, simulating a realistic streaming tool round-trip.
type twoStageAdapter struct {
	callCount int
}

func (a *twoStageAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *twoStageAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.callCount++
	ch := make(chan agent.StreamResult, 4)
	if a.callCount == 1 {
		ch <- agent.StreamResult{ToolCalls: []agent.ToolCall{
			{ID: "tc1", Name: "example_tool", Arguments: `{}`},
		}}
	} else {
		ch <- agent.StreamResult{Delta: "final answer"}
		ch <- agent.StreamResult{Done: true}
	}
	close(ch)
	return ch, nil
}

// --- WebSocket test cases ---------------------------------------------------

func TestWebSocketGatewayRoundTrip(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ws-pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"hello"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output string
	var done bool
	for !done {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp FrameResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Error != "" {
			t.Fatalf("unexpected error: %s", resp.Error)
		}
		if resp.Output != "" {
			output = resp.Output
		}
		done = resp.Done
		if resp.SessionID == "" {
			t.Fatal("expected non-empty session_id in every frame")
		}
	}
	if output != "ws-pong" {
		t.Fatalf("expected output %q, got: %q", "ws-pong", output)
	}
}

func TestWebSocketGatewayStream(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("streamed-payload")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"hi","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var (
		acc  strings.Builder
		done bool
	)
	for !done {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		acc.WriteString(frame.Delta)
		done = frame.Done
	}
	if acc.String() != "streamed-payload" {
		t.Fatalf("expected concatenated payload %q, got: %q", "streamed-payload", acc.String())
	}
}

func TestWebSocketGatewayStreamToolFallbackUsesStreamNotComplete(t *testing.T) {
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
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+session.SessionID()+`","input":"hello","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var (
		toolSeen bool
		delta    strings.Builder
		done     bool
	)
	for !done {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Event == "tool" {
			toolSeen = true
		}
		delta.WriteString(frame.Delta)
		done = frame.Done
	}

	if !toolSeen {
		t.Fatal("expected tool event during streaming")
	}
	if !strings.Contains(delta.String(), "final answer") {
		t.Fatalf("expected delta to contain 'final answer', got: %q", delta.String())
	}
}

func TestWebSocketGatewayJSONCommandReturnsData(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Send a JSON command via the "command" field — text slash commands
	// are NOT intercepted server-side; clients must use JSON commands.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"command":{"cmd":"session_info"},"stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read command reply: %v", err)
	}
	var reply FrameResponse
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("unmarshal reply %q: %v", data, err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error frame: %s", reply.Error)
	}
	if reply.Event != "command_result" {
		t.Fatalf("expected event 'command_result', got event=%q", reply.Event)
	}
	if reply.Cmd != "session_info" {
		t.Fatalf("expected cmd 'session_info', got cmd=%q", reply.Cmd)
	}
	if reply.Data == nil {
		t.Fatalf("expected structured data in response, got: %+v", reply)
	}
	if !reply.Done {
		t.Fatalf("expected done=true for command result, got: %+v", reply)
	}
	if reply.SessionID == "" {
		t.Fatalf("expected session_id in response, got: %+v", reply)
	}
}

func TestWebSocketGatewayStreamingLLMErrorSendsErrorFrame(t *testing.T) {
	adapter := &streamErrorAdapter{err: context.DeadlineExceeded}
	conv, streamer, cmd := newGatewayComponentsWithAdapter(adapter)
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"trigger timeout","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var foundError string
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read error frame: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		if frame.Error != "" {
			foundError = frame.Error
			break
		}
		if frame.Done {
			break
		}
	}
	if foundError == "" {
		t.Fatalf("expected error frame for streaming LLM error, got none")
	}
	if !strings.Contains(foundError, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected deadline error, got: %q", foundError)
	}
}

// --- Helper types -----------------------------------------------------------

type fakeAdapterWithToolCalls struct {
	responses []agent.LLMResponse
	callCount int
}

func (a *fakeAdapterWithToolCalls) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	if a.callCount >= len(a.responses) {
		return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "fallback"}}, nil
	}
	r := a.responses[a.callCount]
	a.callCount++
	return &r, nil
}

func (a *fakeAdapterWithToolCalls) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
}

type streamErrorAdapter struct {
	err error
}

func (a *streamErrorAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return nil, a.err
}

func (a *streamErrorAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	return nil, a.err
}

func newGatewayComponentsWithAdapter(adapter agent.LLMAdapter) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)
	streamer := agent.NewStreamSession(conv, "tcp")
	cmd := commands.New(sm, conv, "", "tcp")
	return conv, streamer, cmd
}

// TestWebSocketGatewayEmitsStatsMetaBeforeDone verifies that a meta
// event with consolidated session stats (total_tokens, total_cost,
// message_count, estimated_context_tokens, model) is emitted before
// the final done frame at the end of a turn.
func TestWebSocketGatewayEmitsStatsMetaBeforeDone(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("stats-check")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"check stats"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var (
		metaSeen     bool
		metaHasStats bool
		doneSeen     bool
	)
	for !doneSeen {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("unexpected error: %s", frame.Error)
		}

		if frame.Event == "meta" && frame.Data != nil {
			metaSeen = true
			if _, ok := frame.Data["total_tokens"]; ok {
				metaHasStats = true
				t.Logf("meta stats: total_tokens=%v, total_cost=%v, message_count=%v, estimated_context_tokens=%v, model=%v",
					frame.Data["total_tokens"], frame.Data["total_cost"],
					frame.Data["message_count"], frame.Data["estimated_context_tokens"],
					frame.Data["model"])
			}
		}

		if frame.Done {
			doneSeen = true
		}
	}

	if !metaSeen {
		t.Fatal("BUG CONFIRMED: expected a meta event with stats before done frame, but none was seen")
	}
	if !metaHasStats {
		t.Fatal("BUG CONFIRMED: meta event did not contain expected stats fields")
	}
}

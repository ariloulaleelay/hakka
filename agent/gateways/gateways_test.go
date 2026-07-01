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

// wsDial is a helper that dials a WebSocket gateway and returns the connection.
func wsDial(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wsDial %s: timed out", url)
	return nil
}

// readUntilDone reads WebSocket frames, discarding non-done frames, and
// returns the final "done" frame data.
func readUntilDone(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &base); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if base.Type == "done" {
			return data
		}
	}
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

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome frame
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	writeWSFrame(t, c, map[string]any{"type": "chat", "input": "hello"})

	data := readUntilDone(t, c)
	var done struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		SessionID string `json:"session_id"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(data, &done); err != nil {
		t.Fatalf("unmarshal done: %v", err)
	}
	if done.Error != "" {
		t.Fatalf("unexpected error: %s", done.Error)
	}
	if done.Text != "ws-pong" {
		t.Fatalf("expected text %q, got: %q", "ws-pong", done.Text)
	}
	if done.SessionID == "" {
		t.Fatal("expected non-empty session_id in done frame")
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

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	writeWSFrame(t, c, map[string]any{"type": "chat", "input": "hi", "stream": true})

	var acc strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Type == "delta" {
			acc.WriteString(frame.Text)
		}
		if frame.Type == "done" {
			break
		}
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

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	writeWSFrame(t, c, map[string]any{
		"type":       "chat",
		"session_id": session.SessionID(),
		"input":      "hello",
		"stream":     true,
	})

	var (
		toolSeen bool
		delta    strings.Builder
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Event string `json:"event"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Type == "tool" {
			toolSeen = true
		}
		if frame.Type == "delta" {
			delta.WriteString(frame.Text)
		}
		if frame.Type == "done" {
			break
		}
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

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	// Send a JSON command via the "cmd" type.
	writeWSFrame(t, c, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "session_info"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read command reply: %v", err)
	}
	var reply struct {
		Type      string         `json:"type"`
		Cmd       string         `json:"cmd"`
		Data      map[string]any `json:"data"`
		SessionID string         `json:"session_id"`
		Error     string         `json:"error"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("unmarshal reply %q: %v", data, err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error frame: %s", reply.Error)
	}
	if reply.Type != "result" {
		t.Fatalf("expected type 'result', got type=%q", reply.Type)
	}
	if reply.Cmd != "session_info" {
		t.Fatalf("expected cmd 'session_info', got cmd=%q", reply.Cmd)
	}
	if reply.Data == nil {
		t.Fatalf("expected structured data in response, got: %+v", reply)
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

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	writeWSFrame(t, c, map[string]any{
		"type":  "chat",
		"input": "trigger timeout",
		"stream": true,
	})

	var foundError string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read error frame: %v", err)
		}
		var frame struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		if frame.Error != "" {
			foundError = frame.Error
			break
		}
		if frame.Type == "done" {
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

// TestWebSocketGatewayDoneFrameHasStats verifies that the done frame
// carries embedded stats (total_tokens, total_cost, etc.) instead of
// a separate meta event.
func TestWebSocketGatewayDoneFrameHasStats(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("stats-check")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	c := wsDial(t, addr)
	defer c.CloseNow()

	// Discard welcome
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)

	writeWSFrame(t, c, map[string]any{"type": "chat", "input": "check stats"})

	data := readUntilDone(t, c)
	var done struct {
		Type  string `json:"type"`
		Stats *struct {
			TotalTokens  int    `json:"total_tokens"`
			MessageCount int    `json:"message_count"`
			Model        string `json:"model"`
		} `json:"stats"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &done); err != nil {
		t.Fatalf("unmarshal done: %v", err)
	}
	if done.Error != "" {
		t.Fatalf("unexpected error: %s", done.Error)
	}
	if done.Type != "done" {
		t.Fatalf("expected type 'done', got %q", done.Type)
	}
	if done.Stats == nil {
		t.Fatal("expected stats in done frame")
	}
	if done.Stats.Model == "" {
		t.Fatalf("expected non-empty model in stats, got %q", done.Stats.Model)
	}
	if done.Stats.MessageCount <= 0 {
		t.Fatalf("expected positive message_count in stats, got %d", done.Stats.MessageCount)
	}
}

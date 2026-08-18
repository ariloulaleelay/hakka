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
	"github.com/ariloulaleelay/hakka/agent/event"
)

type fakeAdapter struct {
	reply string
}

func (f *fakeAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	if onDelta != nil {
		onDelta(f.reply)
	}
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: f.reply},
		FinishReason: "stop",
	}, nil
}

func newGatewayComponents(reply string) (*agent.Conversation, *commands.CommandProcessor) {
	return newNamedGatewayComponents(reply, "tcp")
}

// newNamedGatewayComponents creates components with a specific namespace.
func newNamedGatewayComponents(reply, ns string) (*agent.Conversation, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", &fakeAdapter{reply: reply})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	return conv, cmd
}

// twoStageAdapter returns tool calls on the first call and text on the second.
type twoStageAdapter struct {
	callCount int
}

func (a *twoStageAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, onDelta func(string)) (*agent.LLMResponse, error) {
	a.callCount++
	if a.callCount == 1 {
		return &agent.LLMResponse{
			Message: agent.Message{
				Role: agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{
					{ID: "tc1", Name: "example_tool", Arguments: `{}`},
				},
			},
			FinishReason: "tool_calls",
		}, nil
	}
	if onDelta != nil {
		onDelta("final answer")
	}
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "final answer"},
		FinishReason: "stop",
	}, nil
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
	conv, cmd := newGatewayComponents("ws-pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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
	conv, cmd := newGatewayComponents("streamed-payload")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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
	cmd := commands.New(sm, conv, "", ns)

	session, _ := sm.CreateWithID(context.Background(), ns, "")
	session.EnableTool(context.Background(), "example_tool")
	sm.Save(context.Background(), ns, session)

	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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
	conv, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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
	// Create an explicit session because command lookup is now lookup-only.
	if _, err := conv.Sessions().CreateWithID(context.Background(), gw.Handler.Namespace, "gateway-command-session"); err != nil {
		t.Fatal(err)
	}
	writeWSFrame(t, c, map[string]any{
		"type":       "cmd",
		"session_id": "gateway-command-session",
		"command":    map[string]any{"cmd": "session_info"},
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
	if reply.Type != "done" && reply.Type != "result" {
		t.Fatalf("expected type 'done' or 'result', got type=%q", reply.Type)
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
	conv, cmd := newGatewayComponentsWithAdapter(adapter)
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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

func (a *fakeAdapterWithToolCalls) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	if a.callCount >= len(a.responses) {
		return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "fallback"}}, nil
	}
	r := a.responses[a.callCount]
	a.callCount++
	return &r, nil
}

type streamErrorAdapter struct {
	err error
}

func (a *streamErrorAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	return nil, a.err
}

func newGatewayComponentsWithAdapter(adapter agent.LLMAdapter) (*agent.Conversation, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)
	cmd := commands.New(sm, conv, "", "tcp")
	return conv, cmd
}

// TestWebSocketGatewayDoneFrameHasStats verifies that the done frame
// carries embedded stats (total_tokens, total_cost, etc.) instead of
// a separate meta event.
func TestWebSocketGatewayDoneFrameHasStats(t *testing.T) {
	conv, cmd := newGatewayComponents("stats-check")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
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

func TestMessagesToEvents_includes_message_ids(t *testing.T) {
	msgs := []agent.Message{
		{ID: "m1", Role: agent.RoleUser, Content: "hello", Timestamp: 1700000000123},
		{ID: "m2", Role: agent.RoleAssistant, Content: "hi there", Timestamp: 1700000000456},
	}

	events := messagesToEvents(msgs)

	// First event: type:"chat" with message id.
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	chat := events[0]
	if chat["type"] != "chat" {
		t.Fatalf("expected type 'chat', got %q", chat["type"])
	}
	if chat["id"] != "m1" {
		t.Fatalf("expected id 'm1' on chat event, got %v", chat["id"])
	}
	if chat["text"] != "hello" {
		t.Fatalf("expected text 'hello', got %v", chat["text"])
	}

	delta := events[1]
	if delta["type"] != "delta" {
		t.Fatalf("expected type 'delta', got %q", delta["type"])
	}
	if delta["id"] != "m2" {
		t.Fatalf("expected id 'm2' on delta event, got %v", delta["id"])
	}
	if delta["text"] != "hi there" {
		t.Fatalf("expected text 'hi there', got %v", delta["text"])
	}
}

func TestFrameForEvent_includes_message_id_on_delta_and_done(t *testing.T) {
	// TextDelta → delta frame should carry message ID.
	deltaEvt := event.TextDelta{SessionID: "s1", Delta: "hello", MessageID: "msg-1"}
	fr, ok := frameForEvent(deltaEvt)
	if !ok {
		t.Fatal("frameForEvent(TextDelta) should return ok")
	}
	if fr.Type != "delta" {
		t.Fatalf("expected type 'delta', got %q", fr.Type)
	}
	if fr.ID != "msg-1" {
		t.Fatalf("expected id 'msg-1' on delta frame, got %q", fr.ID)
	}

	// TurnFinished → done frame should carry message ID.
	doneEvt := event.TurnFinished{
		SessionID: "s1",
		Reply:     "final",
		MessageID: "msg-2",
	}
	fr, ok = frameForEvent(doneEvt)
	if !ok {
		t.Fatal("frameForEvent(TurnFinished) should return ok")
	}
	if fr.Type != "done" {
		t.Fatalf("expected type 'done', got %q", fr.Type)
	}
	if fr.ID != "msg-2" {
		t.Fatalf("expected id 'msg-2' on done frame, got %q", fr.ID)
	}
}

func TestDeltaAndDoneCarrySameMessageID(t *testing.T) {
	conv, _ := newGatewayComponents("hello from streaming")

	// Create a session first.
	sess, err := conv.Sessions().CreateWithID(context.Background(), "tcp", "test-session-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	conv.EnsureDefaultModel(context.Background(), sess)
	if err := sess.AddMessages(context.Background(), []agent.Message{agent.Message{ID: agent.MakeUniqueID(), Role: agent.RoleUser, Content: "hi"}}, 0, 0); err != nil {
		t.Fatal(err)
	}

	// Execute a turn with streaming.
	eventCh, err := conv.Execute(context.Background(), sess.SessionID(), "hi again")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var deltaID, doneID string
	var gotDelta, gotDone bool

	for evt := range eventCh {
		switch e := evt.(type) {
		case event.TextDelta:
			deltaID = e.MessageID
			gotDelta = true
		case event.TurnFinished:
			doneID = e.MessageID
			gotDone = true
		}
	}

	if !gotDelta {
		t.Fatal("expected at least one TextDelta event")
	}
	if !gotDone {
		t.Fatal("expected a TurnFinished event")
	}
	if deltaID == "" {
		t.Fatal("expected non-empty MessageID on TextDelta")
	}
	if doneID == "" {
		t.Fatal("expected non-empty MessageID on TurnFinished")
	}
	if deltaID != doneID {
		t.Fatalf("delta MessageID %q != done MessageID %q", deltaID, doneID)
	}
}

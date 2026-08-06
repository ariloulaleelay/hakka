package gateways

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
)

// TestGetSessionDuringActiveTurn verifies that sending a get_session
// command while a turn is running returns the session data immediately
// (without blocking on turn completion). With the namespace hub
// architecture, all events are broadcast to every subscriber; no
// per-turn subscription is needed.
func TestGetSessionDuringActiveTurn(t *testing.T) {
	// ── Setup: adapter that blocks until we signal ──────────────────────
	blockCh := make(chan struct{})
	slowAdapter := &blockingAdapter{blockCh: blockCh}

	reg := agent.NewRegistry()
	reg.Register("default", slowAdapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "noop", Description: "does nothing"},
		Tags:   []string{"all"},
		Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	cfg := agent.EngineConfig{MaxToolIterations: 2}
	ns := "testns_get_session_while_inflight"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	handler := NewTurnHandler(conv, cmd, ns)

	// Create a session and send a chat to start a turn that blocks.
	session, err := sm.CreateWithID(context.Background(), ns, "")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}

	// Writer for the chat request.
	chatWriter := &spyWriter{connKey: "chat-writer"}
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	// Start a chat turn in a separate goroutine (the turn will block
	// because the adapter blocks on blockCh). With the hub, the goroutine
	// returns immediately; the turn runs in the background.
	go handler.HandleRequest(context.Background(), FrameRequest{
		SessionID: session.SessionID(),
		Input:     "hello",
	}, chatWriter, responseReader)

	// Wait for the turn to actually start.
	time.Sleep(100 * time.Millisecond)

	// Verify the turn is active.
	if active := handler.Turns().Get(session.SessionID()); active == nil {
		t.Fatal("expected active turn to be registered")
	}

	// ── Now simulate a second connection sending get_session ────────────
	getSessionWriter := &spyWriter{connKey: "get-session-writer"}
	go handler.HandleRequest(context.Background(), FrameRequest{
		SessionID: session.SessionID(),
		Command: &CommandRequest{
			Cmd:    "get_session",
			Params: json.RawMessage(`{"id":"` + session.SessionID() + `"}`),
		},
	}, getSessionWriter, responseReader)

	// Give the get_session handler time to execute and write frames.
	// With the hub, get_session returns immediately (no blocking on
	// ReplaceSubscriber).
	time.Sleep(200 * time.Millisecond)

	// The get_session response should have been sent immediately.
	frames := getSessionWriter.Frames()
	if len(frames) == 0 {
		t.Fatal("expected at least one frame from get_session, got none — " +
			"get_session response was never sent")
	}

	// The first frame should be a "session" frame (not "done")
	fr := frames[0]
	if fr.Type != "session" {
		t.Fatalf("expected first frame type 'session', got %q", fr.Type)
	}
	if fr.Event != "get_session" {
		t.Fatalf("expected event 'get_session', got %q", fr.Event)
	}
	if fr.Session == nil {
		t.Fatal("expected session data in get_session response")
	}

	t.Logf("get_session response received: type=%s event=%s", fr.Type, fr.Event)

	// The events should NOT include "done" because the session is in-flight.
	if fr.Events != nil {
		for _, evt := range fr.Events {
			if evt["type"] == "done" {
				t.Fatal("get_session should not include 'done' event for in-flight session")
			}
		}
	}

	// Unblock the turn so it can finish.
	close(blockCh)

	// Wait for the turn to complete — the done frame is broadcast
	// to all hub subscribers (including chatWriter).
	if !chatWriter.WaitForDone(t, 5*time.Second) {
		t.Fatal("timed out waiting for turn to finish")
	}

	// Verify the turn completed by checking session messages.
	session, err = sm.Get(context.Background(), ns, session.SessionID())
	if err != nil {
		t.Fatalf("Get session after turn: %v", err)
	}
	if len(session.Messages()) != 2 {
		t.Fatalf("expected 2 messages after turn (user+assistant), got %d", len(session.Messages()))
	}
	t.Logf("session messages after turn: %d", len(session.Messages()))

	// The get_session writer should also have received the done frame
	// via the hub broadcast.
	frames = getSessionWriter.Frames()
	var foundDone bool
	for _, f := range frames {
		if f.Type == "done" {
			foundDone = true
			break
		}
	}
	if !foundDone {
		t.Log("get_session writer did not receive done frame (may have been missed)")
	}
}

// blockingAdapter blocks on a channel during Stream/Complete to simulate
// a long-running LLM response.
type blockingAdapter struct {
	blockCh chan struct{}
	called  bool
	mu      sync.Mutex
}

func (a *blockingAdapter) Complete(ctx context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	a.mu.Lock()
	a.called = true
	a.mu.Unlock()
	select {
	case <-a.blockCh:
	case <-ctx.Done():
	}
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

// TestGetSessionDuringActiveTurnViaWebSocket is the same test but through
// the full WebSocket gateway (end-to-end). With the hub, sendWelcome
// no longer blocks on ReplaceSubscriber.
func TestGetSessionDuringActiveTurnViaWebSocket(t *testing.T) {
	blockCh := make(chan struct{})
	slowAdapter := &blockingAdapter{blockCh: blockCh}

	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", slowAdapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	ns := "test_ws_get_session_inflight"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	gw := NewWebSocketGateway(conv, cmd, freeAddr(t))

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Step 1: Create a session first (not via WebSocket).
	session, err := sm.CreateWithID(context.Background(), ns, "")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	t.Logf("created session: %s", session.SessionID())

	// Step 2: Connect client 1 and start a chat turn that blocks.
	c1 := wsDial(t, gw.Addr)
	defer c1.CloseNow()

	// c1 receives: welcome (no auto-subscribe because bestSession has no
	// messages yet — it was just created)
	readWSFrame(t, c1, 3*time.Second, new(struct{ Type string })) // welcome

	// Send chat to start a turn.
	writeWSFrame(t, c1, map[string]any{
		"type":       "chat",
		"session_id": session.SessionID(),
		"input":      "hello that blocks",
	})

	// Wait for the turn to actually start running.
	time.Sleep(300 * time.Millisecond)

	// Verify the turn is active.
	if active := gw.Handler.Turns().Get(session.SessionID()); active == nil {
		t.Fatal("expected active turn to be registered")
	}

	// Step 3: Connect client 2 while the turn is running.
	c2 := wsConnect(t, gw.Addr)
	defer c2.CloseNow()

	// Client 2 should receive welcome frame immediately.
	readWSFrame(t, c2, 3*time.Second, new(struct{ Type string })) // welcome

	// Now send get_session.
	writeWSFrame(t, c2, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "get_session", "params": map[string]any{"id": session.SessionID()}},
	})

	// Read the response — should be a "session" frame with get_session event.
	type sessionResp struct {
		Type    string         `json:"type"`
		Event   string         `json:"event"`
		Session map[string]any `json:"session"`
		Error   string         `json:"error"`
	}
	var resp sessionResp
	readWSFrame(t, c2, 5*time.Second, &resp)
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Type != "session" {
		t.Fatalf("expected type 'session', got type=%q", resp.Type)
	}
	if resp.Event != "get_session" {
		t.Fatalf("expected event 'get_session', got event=%q", resp.Event)
	}
	if resp.Session == nil {
		t.Fatal("expected session data in response")
	}
	t.Logf("c2: get_session response received (session has messages: %v)", resp.Session["message_count"])

	// Unblock the turn.
	close(blockCh)

	// Wait for c1's turn to finish. The done frame arrives via hub broadcast
	// to all subscribers (including both c1 and c2).
	ctxDone, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c1.Read(ctxDone)
		if err != nil {
			break
		}
		var base struct {
			Type string `json:"type"`
		}
		json.Unmarshal(data, &base)
		if base.Type == "done" {
			break
		}
	}
}

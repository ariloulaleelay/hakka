package gateways

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// v2 protocol tests
//
// These tests define the expected behaviour of the wire protocol.
// ---------------------------------------------------------------------------

// wsConnect is a helper that dials a WebSocket gateway and returns the
// connection. It retries until the gateway is ready.
func wsConnect(t *testing.T, addr string) *websocket.Conn {
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
	t.Fatalf("wsConnect %s: timed out", url)
	return nil
}

// readWSFrame reads one JSON frame from the WebSocket and unmarshals it
// into the provided value, returning the raw bytes.
func readWSFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration, dst any) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal ws frame: %v (raw: %s)", err, string(data))
	}
	return data
}

// writeWSFrame writes a JSON object to the WebSocket.
func writeWSFrame(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal ws frame: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write ws frame: %v", err)
	}
}

// TestWelcomeOnConnect verifies that the server sends a "welcome" frame
// immediately on WebSocket connect, listing all sessions with in_flight
// status at the top level (not inside "data").
func TestWelcomeOnConnect(t *testing.T) {
	conv, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Read the first frame — it should be a welcome without sending anything.
	var welcome struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)

	if welcome.Type != "welcome" {
		t.Fatalf("expected type 'welcome', got %q", welcome.Type)
	}

	// Sessions should be at the top level (not inside "data").
	if welcome.Sessions != nil {
		for _, s := range welcome.Sessions {
			if _, exists := s["in_flight"]; !exists {
				t.Error("expected in_flight field in session entry")
			}
		}
	}
}

// TestToolFrameHasID verifies that every type:"tool" frame carries a
// unique "id" field so clients can correlate start → ok/err.
func TestToolFrameHasID(t *testing.T) {
	// Simulate a tool call event with an ID and verify it appears in the frame.
	sessionID := "test-session"
	evt := event.ToolCallStarted{
		SessionID: sessionID,
		ID:        "call_abc123",
		Name:      "read_file",
		Arguments: `{"path":"README.md"}`,
	}

	var frames []FrameResponse
	w := &spyWriter{}
	processEvent(w, evt)
	frames = w.Frames()

	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	fr := frames[0]

	// The frame type should be "tool"
	if fr.Type != "tool" {
		t.Fatalf("expected type 'tool', got %q", fr.Type)
	}

	// The ID should be present
	if fr.ID == "" {
		t.Fatal("expected non-empty id in tool frame")
	}
	if fr.ID != "call_abc123" {
		t.Fatalf("expected id 'call_abc123', got %q", fr.ID)
	}

	// Verify finish frame also has the ID
	w2 := &spyWriter{}
	processEvent(w2, event.ToolCallFinished{
		SessionID: sessionID,
		ID:        "call_abc123",
		Name:      "read_file",
		Arguments: `{"path":"README.md"}`,
	})
	frames2 := w2.Frames()
	if len(frames2) != 1 {
		t.Fatalf("expected 1 finish frame, got %d", len(frames2))
	}
	if frames2[0].ID != "call_abc123" {
		t.Fatalf("expected finish frame id 'call_abc123', got %q", frames2[0].ID)
	}
}

// TestUsageIncludesEstimatedContext verifies that the usage frame carries
// estimated_context_tokens alongside the usual token counts.
func TestUsageIncludesEstimatedContext(t *testing.T) {
	w := &spyWriter{}
	processEvent(w, event.UsageReported{
		SessionID: "test-session",
		Usage: event.UsageInfo{
			PromptTokens:           10,
			CompletionTokens:       20,
			TotalTokens:            30,
			EstimatedContextTokens: 52000,
		},
	})
	frames := w.Frames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	fr := frames[0]
	if fr.Type != "usage" {
		t.Fatalf("expected type 'usage', got %q", fr.Type)
	}
	if fr.PromptTokens == nil {
		t.Fatal("expected prompt_tokens in usage frame")
	}
	if *fr.PromptTokens != 10 {
		t.Fatalf("expected prompt_tokens 10, got %d", *fr.PromptTokens)
	}
	if fr.TotalTokens == nil {
		t.Fatal("expected total_tokens in usage frame")
	}
	if *fr.TotalTokens != 30 {
		t.Fatalf("expected total_tokens 30, got %d", *fr.TotalTokens)
	}
	// estimated_context_tokens should be in the usage frame, not separate
	if fr.EstimatedTokens == nil {
		t.Fatal("expected estimated_context_tokens in usage frame")
	}
	if *fr.EstimatedTokens != 52000 {
		t.Fatalf("expected estimated_context_tokens 52000, got %d", *fr.EstimatedTokens)
	}
}

// TestDoneFrameHasStats verifies that the done frame carries end-of-turn
// statistics embedded directly, not as a separate meta event. The LLM
// content field is called "text" (unified with delta).
func TestDoneFrameHasStats(t *testing.T) {
	w := &spyWriter{}
	processEvent(w, event.TurnFinished{
		SessionID:              "test-session",
		Reply:                  "Hello!",
		TotalTokens:            1290,
		TotalCost:              0.00019,
		MessageCount:           5,
		EstimatedContextTokens: 52000,
		Model:                  "deepseek",
	})
	frames := w.Frames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame (done with embedded stats), got %d", len(frames))
	}
	fr := frames[0]
	if fr.Type != "done" {
		t.Fatalf("expected type 'done', got %q", fr.Type)
	}
	// Content field is "text" (unified with delta)
	if fr.Text != "Hello!" {
		t.Fatalf("expected text 'Hello!', got %q", fr.Text)
	}
	// Stats should be embedded directly in the done frame
	if fr.Stats == nil {
		t.Fatal("expected stats in done frame")
	}
	if fr.Stats.TotalTokens != 1290 {
		t.Fatalf("expected stats.total_tokens 1290, got %d", fr.Stats.TotalTokens)
	}
	if fr.Stats.Model != "deepseek" {
		t.Fatalf("expected stats.model 'deepseek', got %q", fr.Stats.Model)
	}
}

// TestInboundFrameTypeParsing verifies that all inbound frames with
// explicit "type" field are parsed correctly.
func TestInboundFrameTypeParsing(t *testing.T) {
	// Chat frame with type
	var chat FrameRequest
	if err := json.Unmarshal([]byte(`{"type":"chat","session_id":"abc","input":"hello","stream":true}`), &chat); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if chat.Type != "chat" {
		t.Fatalf("expected type 'chat', got %q", chat.Type)
	}
	if chat.Input != "hello" {
		t.Fatalf("expected input 'hello', got %q", chat.Input)
	}

	// Cmd frame
	var cmd FrameRequest
	if err := json.Unmarshal([]byte(`{"type":"cmd","session_id":"abc","command":{"cmd":"session_list"}}`), &cmd); err != nil {
		t.Fatalf("unmarshal cmd: %v", err)
	}
	if cmd.Type != "cmd" {
		t.Fatalf("expected type 'cmd', got %q", cmd.Type)
	}
	if cmd.Command == nil || cmd.Command.Cmd != "session_list" {
		t.Fatalf("expected command.cmd 'session_list'")
	}

	// Cancel frame
	var cancel FrameRequest
	if err := json.Unmarshal([]byte(`{"type":"cancel","session_id":"abc"}`), &cancel); err != nil {
		t.Fatalf("unmarshal cancel: %v", err)
	}
	if cancel.Type != "cancel" {
		t.Fatalf("expected type 'cancel', got %q", cancel.Type)
	}

	// Resp frame (client response to server request)
	var resp FrameRequest
	if err := json.Unmarshal([]byte(`{"type":"resp","request_id":"req-1","result":42}`), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Type != "resp" {
		t.Fatalf("expected type 'resp', got %q", resp.Type)
	}
	if resp.RequestID != "req-1" {
		t.Fatalf("expected request_id 'req-1', got %q", resp.RequestID)
	}
}

// TestWebSocketV2RoundTrip tests a full chat turn using the v2 protocol.
// Sends type:"chat", receives type:"done" with embedded stats and "text" field.
func TestWebSocketV2RoundTrip(t *testing.T) {
	conv, cmd := newNamedGatewayComponents("hello from llm", "v2test")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Expect welcome frame first (discard it)
	var base struct {
		Type string `json:"type"`
	}
	readWSFrame(t, conn, 3*time.Second, &base)
	if base.Type != "welcome" {
		t.Fatalf("expected first frame to be welcome, got %q", base.Type)
	}

	// Send chat with v2 type
	writeWSFrame(t, conn, map[string]any{
		"type":   "chat",
		"input":  "hello",
	})

	// Read frames until done
	var doneText string
	var doneStats *struct {
		TotalTokens int    `json:"total_tokens"`
		Model       string `json:"model"`
	}
	found := false
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		var msg struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Error string          `json:"error"`
			Stats json.RawMessage `json:"stats"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal frame %d: %v", i, err)
		}
		if msg.Type == "done" {
			doneText = msg.Text
			if msg.Stats != nil {
				json.Unmarshal(msg.Stats, &doneStats)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected done frame with type 'done'")
	}
	if doneText != "hello from llm" {
		t.Fatalf("expected text %q, got %q", "hello from llm", doneText)
	}
	if doneStats == nil {
		t.Fatal("expected stats in done frame")
	}
	if doneStats.Model == "" {
		t.Fatal("expected non-empty model in stats")
	}
}

// TestAutoSubscribeOnConnect verifies that the welcome frame includes
// in_flight status and the connection is ready to receive events without
// extra round trips.
func TestAutoSubscribeOnConnect(t *testing.T) {
	conv, cmd := newNamedGatewayComponents("response from running turn", "autosub")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Connect and verify welcome includes session listing.
	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	var welcome struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)
	if welcome.Type != "welcome" {
		t.Fatalf("expected welcome, got %q", welcome.Type)
	}

	// The welcome should include sessions (even if empty) at the top level.
	if welcome.Sessions == nil {
		t.Fatal("expected sessions in welcome frame at top level")
	}

	// Send a chat and verify we get a done frame back (not an error)
	writeWSFrame(t, conn, map[string]any{
		"type":  "chat",
		"input": "hello",
	})

	foundDone := false
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		var msg struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		json.Unmarshal(data, &msg)
		if msg.Error != "" {
			t.Fatalf("unexpected error frame: %s", msg.Error)
		}
		if msg.Type == "done" {
			foundDone = true
			break
		}
	}
	if !foundDone {
		t.Fatal("expected done frame")
	}
}

// TestGetSessionEvents verifies that get_session returns an "events"
// field alongside the existing "messages" field, with events that
// mirror the live wire protocol (chat, delta, tool start/ok, usage,
// done). Tool calls from history are replayed as typed events so the
// UI can render them the same way as live streaming frames.
func TestGetSessionEvents(t *testing.T) {
	conv, cmd := newNamedGatewayComponents("unused", "default")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Create a session with realistic messages including tool calls.
	sm := conv.Sessions()
	session, err := sm.GetOrCreate(context.Background(), "default", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	usage := &agent.Usage{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}

	// Build a conversation with: user msg → assistant (thinking + tool call) → tool result → assistant (final)
	session.Append(agent.Message{Role: "user", Content: "Read README.md", Timestamp: 1700000000123})
	session.Append(agent.Message{
		Role:    "assistant",
		Content: "Let me check that file...",
		ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"README.md"}`, ExecSnippet: "read_file 'README.md'"},
			{ID: "call_2", Name: "search", Arguments: `{"pattern":"TODO"}`, ExecSnippet: `search 'TODO'`},
		},
		Usage: usage,
		Timestamp: 1700000000123,
	})
	session.Append(agent.Message{
		Role:       "tool",
		Content:    "# Hakka\n\nA Go framework...",
		ToolCallID: "call_1",
		Name:       "read_file",
		Timestamp:  1700000000123,
	})
	session.Append(agent.Message{
		Role:       "tool",
		Content:    "Error: pattern not found",
		ToolCallID: "call_2",
		Name:       "search",
		Timestamp:  1700000000123,
	})
	session.Append(agent.Message{
		Role:    "assistant",
		Content: "Here's what I found in README.md...",
		Usage:   usage,
		Timestamp: 1700000000123,
	})

	if err := sm.Save(context.Background(), "default", session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Send get_session command via WebSocket.
	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome and auto-subscribe session frames.
	var base struct{ Type string }
	readWSFrame(t, conn, 3*time.Second, &base) // welcome

	// After welcome, the server auto-subscribes to the best session and
	// sends a "session" frame (get_session) with messages. Read and discard it.
	var sessionFrame struct{ Type string }
	readWSFrame(t, conn, 3*time.Second, &sessionFrame) // auto-subscribe session

	writeWSFrame(t, conn, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "get_session", "params": map[string]string{"id": session.SessionID()}},
	})

	// Read response.
	var resp struct {
		Type     string           `json:"type"`
		Event    string           `json:"event"`
		Session  map[string]any   `json:"session"`
		Events   []map[string]any `json:"events"`
		Error    string           `json:"error"`
	}
	rawData := readWSFrame(t, conn, 3*time.Second, &resp)

	// --- Messages field should NOT be present (only events are used) ---
	var rawMap map[string]any
	if err := json.Unmarshal(rawData, &rawMap); err == nil {
		if _, exists := rawMap["messages"]; exists {
			t.Fatal("expected messages field to be absent (use events instead)")
		}
	}

	if resp.Type != "session" {
		t.Fatalf("expected type 'session', got %q", resp.Type)
	}
	if resp.Event != "get_session" {
		t.Fatalf("expected event 'get_session', got %q", resp.Event)
	}
	if resp.Session == nil {
		t.Fatal("expected session field")
	}

	// --- Assert events field is present ---
	if resp.Events == nil {
		t.Fatal("expected events field in get_session response")
	}

	// --- Assert events have the expected shape ---
	// Events should be: chat, delta, tool(start)×2, usage, tool(ok), tool(err), delta, usage, done
	if len(resp.Events) != 10 {
		t.Fatalf("expected 10 events, got %d: %+v", len(resp.Events), resp.Events)
	}

	// 1. User message → chat event
	evt0 := resp.Events[0]
	if evt0["type"] != "chat" {
		t.Fatalf("event[0] type: expected 'chat', got %q", evt0["type"])
	}
	if evt0["text"] != "Read README.md" {
		t.Fatalf("event[0] text: expected 'Read README.md', got %q", evt0["text"])
	}

	// 2. Assistant thinking → delta event
	evt1 := resp.Events[1]
	if evt1["type"] != "delta" {
		t.Fatalf("event[1] type: expected 'delta', got %q", evt1["type"])
	}
	if evt1["text"] != "Let me check that file..." {
		t.Fatalf("event[1] text: expected 'Let me check that file...', got %q", evt1["text"])
	}

	// 3. First tool call start
	evt2 := resp.Events[2]
	if evt2["type"] != "tool" {
		t.Fatalf("event[2] type: expected 'tool', got %q", evt2["type"])
	}
	if evt2["id"] != "call_1" {
		t.Fatalf("event[2] id: expected 'call_1', got %q", evt2["id"])
	}
	if evt2["tool"] != "read_file" {
		t.Fatalf("event[2] tool: expected 'read_file', got %q", evt2["tool"])
	}
	if evt2["status"] != "start" {
		t.Fatalf("event[2] status: expected 'start', got %q", evt2["status"])
	}
	if evt2["args"] == nil {
		t.Fatal("event[2] args should be present")
	}
	if evt2["snippet"] != "read_file 'README.md'" {
		t.Fatalf("event[2] snippet: expected 'read_file README.md', got %q", evt2["snippet"])
	}

	// 4. Second tool call start
	evt3 := resp.Events[3]
	if evt3["type"] != "tool" {
		t.Fatalf("event[3] type: expected 'tool', got %q", evt3["type"])
	}
	if evt3["id"] != "call_2" {
		t.Fatalf("event[3] id: expected 'call_2', got %q", evt3["id"])
	}
	if evt3["tool"] != "search" {
		t.Fatalf("event[3] tool: expected 'search', got %q", evt3["tool"])
	}
	if evt3["status"] != "start" {
		t.Fatalf("event[3] status: expected 'start', got %q", evt3["status"])
	}

	// 5. First usage event (from the first LLM call with tool calls)
	evt4 := resp.Events[4]
	if evt4["type"] != "usage" {
		t.Fatalf("event[4] type: expected 'usage', got %q", evt4["type"])
	}
	if evt4["total_tokens"] != float64(30) {
		t.Fatalf("event[4] total_tokens: expected 30, got %v", evt4["total_tokens"])
	}

	// 6. First tool result (ok)
	evt5 := resp.Events[5]
	if evt5["type"] != "tool" {
		t.Fatalf("event[5] type: expected 'tool', got %q", evt5["type"])
	}
	if evt5["id"] != "call_1" {
		t.Fatalf("event[5] id: expected 'call_1', got %q", evt5["id"])
	}
	if evt5["tool"] != "read_file" {
		t.Fatalf("event[5] tool: expected 'read_file', got %q", evt5["tool"])
	}
	if evt5["status"] != "ok" {
		t.Fatalf("event[5] status: expected 'ok', got %q", evt5["status"])
	}
	if evt5["result"] == nil {
		t.Fatal("event[5] result should be present for ok status")
	}

	// 7. Second tool result (error)
	evt6 := resp.Events[6]
	if evt6["type"] != "tool" {
		t.Fatalf("event[6] type: expected 'tool', got %q", evt6["type"])
	}
	if evt6["id"] != "call_2" {
		t.Fatalf("event[6] id: expected 'call_2', got %q", evt6["id"])
	}
	if evt6["tool"] != "search" {
		t.Fatalf("event[6] tool: expected 'search', got %q", evt6["tool"])
	}
	if evt6["status"] != "err" {
		t.Fatalf("event[6] status: expected 'err', got %q", evt6["status"])
	}
	if evt6["error"] == nil {
		t.Fatal("event[6] error should be present for err status")
	}

	// 8. Final assistant delta
	evt7 := resp.Events[7]
	if evt7["type"] != "delta" {
		t.Fatalf("event[7] type: expected 'delta', got %q", evt7["type"])
	}
	if evt7["text"] != "Here's what I found in README.md..." {
		t.Fatalf("event[7] text: expected 'Here's what I found in README.md...', got %q", evt7["text"])
	}

	// 9. Second usage event
	evt8 := resp.Events[8]
	if evt8["type"] != "usage" {
		t.Fatalf("event[8] type: expected 'usage', got %q", evt8["type"])
	}
	if evt8["total_tokens"] != float64(30) {
		t.Fatalf("event[8] total_tokens: expected 30, got %v", evt8["total_tokens"])
	}

	// 10. Done marker (no text, no stats — just a terminal)
	evt9 := resp.Events[9]
	if evt9["type"] != "done" {
		t.Fatalf("event[9] type: expected 'done', got %q", evt9["type"])
	}
	if _, hasText := evt9["text"]; hasText {
		t.Fatal("event[9] done should not have text field in history replay")
	}
	if _, hasStats := evt9["stats"]; hasStats {
		t.Fatal("event[9] done should not have stats field in history replay")
	}

	// --- Verify events carry timestamp (ts) ---
	for i, evt := range resp.Events {
		if evt["type"] == "done" {
			continue // done marker doesn't carry ts
		}
		if _, hasTS := evt["ts"]; !hasTS {
			t.Fatalf("event[%d] (%s) should have ts field", i, evt["type"])
		}
	}

	// --- Messages field should NOT be present (only events are used) ---
	if _, exists := rawMap["messages"]; exists {
		t.Fatal("expected messages field to be absent (use events instead)")
	}
}

func TestFrameForEvent_SessionCreated(t *testing.T) {
	childID := "child-abc"
	parentID := "parent-xyz"
	forkPoint := "fp-1"
	evt := event.SessionCreated{
		SessionID: childID,
		Session: map[string]any{
			"id":         childID,
			"parent_id":  parentID,
			"fork_point": forkPoint,
			"name":       "child-session",
			"short_id":   childID[:8],
			"model":      "deepseek",
			"message_count": 3,
		},
	}

	w := &spyWriter{}
	processEvent(w, evt)

	frames := w.Frames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	fr := frames[0]

	if fr.Type != "session" {
		t.Fatalf("expected type 'session', got %q", fr.Type)
	}
	if fr.Event != "session_create" {
		t.Fatalf("expected event 'session_create', got %q", fr.Event)
	}
	if fr.SessionID != childID {
		t.Fatalf("expected session_id %q, got %q", childID, fr.SessionID)
	}
	if fr.Session == nil {
		t.Fatal("expected session metadata map")
	}
	if fr.Session["id"] != childID {
		t.Fatalf("expected session.id %q, got %v", childID, fr.Session["id"])
	}
	if fr.Session["parent_id"] != parentID {
		t.Fatalf("expected session.parent_id %q, got %v", parentID, fr.Session["parent_id"])
	}
	if fr.Session["fork_point"] != forkPoint {
		t.Fatalf("expected session.fork_point %q, got %v", forkPoint, fr.Session["fork_point"])
	}
}

package gateways

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// v2 protocol tests
//
// These tests define the expected behaviour of the new wire protocol.
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
// status, without the client having to send list_sessions first.
func TestWelcomeOnConnect(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Read the first frame — it should be a welcome without sending anything.
	var welcome struct {
		Type            string `json:"type"`
		ProtocolVersion string `json:"protocol_version"`
		Data            map[string]any `json:"data,omitempty"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)

	if welcome.Type != "welcome" {
		t.Fatalf("expected type 'welcome', got %q", welcome.Type)
	}
	if welcome.ProtocolVersion != "2" {
		t.Fatalf("expected protocol_version '2', got %q", welcome.ProtocolVersion)
	}

	// Verify sessions list with in_flight field
	if welcome.Data != nil {
		if sessions, ok := welcome.Data["sessions"].([]any); ok {
			for _, s := range sessions {
				if sess, ok := s.(map[string]any); ok {
					// Should have in_flight field
					if _, exists := sess["in_flight"]; !exists {
						t.Error("expected in_flight field in session entry")
					}
				}
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
// statistics embedded directly, not as a separate meta event.
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
	if fr.Output != "Hello!" {
		t.Fatalf("expected output 'Hello!', got %q", fr.Output)
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
	if !chat.Stream {
		t.Fatal("expected stream true")
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

// TestWebSocketV2RoundTrip tests a full chat turn using the new v2 protocol.
// Sends type:"chat", receives type:"done" with embedded stats.
func TestWebSocketV2RoundTrip(t *testing.T) {
	conv, streamer, cmd := newNamedGatewayComponents("hello from llm", "v2test")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
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
		"stream": false,
	})

	// Read frames until done
	var doneOutput string
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
			Type   string          `json:"type"`
			Output string          `json:"output"`
			Error  string          `json:"error"`
			Stats  json.RawMessage `json:"stats"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal frame %d: %v", i, err)
		}
		if msg.Type == "done" {
			doneOutput = msg.Output
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
	if doneOutput != "hello from llm" {
		t.Fatalf("expected output %q, got %q", "hello from llm", doneOutput)
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
	conv, streamer, cmd := newNamedGatewayComponents("response from running turn", "autosub")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Connect and verify welcome includes session listing.
	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	var welcome struct {
		Type            string `json:"type"`
		ProtocolVersion string `json:"protocol_version"`
		Data            map[string]any `json:"data"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)
	if welcome.Type != "welcome" {
		t.Fatalf("expected welcome, got %q", welcome.Type)
	}
	if welcome.ProtocolVersion != "2" {
		t.Fatalf("expected protocol_version '2', got %q", welcome.ProtocolVersion)
	}

	// The welcome should include sessions (even if empty)
	if welcome.Data == nil {
		t.Fatal("expected data in welcome frame")
	}
	if _, ok := welcome.Data["sessions"]; !ok {
		t.Fatal("expected sessions in welcome data")
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

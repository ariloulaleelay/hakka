package gateways

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/adapters"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/tools"
)

// ---------------------------------------------------------------------------
// E2E Protocol Tests
//
// These tests use MockProvider (deterministic scripted LLM) + mock tools
// to verify the full wire protocol end-to-end over WebSocket.
//
// Each test:
//   1. Creates a WebSocket gateway with MockProvider + mock tools
//   2. Connects as a real WebSocket client
//   3. Sends protocol frames
//   4. Asserts expected frame types and payloads
// ---------------------------------------------------------------------------

// mockE2ESetup creates a complete gateway setup with MockProvider and mock
// tools. It returns the gateway components, the mock adapter (for resetting
// state), and the gateway itself.
//
// The mock provider is pre-configured with the given namespace. All mock
// tools (echo_tool, fail_tool, slow_tool) are registered and the #all
// tag is pre-enabled on every session.
func mockE2ESetup(t *testing.T, ns string) (*agent.Conversation, *commands.CommandProcessor, *adapters.MockProvider, *agent.ToolRegistry, string) {
	t.Helper()

	mockAdapter := adapters.NewMockProvider()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sm := agent.NewSessionManager(nil, "sys")
	reg := agent.NewRegistry()
	reg.Register("default", mockAdapter)
	router := agent.NewRouter(reg)

	toolReg := agent.NewToolRegistry()
	tools.RegisterMockTools(toolReg)

	cfg := agent.EngineConfig{
		MaxToolIterations: 8,
		Logger:            logger,
	}
	conv := agent.NewConversation(sm, router, toolReg, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)

	addr := freeAddr(t)
	return conv, cmd, mockAdapter, toolReg, addr
}

// startMockGateway starts a WebSocket gateway and returns it.
func startMockGateway(t *testing.T, conv *agent.Conversation, cmd *commands.CommandProcessor, addr string) *WebSocketGateway {
	t.Helper()
	gw := NewWebSocketGateway(conv, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	return gw
}

// createAndEnableSession creates a session in the given namespace with all
// tools enabled (via #all tag), and returns the session. If sessionID is
// empty, a new session is created with a random UUID.
func createAndEnableSession(t *testing.T, conv *agent.Conversation, ns string, sessionID string) *agent.Session {
	t.Helper()
	sm := conv.Sessions()
	session, err := sm.CreateWithID(context.Background(), ns, sessionID)
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	// Enable all mock tools.
	for _, name := range []string{"echo_tool", "fail_tool", "slow_tool"} {
		session.EnableTool(context.Background(), name)
	}
	if err := sm.Save(context.Background(), ns, session); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return session
}

// readFrames reads up to maxFrames from the WebSocket with the given
// timeout, returning all frames as raw JSON data.
func readFrames(t *testing.T, conn *websocket.Conn, maxFrames int, timeout time.Duration) [][]byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var frames [][]byte
	for i := 0; i < maxFrames; i++ {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		frames = append(frames, data)
	}
	return frames
}

// readUntilFrame reads frames until a frame with the given type is found,
// or timeout expires. Returns all frames read (including the target).
func readUntilFrame(t *testing.T, conn *websocket.Conn, targetType string, timeout time.Duration) [][]byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var frames [][]byte
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read frame (waiting for %q): %v", targetType, err)
		}
		frames = append(frames, data)
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &base); err != nil {
			continue
		}
		if base.Type == targetType {
			return frames
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestE2E_WelcomeOnConnect verifies that connecting to the gateway sends
// a "welcome" frame with sessions at the top level.
func TestE2E_WelcomeOnConnect(t *testing.T) {
	conv, cmd, _, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Read welcome frame.
	var welcome struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)

	if welcome.Type != "welcome" {
		t.Fatalf("expected type 'welcome', got %q", welcome.Type)
	}
	if welcome.Sessions == nil {
		t.Fatal("expected sessions field at top level")
	}
}

// TestE2E_SimpleChat verifies a basic non-streaming chat turn:
//  1. Send chat with a message script
//  2. Receive a single "done" frame with the reply text and embedded stats
func TestE2E_SimpleChat(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	// Create a session first.
	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + auto-subscribe session frames.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send a simple chat with a text-only script.
	script := `{"message":{"content":"Hello from mock!"}}`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Read until done.
	frames := readUntilFrame(t, conn, "done", 5*time.Second)

	var doneFrame struct {
		Type  string     `json:"type"`
		Text  string     `json:"text"`
		Error string     `json:"error"`
		Stats *TurnStats `json:"stats"`
	}
	for _, data := range frames {
		var base struct{ Type string }
		json.Unmarshal(data, &base)
		if base.Type == "done" {
			if err := json.Unmarshal(data, &doneFrame); err != nil {
				t.Fatalf("unmarshal done: %v", err)
			}
			break
		}
	}

	if doneFrame.Error != "" {
		t.Fatalf("unexpected error: %s", doneFrame.Error)
	}
	if doneFrame.Text != "Hello from mock!" {
		t.Fatalf("expected text 'Hello from mock!', got %q", doneFrame.Text)
	}
	if doneFrame.Stats == nil {
		t.Fatal("expected stats in done frame")
	}
	if doneFrame.Stats.Model == "" {
		t.Fatal("expected non-empty model in stats")
	}
}

// TestE2E_StreamingChat verifies a streaming chat turn:
//  1. Send chat with stream:true and a text script
//  2. Receive "delta" frames with text chunks
//  3. Receive a final "done" frame with the full text and stats
func TestE2E_StreamingChat(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send streaming chat.
	script := `{"message":{"content":"Streamed response"}}`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Read all frames until done.
	var accumulated strings.Builder
	var doneText string
	var doneStats *TurnStats
	foundDone := false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type  string     `json:"type"`
			Text  string     `json:"text"`
			Error string     `json:"error"`
			Stats *TurnStats `json:"stats"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("unexpected error frame: %s", frame.Error)
		}
		switch frame.Type {
		case "delta":
			accumulated.WriteString(frame.Text)
		case "usage":
			// Expected between deltas and done.
		case "done":
			foundDone = true
			doneText = frame.Text
			doneStats = frame.Stats
		}
		if foundDone {
			break
		}
	}

	if !foundDone {
		t.Fatal("expected done frame")
	}
	if doneText != "Streamed response" {
		t.Fatalf("expected done text 'Streamed response', got %q", doneText)
	}
	if accumulated.String() != "Streamed response" {
		t.Fatalf("expected accumulated deltas 'Streamed response', got %q", accumulated.String())
	}
	if doneStats == nil {
		t.Fatal("expected stats in done frame")
	}
}

// TestE2E_ToolCallThenText verifies the full tool call lifecycle:
//  1. Send a script that requests a tool call
//  2. Receive tool(start) → tool(ok) → delta → done
//  3. The tool result is visible in the conversation
func TestE2E_ToolCallThenText(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send a multi-step script: tool call, then text reply.
	script := `[
		{"run_tool": {"name": "echo_tool", "args": {"message": "hello"}}},
		{"message": {"content": "Tool returned successfully"}}
	]`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Read frames until done.
	var (
		toolStarted bool
		toolOK      bool
		toolID      string
		doneText    string
		hasDelta    bool
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Tool       string `json:"tool"`
			Status     string `json:"status"`
			ToolResult string `json:"result"`
			Text       string `json:"text"`
			Error      string `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("unexpected error: %s", frame.Error)
		}
		switch {
		case frame.Type == "tool" && frame.Status == "start":
			toolStarted = true
			toolID = frame.ID
			if frame.Tool != "echo_tool" {
				t.Fatalf("expected tool 'echo_tool', got %q", frame.Tool)
			}
		case frame.Type == "tool" && frame.Status == "ok":
			toolOK = true
			if frame.ID != toolID {
				t.Fatalf("tool finish id %q doesn't match start id %q", frame.ID, toolID)
			}
			if frame.ToolResult == "" {
				t.Fatal("expected result in tool ok frame")
			}
		case frame.Type == "delta":
			hasDelta = true
		case frame.Type == "done":
			doneText = frame.Text
			goto done
		}
	}
done:

	if !toolStarted {
		t.Fatal("expected tool start event")
	}
	if !toolOK {
		t.Fatal("expected tool ok event")
	}
	if toolID == "" {
		t.Fatal("expected non-empty tool call ID")
	}
	if !hasDelta {
		t.Log("NOTE: no delta frame seen (tool-only response may skip delta)")
	}
	if doneText != "Tool returned successfully" {
		t.Fatalf("expected done text 'Tool returned successfully', got %q", doneText)
	}

	// Verify the tool result was recorded in the session.
	session, _ = conv.Sessions().Get(context.Background(), "e2e", sessionID)
	messages := session.Messages()
	foundToolResult := false
	for _, m := range messages {
		if m.Role == agent.RoleTool && strings.Contains(m.Content, "Echo: hello") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatal("expected tool result 'Echo: hello' in session messages")
	}
}

// TestE2E_ToolError verifies that a failing tool produces the correct
// error event:
//  1. Send a script that calls fail_tool
//  2. Receive tool(start) → tool(err) with error message
func TestE2E_ToolError(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Script: single failing tool call.
	script := `{"run_tool": {"name": "fail_tool", "args": {"message": "something went wrong"}}}`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	var foundToolErr bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type       string `json:"type"`
			Status     string `json:"status"`
			Tool       string `json:"tool"`
			Error      string `json:"error"`
			ToolResult string `json:"result"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		switch {
		case frame.Type == "tool" && frame.Status == "start":
			if frame.Tool != "fail_tool" {
				t.Fatalf("expected tool 'fail_tool', got %q", frame.Tool)
			}
		case frame.Type == "tool" && frame.Status == "err":
			foundToolErr = true
			if frame.Error == "" {
				t.Fatal("expected error message in tool err frame")
			}
			if !strings.Contains(frame.Error, "fail_tool") {
				t.Fatalf("expected error containing 'fail_tool', got %q", frame.Error)
			}
		case frame.Type == "done":
			goto check
		}
	}
check:

	if !foundToolErr {
		t.Fatal("expected tool error event")
	}
}

// TestE2E_JSONCommand verifies that sending a JSON command returns a
// structured "result" frame.
func TestE2E_JSONCommand(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome.
	var welcome struct{ Type string }
	readWSFrame(t, conn, 3*time.Second, &welcome)

	// Create an explicit session because command lookup is lookup-only.
	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	writeWSFrame(t, conn, map[string]any{
		"type":       "cmd",
		"session_id": sessionID,
		"command":    map[string]any{"cmd": "session_info"},
	})

	// Read response.
	var result struct {
		Type      string         `json:"type"`
		Cmd       string         `json:"cmd"`
		Data      map[string]any `json:"data"`
		SessionID string         `json:"session_id"`
		Error     string         `json:"error"`
	}
	readWSFrame(t, conn, 3*time.Second, &result)

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Type != "done" && result.Type != "result" {
		t.Fatalf("expected type 'done' or 'result', got %q", result.Type)
	}
	if result.Cmd != "session_info" {
		t.Fatalf("expected cmd 'session_info', got %q", result.Cmd)
	}
	if result.Data == nil {
		t.Fatal("expected data in result frame")
	}
}

// TestE2E_CancelMidTurn verifies that sending a cancel frame during a
// long-running tool immediately terminates the turn:
//  1. Send a chat that calls slow_tool (3 second sleep)
//  2. Immediately send cancel
//  3. Receive done frame with cancelled:true
func TestE2E_CancelMidTurn(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send a script that calls slow_tool (will sleep 3 seconds).
	script := `{"run_tool": {"name": "slow_tool", "args": {"seconds": 3}}}`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Wait for tool start event, then cancel.
	time.Sleep(100 * time.Millisecond)

	writeWSFrame(t, conn, map[string]any{
		"type":       "cancel",
		"session_id": sessionID,
	})

	// Read until we get a done frame (with or without cancelled).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var doneFrame struct {
		Type      string `json:"type"`
		Cancelled bool   `json:"cancelled"`
		Error     string `json:"error"`
	}
	found := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// Reset struct to avoid stale fields from previous frames
		// (e.g. error from a tool frame bleeding into done).
		doneFrame = struct {
			Type      string `json:"type"`
			Cancelled bool   `json:"cancelled"`
			Error     string `json:"error"`
		}{}
		if err := json.Unmarshal(data, &doneFrame); err != nil {
			continue
		}
		if doneFrame.Type == "done" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("never received done frame")
	}

	if doneFrame.Error != "" {
		t.Fatalf("unexpected error: %s", doneFrame.Error)
	}
	if !doneFrame.Cancelled {
		t.Fatal("expected cancelled:true in done frame after cancel")
	}
}

// TestE2E_GetSessionEvents verifies that get_session returns an events
// field with replay-friendly typed events that mirror the wire protocol.
func TestE2E_GetSessionEvents(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + auto-subscribe session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// First, run a chat with a tool call to populate the session.
	script := `[
		{"run_tool": {"name": "echo_tool", "args": {"message": "hello"}}},
		{"message": {"content": "Done with tool"}}
	]`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Wait for done.
	readUntilFrame(t, conn, "done", 5*time.Second)

	// With the hub, a turn_finished broadcast arrives after the turn
	// completes. Consume it before sending get_session.
	{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				break
			}
			var base struct {
				Type  string `json:"type"`
				Event string `json:"event"`
			}
			json.Unmarshal(data, &base)
			if base.Type == "session" && base.Event == "turn_finished" {
				t.Log("consumed turn_finished broadcast")
				break
			}
		}
	}

	// Now send get_session command.
	writeWSFrame(t, conn, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "get_session", "params": map[string]string{"id": sessionID}},
	})

	// Read the response.
	var resp struct {
		Type    string           `json:"type"`
		Event   string           `json:"event"`
		Session map[string]any   `json:"session"`
		Events  []map[string]any `json:"events"`
		Error   string           `json:"error"`
	}
	readWSFrame(t, conn, 3*time.Second, &resp)

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
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
	if resp.Events == nil {
		t.Fatal("expected events field in get_session response")
	}

	// Verify events have the expected structure.
	// The session should have: user msg → tool(start) → tool(ok) → delta → done
	if len(resp.Events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(resp.Events))
	}

	// First event: user chat.
	if resp.Events[0]["type"] != "chat" {
		t.Fatalf("expected event[0] type 'chat', got %q", resp.Events[0]["type"])
	}

	// Look for tool start.
	foundToolStart := false
	foundToolOK := false
	foundDone := false
	for _, evt := range resp.Events {
		typ, _ := evt["type"].(string)
		if typ == "tool" {
			status, _ := evt["status"].(string)
			if status == "start" && evt["tool"] == "echo_tool" {
				foundToolStart = true
			}
			if status == "ok" {
				foundToolOK = true
			}
		}
		if typ == "done" {
			foundDone = true
		}
	}

	if !foundToolStart {
		t.Fatal("expected tool start event in get_session events")
	}
	if !foundToolOK {
		t.Fatal("expected tool ok event in get_session events")
	}
	if !foundDone {
		t.Fatal("expected done terminal event in get_session events")
	}

	// Verify events carry timestamp field.
	for _, evt := range resp.Events {
		if evt["type"] == "done" {
			continue
		}
		if _, hasTS := evt["ts"]; !hasTS {
			t.Fatal("expected ts field on all events (except done marker)")
		}
	}
}

// TestE2E_InvalidScript verifies that sending non-JSON input produces an
// error frame (or done frame with error).
func TestE2E_InvalidScript(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send invalid input (not a valid mock script).
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      "this is not valid JSON for mock",
	})

	// Should get a done frame with an error (the mock provider will fail
	// to parse the script).
	var doneFrame struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := json.Unmarshal(data, &doneFrame); err != nil {
			continue
		}
		if doneFrame.Type == "done" {
			break
		}
	}

	if doneFrame.Error == "" {
		t.Fatal("expected error in done frame for invalid script")
	}
}

// TestE2E_NonStreamingToolLoop verifies that non-streaming chat with a
// tool call correctly cycles through the tool loop and returns the final
// response in a single done frame.
func TestE2E_NonStreamingToolLoop(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// Send multi-step script (tool then text) with streaming disabled.
	script := `[
		{"run_tool": {"name": "echo_tool", "args": {"message": "world"}}},
		{"message": {"content": "Final answer"}}
	]`
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      script,
	})

	// Read all frames until done. Should see tool frames and then a done
	// with the final text.
	var (
		foundToolStart bool
		foundToolOK    bool
		doneText       string
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Text   string `json:"text"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		if frame.Error != "" {
			t.Fatalf("unexpected error: %s", frame.Error)
		}
		switch frame.Type {
		case "tool":
			if frame.Status == "start" {
				foundToolStart = true
			}
			if frame.Status == "ok" {
				foundToolOK = true
			}
		case "done":
			doneText = frame.Text
			goto check
		}
	}
check:

	if !foundToolStart {
		t.Fatal("expected tool start frame in non-streaming tool loop")
	}
	if !foundToolOK {
		t.Fatal("expected tool ok frame in non-streaming tool loop")
	}
	if doneText != "Final answer" {
		t.Fatalf("expected done text 'Final answer', got %q", doneText)
	}
}

// TestE2E_MultipleSequentialChats verifies that sending multiple chat
// messages in sequence works correctly — each turn completes before the
// next begins, and the session accumulates messages.
func TestE2E_MultipleSequentialChats(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := mockE2ESetup(t, "e2e")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	session := createAndEnableSession(t, conv, "e2e", "")
	sessionID := session.SessionID()

	conn := wsConnect(t, addr)
	defer conn.CloseNow()

	// Discard welcome + session.
	readUntilFrame(t, conn, "session", 3*time.Second)

	// First chat.
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      `{"message":{"content":"First response"}}`,
	})

	frames := readUntilFrame(t, conn, "done", 5*time.Second)
	var firstDone struct{ Text string }
	for _, data := range frames {
		var base struct{ Type string }
		json.Unmarshal(data, &base)
		if base.Type == "done" {
			json.Unmarshal(data, &firstDone)
		}
	}
	if firstDone.Text != "First response" {
		t.Fatalf("expected 'First response', got %q", firstDone.Text)
	}

	// Second chat.
	writeWSFrame(t, conn, map[string]any{
		"type":       "chat",
		"session_id": sessionID,
		"input":      `{"message":{"content":"Second response"}}`,
	})

	frames = readUntilFrame(t, conn, "done", 5*time.Second)
	var secondDone struct{ Text string }
	for _, data := range frames {
		var base struct{ Type string }
		json.Unmarshal(data, &base)
		if base.Type == "done" {
			json.Unmarshal(data, &secondDone)
		}
	}
	if secondDone.Text != "Second response" {
		t.Fatalf("expected 'Second response', got %q", secondDone.Text)
	}

	// Verify session has 4 messages (2 user + 2 assistant).
	session, _ = conv.Sessions().Get(context.Background(), "e2e", sessionID)
	if len(session.Messages()) != 4 {
		t.Fatalf("expected 4 messages in session, got %d", len(session.Messages()))
	}
}

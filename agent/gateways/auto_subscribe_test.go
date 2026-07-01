package gateways

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// TestConnectPushFrames verifies what frames the server sends immediately
// on connect (welcome + potentially auto-subscribe frames).
func TestConnectPushFrames(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// First create a session so auto-subscribe has something to do
	func() {
		c := wsConnect(t, addr)
		defer c.CloseNow()

		var f struct{ Type string }
		readWSFrame(t, c, 3*time.Second, &f) // welcome

		writeWSFrame(t, c, map[string]any{
			"type": "cmd",
			"command": map[string]any{"cmd": "session_create", "params": map[string]any{}},
		})
		var r struct{ Type string }
		readWSFrame(t, c, 3*time.Second, &r) // session frame
		t.Logf("session_create response type=%s", r.Type)
	}()

	// Now manually check how many sessions exist
	ns := gw.Handler.Namespace
	t.Logf("namespace: %s", ns)
	nsCtx := event.ContextWithNamespace(context.Background(), ns)
	sessions, err := gw.Handler.Conv.Sessions().List(nsCtx, ns)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	t.Logf("sessions count: %d", len(sessions))
	for _, s := range sessions {
		t.Logf("  session: %s (%s)", s.SessionID(), s.SessionName())
	}

	// Now connect again — should get welcome + auto-subscribe frames
	c2 := wsConnect(t, addr)
	defer c2.CloseNow()

	for i := 0; i < 5; i++ {
		var f struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Cmd   string `json:"cmd"`
			Text  string `json:"text"`
		}
		err := readWSFrameWithTimeout(t, c2, time.Second, &f)
		if err != "" {
			t.Logf("Frame #%d: (no more frames: %s)", i, err)
			break
		}
		t.Logf("Frame #%d: type=%s event=%s cmd=%s text=%q", i, f.Type, f.Event, f.Cmd, f.Text)
	}
}

// TestAutoSubscribeDoesNotInterfereWithCommands verifies that when a new
// WebSocket connection is established (like the Lua client does for every
// command), the auto-subscription "get_session" frame does NOT swallow the
// actual command response. The client must receive the correct command
// result (e.g. session_create → type="session" event="session_create"),
// not the auto-subscription frame.
func TestAutoSubscribeDoesNotInterfereWithCommands(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Step 1: First connection — create a session so auto-subscribe has
	// something to subscribe to on subsequent connections.
	sessionCreate := func() string {
		c := wsConnect(t, addr)
		defer c.CloseNow()

		readWSFrame(t, c, 3*time.Second, new(struct{ Type string })) // welcome

		writeWSFrame(t, c, map[string]any{
			"type":    "cmd",
			"command": map[string]any{"cmd": "session_create", "params": map[string]any{}},
		})

		var createRes struct {
			Type    string         `json:"type"`
			Event   string         `json:"event"`
			Session map[string]any `json:"session"`
		}
		readWSFrame(t, c, 3*time.Second, &createRes)
		if createRes.Session != nil {
			if id, ok := createRes.Session["id"].(string); ok {
				return id
			}
		}
		return ""
	}

	firstSessionID := sessionCreate()
	t.Logf("created first session: %s", firstSessionID)
	if firstSessionID == "" {
		t.Fatal("failed to get first session ID")
	}

	// Step 2: New connection (like Lua client opens for each command).
	// The server sends welcome + auto-subscribe get_session frames.
	c := wsConnect(t, addr)
	defer c.CloseNow()

	// Consume welcome frame
	var welcome struct{ Type string }
	readWSFrame(t, c, 3*time.Second, &welcome)
	if welcome.Type != "welcome" {
		t.Fatalf("expected welcome, got type=%q", welcome.Type)
	}

	// Consume auto-subscribe get_session frame
	var autoSub struct {
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	readWSFrame(t, c, 3*time.Second, &autoSub)
	if autoSub.Type != "session" {
		t.Fatalf("expected auto-subscribe type 'session', got type=%q", autoSub.Type)
	}
	if autoSub.Event != "get_session" {
		t.Fatalf("expected auto-subscribe event %q, got event=%q", "get_session", autoSub.Event)
	}

	// Now send a command — this simulates what the Lua client does AFTER
	// skipping the welcome and auto-subscribe frames.
	writeWSFrame(t, c, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "session_info"},
	})

	// Read the response — must be a "result" frame for session_info,
	// NOT another "session" frame.
	var result struct {
		Type  string         `json:"type"`
		Cmd   string         `json:"cmd"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	readWSFrame(t, c, 3*time.Second, &result)
	if result.Type != "result" {
		t.Fatalf("expected command response type 'result', got type=%q", result.Type)
	}
	if result.Cmd != "session_info" {
		t.Fatalf("expected cmd 'session_info', got cmd=%q", result.Cmd)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Data == nil {
		t.Fatal("expected data in session_info response")
	}
	if result.Data["session"] == nil {
		t.Fatal("expected session field in session_info data")
	}
}

// TestAutoSubscribeSessionCreate verifies that sending a session_create
// command on a new connection (after auto-subscription frames have been
// consumed) correctly creates a new session instead of returning the
// old session's data.
func TestAutoSubscribeSessionCreate(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Step 1: Create an initial session so there's a "best session".
	var firstSessionID string
	func() {
		c := wsConnect(t, addr)
		defer c.CloseNow()

		readWSFrame(t, c, 3*time.Second, new(struct{ Type string })) // welcome

		writeWSFrame(t, c, map[string]any{
			"type":    "cmd",
			"command": map[string]any{"cmd": "session_create", "params": map[string]any{}},
		})

		var sessionRes struct {
			Type    string         `json:"type"`
			Event   string         `json:"event"`
			Session map[string]any `json:"session"`
		}
		readWSFrame(t, c, 3*time.Second, &sessionRes)
		if sessionRes.Session != nil {
			if id, ok := sessionRes.Session["id"].(string); ok {
				firstSessionID = id
			}
		}
		t.Logf("created first session: %s", firstSessionID)
	}()

	if firstSessionID == "" {
		t.Fatal("failed to get first session ID")
	}

	// Step 2: New connection — consume auto-subscribe, then send
	// session_create.
	c := wsConnect(t, addr)
	defer c.CloseNow()

	readWSFrame(t, c, 3*time.Second, new(struct{ Type string })) // welcome
	readWSFrame(t, c, 3*time.Second, new(struct{ Type string })) // get_session

	writeWSFrame(t, c, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "session_create", "params": map[string]any{}},
	})

	// Read the response — must be a "session" frame with event="session_create",
	// with a DIFFERENT session ID from the first session.
	var createRes struct {
		Type    string         `json:"type"`
		Event   string         `json:"event"`
		Session map[string]any `json:"session"`
	}
	readWSFrame(t, c, 3*time.Second, &createRes)
	if createRes.Type != "session" {
		t.Fatalf("expected type 'session', got type=%q", createRes.Type)
	}
	if createRes.Event != "session_create" {
		t.Fatalf("expected event 'session_create', got event=%q", createRes.Event)
	}
	if createRes.Session == nil {
		t.Fatal("expected session data in session_create response at top level")
	}

	newSessionID, ok := createRes.Session["id"].(string)
	if !ok || newSessionID == "" {
		t.Fatal("no session ID in session_create response")
	}
	if newSessionID == firstSessionID {
		t.Fatal("session_create returned the same session ID — expected a NEW session")
	}
	t.Logf("new session ID: %s (different from first: %s)", newSessionID, firstSessionID)
}

func readWSFrameWithTimeout(t *testing.T, conn *websocket.Conn, timeout time.Duration, dst any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return err.Error()
	}
	return ""
}

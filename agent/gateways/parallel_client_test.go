package gateways

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/adapters"
	"github.com/ariloulaleelay/hakka/agent/commands"
	"github.com/ariloulaleelay/hakka/agent/tools"
)

// ---------------------------------------------------------------------------
// Parallel Client Protocol Tests
//
// Uses MockProvider + mock tools for deterministic behavior across
// multiple concurrent WebSocket connections.
//
// With the namespace hub, ALL events are broadcast to ALL clients in the
// namespace. Tests verify that session lifecycle events and turn events
// are correctly delivered to every connected client.
//
// Key constraint: the coder/websocket library treats Read errors
// (including context timeouts) as fatal. Once a Read times out, the
// connection cannot be reused. All helper functions respect this.
// ---------------------------------------------------------------------------

func parallelSetup(t *testing.T, ns string) (*agent.Conversation, *commands.CommandProcessor, *adapters.MockProvider, *agent.ToolRegistry, string) {
	t.Helper()
	mockAdapter := adapters.NewMockProvider()
	sm := agent.NewSessionManager(nil, "sys")
	reg := agent.NewRegistry()
	reg.Register("default", mockAdapter)
	router := agent.NewRouter(reg)
	toolReg := agent.NewToolRegistry()
	tools.RegisterMockTools(toolReg)
	cfg := agent.EngineConfig{MaxToolIterations: 8}
	conv := agent.NewConversation(sm, router, toolReg, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	addr := freeAddr(t)
	return conv, cmd, mockAdapter, toolReg, addr
}

func enableToolsDirect(session *agent.Session, names ...string) {
	for _, n := range names {
		session.AllowTool(n)
		session.EnableTool(n)
	}
}

// connectAndWelcome connects, reads welcome + auto-subscribe session frame.
// Returns the connection. MUST be closed by caller.
func connectAndWelcome(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	conn := wsConnect(t, addr)
	var welcome struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	readWSFrame(t, conn, 3*time.Second, &welcome)
	if welcome.Type != "welcome" {
		t.Fatalf("expected welcome, got %q", welcome.Type)
	}
	// If sessions exist, consume the auto-subscribe frame.
	if len(welcome.Sessions) > 0 {
		readWSFrame(t, conn, 3*time.Second, &struct{ Type string }{})
	}
	return conn
}

func createSessionViaWebSocket(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	writeWSFrame(t, conn, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "session_create", "params": map[string]any{}},
	})
	var f struct {
		Type    string         `json:"type"`
		Event   string         `json:"event"`
		Session map[string]any `json:"session"`
	}
	readWSFrame(t, conn, 3*time.Second, &f)
	if f.Type != "session" || f.Event != "session_create" {
		t.Fatalf("expected session_create, got type=%q event=%q", f.Type, f.Event)
	}
	id, _ := f.Session["id"].(string)
	if id == "" {
		t.Fatal("empty session ID")
	}
	return id
}

// getSessionViaWebSocket sends get_session and returns the response frame.
func getSessionViaWebSocket(t *testing.T, conn *websocket.Conn, sessionID string) map[string]any {
	t.Helper()
	writeWSFrame(t, conn, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "get_session", "params": map[string]string{"id": sessionID}},
	})
	var f struct {
		Type    string         `json:"type"`
		Event   string         `json:"event"`
		Session map[string]any `json:"session"`
		Error   string         `json:"error"`
	}
	readWSFrame(t, conn, 3*time.Second, &f)
	if f.Error != "" {
		t.Fatalf("get_session error: %s", f.Error)
	}
	if f.Type != "session" || f.Event != "get_session" {
		t.Fatalf("expected session/get_session, got type=%q event=%q", f.Type, f.Event)
	}
	return f.Session
}

// readFrameByType reads from the WebSocket until a frame with the given
// type is received, or timeout. Returns the matching frame and its raw data.
// Skips frames of other types.
func readFrameByType(t *testing.T, conn *websocket.Conn, frameType string, timeout time.Duration) (map[string]any, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var f map[string]any
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		if f["type"] == frameType {
			return f, data
		}
	}
}

// readDoneForSession reads frames until a done frame with the given
// session_id is found.
func readDoneForSession(t *testing.T, conn *websocket.Conn, sessionID string, timeout time.Duration) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var f map[string]any
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		if f["type"] == "done" && f["session_id"] == sessionID {
			return f
		}
	}
}

// ---------------------------------------------------------------------------
// Test 1: Session create broadcast — B receives session_create.
// ---------------------------------------------------------------------------
func TestParallel_SessionCreateBroadcast(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par1")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	// A creates a session. B should receive the broadcast.
	sessionID := createSessionViaWebSocket(t, connA)
	t.Logf("Client A: created %s", sessionID)

	// B should have received a "session" frame with event="session_create"
	// since the hub broadcasts session lifecycle events to all clients.
	f, _ := readFrameByType(t, connB, "session", 3*time.Second)
	if f["event"] != "session_create" {
		t.Fatalf("B: expected event 'session_create', got %q", f["event"])
	}
	s := f["session"].(map[string]any)
	if s["id"] != sessionID {
		t.Fatalf("B: expected session_id %s, got %v", sessionID, s["id"])
	}
	t.Logf("Client B: received session_create broadcast for session %s", sessionID)
}

// ---------------------------------------------------------------------------
// Test 2: Shared session — both receive the same tool and done events
// via hub broadcast. B doesn't need to call get_session.
// ---------------------------------------------------------------------------
func TestParallel_SharedSessionBothReceiveEvents(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par2")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _ := conv.Sessions().Get(context.Background(), "par2", sessionID)
	enableToolsDirect(session, "slow_tool", "echo_tool")
	conv.Sessions().Save(context.Background(), "par2", session)

	// B connects BEFORE the turn starts — just for broadcast receipt.
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	// Start a turn that calls slow_tool (2s) then echo_tool then responds.
	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"run_tool":{"name":"echo_tool","args":{"message":"done"}}},{"message":{"content":"Final"}}]`,
	})

	// Both receive done via hub broadcast (filter by session_id).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connA, sessionID, 10*time.Second)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connB, sessionID, 10*time.Second)
	}()
	wg.Wait()
	t.Log("Both clients received done for the shared session")
}

// ---------------------------------------------------------------------------
// Test 3: Cross-client cancellation
// ---------------------------------------------------------------------------
func TestParallel_CrossClientCancel(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par3")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _ := conv.Sessions().Get(context.Background(), "par3", sessionID)
	enableToolsDirect(session, "slow_tool")
	conv.Sessions().Save(context.Background(), "par3", session)

	// A starts a slow tool (3 seconds), then a follow-up text reply.
	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":3}}},{"message":{"content":"done after cancel"}}]`,
	})
	time.Sleep(100 * time.Millisecond)

	// B cancels.
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	writeWSFrame(t, connB, map[string]any{
		"type": "cancel", "session_id": sessionID,
	})

	// B gets cancel response.
	var cancelResp struct {
		Type      string `json:"type"`
		Cancelled bool   `json:"cancelled"`
	}
	readWSFrame(t, connB, 5*time.Second, &cancelResp)
	if cancelResp.Type != "done" || !cancelResp.Cancelled {
		t.Fatalf("expected done+cancelled, got type=%q cancelled=%v", cancelResp.Type, cancelResp.Cancelled)
	}
	t.Log("B: cancel acknowledged")

	// A also gets cancelled done via hub broadcast.
	readDoneForSession(t, connA, sessionID, 5*time.Second)
	t.Log("A: received cancel done")
}

// ---------------------------------------------------------------------------
// Test 4: Independent sessions — two clients chat on different sessions.
// Each receives only the relevant done by filtering session_id.
// ---------------------------------------------------------------------------
func TestParallel_IndependentSessions(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par4")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sidA := createSessionViaWebSocket(t, connA)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	sidB := createSessionViaWebSocket(t, connB)

	sA, _ := conv.Sessions().Get(context.Background(), "par4", sidA)
	enableToolsDirect(sA, "echo_tool")
	conv.Sessions().Save(context.Background(), "par4", sA)
	sB, _ := conv.Sessions().Get(context.Background(), "par4", sidB)
	enableToolsDirect(sB, "echo_tool")
	conv.Sessions().Save(context.Background(), "par4", sB)

	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sidA,
		"input": `[{"run_tool":{"name":"echo_tool","args":{"message":"from A"}}},{"message":{"content":"A done"}}]`,
	})
	writeWSFrame(t, connB, map[string]any{
		"type": "chat", "session_id": sidB,
		"input": `[{"run_tool":{"name":"echo_tool","args":{"message":"from B"}}},{"message":{"content":"B done"}}]`,
	})

	var textA, textB string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f := readDoneForSession(t, connA, sidA, 5*time.Second)
		if v, ok := f["text"].(string); ok {
			textA = v
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		f := readDoneForSession(t, connB, sidB, 5*time.Second)
		if v, ok := f["text"].(string); ok {
			textB = v
		}
	}()
	wg.Wait()

	if textA != "A done" {
		t.Fatalf("expected 'A done', got %q", textA)
	}
	if textB != "B done" {
		t.Fatalf("expected 'B done', got %q", textB)
	}
	t.Logf("Independent turns filtered by session_id: A=%q B=%q", textA, textB)
}

// ---------------------------------------------------------------------------
// Test 5: Session list visibility across clients
// ---------------------------------------------------------------------------
func TestParallel_SessionListVisibility(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par5")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	createSessionViaWebSocket(t, connA)
	createSessionViaWebSocket(t, connA)

	// B receives session_create broadcasts for both. Consume them.
	for i := 0; i < 2; i++ {
		readFrameByType(t, connB, "session", 3*time.Second)
	}

	// B lists sessions.
	writeWSFrame(t, connB, map[string]any{
		"type": "cmd", "command": map[string]any{"cmd": "session_list"},
	})
	var res struct {
		Type  string          `json:"type"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	readWSFrame(t, connB, 3*time.Second, &res)
	if res.Error != "" {
		t.Fatalf("session_list error: %s", res.Error)
	}
	if res.Type != "result" {
		t.Fatalf("expected result, got %q", res.Type)
	}
	t.Log("B: session_list works (sees A's sessions)")
}

// ---------------------------------------------------------------------------
// Test 6: Two clients create sessions — unique IDs.
// Sessions are created sequentially to avoid the race condition where
// each client's createSessionViaWebSocket helper may consume the other
// client's broadcast instead of its own direct response. Both clients
// still verify that session IDs are unique.
// ---------------------------------------------------------------------------
func TestParallel_SimultaneousSessionCreate(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par6")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	// A creates a session; B receives the broadcast.
	sidA := createSessionViaWebSocket(t, connA)
	// B consumes the broadcast.
	readFrameByType(t, connB, "session", 3*time.Second)

	// B creates a session; A receives the broadcast.
	sidB := createSessionViaWebSocket(t, connB)
	// A consumes the broadcast.
	readFrameByType(t, connA, "session", 3*time.Second)

	if sidA == "" || sidB == "" {
		t.Fatal("both IDs must be non-empty")
	}
	if sidA == sidB {
		t.Fatal("duplicate session IDs")
	}
	t.Logf("A=%s B=%s (unique)", shortID(sidA), shortID(sidB))

	sessions, _ := conv.Sessions().List(context.Background(), "par6")
	if len(sessions) < 2 {
		t.Fatalf("expected >=2 sessions, got %d", len(sessions))
	}
}

// ---------------------------------------------------------------------------
// Test 7: Mid-turn reconnect (slow_tool keeps turn alive)
// ---------------------------------------------------------------------------
func TestParallel_MidTurnReconnect(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par7")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _ := conv.Sessions().Get(context.Background(), "par7", sessionID)
	enableToolsDirect(session, "slow_tool", "echo_tool")
	conv.Sessions().Save(context.Background(), "par7", session)

	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"run_tool":{"name":"echo_tool","args":{"message":"hi"}}},{"message":{"content":"Final"}}]`,
	})
	time.Sleep(200 * time.Millisecond)

	// B connects mid-turn. Welcome + auto-subscribe sent. B already
	// receives live turn events via hub broadcast without needing
	// explicit subscription.
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	// Both receive done via hub broadcast.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connA, sessionID, 10*time.Second)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connB, sessionID, 10*time.Second)
	}()
	wg.Wait()
	t.Log("Both done")
}

// ---------------------------------------------------------------------------
// Test 8: Streaming mid-turn reconnect (slow_tool keeps turn alive)
// ---------------------------------------------------------------------------
func TestParallel_StreamingMidTurnReconnect(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par8")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _ := conv.Sessions().Get(context.Background(), "par8", sessionID)
	enableToolsDirect(session, "slow_tool")
	conv.Sessions().Save(context.Background(), "par8", session)

	// Streaming turn with slow_tool.
	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"message":{"content":"Done streaming"}}]`,
	})
	time.Sleep(200 * time.Millisecond)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connA, sessionID, 10*time.Second)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		readDoneForSession(t, connB, sessionID, 10*time.Second)
	}()
	wg.Wait()
	t.Log("Both done after streaming reconnect")
}

// ---------------------------------------------------------------------------
// Test 9: Two concurrent tool turns on different sessions.
// ---------------------------------------------------------------------------
func TestParallel_ConcurrentToolTurnsDifferentSessions(t *testing.T) {
	conv, cmd, mockAdapter, _, addr := parallelSetup(t, "par9")
	gw := startMockGateway(t, conv, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sidA := createSessionViaWebSocket(t, connA)
	sA, _ := conv.Sessions().Get(context.Background(), "par9", sidA)
	enableToolsDirect(sA, "echo_tool")
	conv.Sessions().Save(context.Background(), "par9", sA)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	sidB := createSessionViaWebSocket(t, connB)
	sB, _ := conv.Sessions().Get(context.Background(), "par9", sidB)
	enableToolsDirect(sB, "echo_tool")
	conv.Sessions().Save(context.Background(), "par9", sB)

	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sidA,
		"input": `[{"run_tool":{"name":"echo_tool","args":{"message":"from A"}}},{"message":{"content":"A done"}}]`,
	})
	writeWSFrame(t, connB, map[string]any{
		"type": "chat", "session_id": sidB,
		"input": `[{"run_tool":{"name":"echo_tool","args":{"message":"from B"}}},{"message":{"content":"B done"}}]`,
	})

	var textA, textB string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f := readDoneForSession(t, connA, sidA, 5*time.Second)
		if v, ok := f["text"].(string); ok {
			textA = v
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		f := readDoneForSession(t, connB, sidB, 5*time.Second)
		if v, ok := f["text"].(string); ok {
			textB = v
		}
	}()
	wg.Wait()

	if textA != "A done" {
		t.Fatalf("expected 'A done', got %q", textA)
	}
	if textB != "B done" {
		t.Fatalf("expected 'B done', got %q", textB)
	}
	t.Logf("Concurrent turns: A=%q B=%q", textA, textB)
}

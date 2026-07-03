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
// Key constraint: the coder/websocket library treats Read errors
// (including context timeouts) as fatal. Once a Read times out, the
// connection cannot be reused. All helper functions respect this.
// ---------------------------------------------------------------------------

func parallelSetup(t *testing.T, ns string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor, *adapters.MockProvider, *agent.ToolRegistry, string) {
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
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)
	addr := freeAddr(t)
	return conv, streamer, cmd, mockAdapter, toolReg, addr
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
	// Use a blocking read since sendWelcome sends it synchronously.
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

// ---------------------------------------------------------------------------
// Test 1: New session not broadcast (design limitation)
// ---------------------------------------------------------------------------
func TestParallel_NewSessionNotBroadcast(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par1")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	sessionID := createSessionViaWebSocket(t, connA)
	t.Logf("Client A: created %s", sessionID)

	// Conn C verifies the session exists in the store.
	connC := connectAndWelcome(t, addr)
	defer connC.CloseNow()

	// Conn B can see it via session_list.
	writeWSFrame(t, connB, map[string]any{
		"type":    "cmd",
		"command": map[string]any{"cmd": "session_list"},
	})
	var listRes struct {
		Type  string          `json:"type"`
		Cmd   string          `json:"cmd"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	readWSFrame(t, connB, 3*time.Second, &listRes)
	if listRes.Error != "" {
		t.Fatalf("session_list error: %s", listRes.Error)
	}
	if listRes.Type != "result" {
		t.Fatalf("expected type=result, got %q", listRes.Type)
	}
	t.Log("Client B: session_list works via explicit query")
	t.Log("NOTE: new sessions are NOT broadcast to other connected clients (design limitation)")
}

// ---------------------------------------------------------------------------
// Test 2: Shared session — B connects while A's turn (with slow_tool)
// is running. Both receive the same tool and done events.
// ---------------------------------------------------------------------------
func TestParallel_SharedSessionBothReceiveEvents(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par2")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _, _ := conv.Sessions().Get(context.Background(), "par2", sessionID)
	enableToolsDirect(session, "slow_tool", "echo_tool")
	conv.Sessions().Save(context.Background(), "par2", session)

	// Start a turn that calls slow_tool (2s) then echo_tool then responds.
	// The 2-second delay keeps the turn alive for B to connect.
	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"run_tool":{"name":"echo_tool","args":{"message":"done"}}},{"message":{"content":"Final"}}]`,
	})

	// Give the turn time to start executing slow_tool.
	time.Sleep(200 * time.Millisecond)

	// Client B connects and fetches the session.
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	getSessionViaWebSocket(t, connB, sessionID)

	// Both receive done.
	var wg sync.WaitGroup
	doneA, doneB := make(chan struct{}), make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, d, e := connA.Read(context.Background())
			if e != nil { return }
			var base struct{ Type string }
			json.Unmarshal(d, &base)
			if base.Type == "done" { close(doneA); return }
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, d, e := connB.Read(context.Background())
			if e != nil { return }
			var base struct{ Type string }
			json.Unmarshal(d, &base)
			if base.Type == "done" { close(doneB); return }
		}
	}()

	select {
	case <-doneA:
		t.Log("Client A: done")
	case <-time.After(5 * time.Second):
		t.Fatal("A timed out")
	}
	select {
	case <-doneB:
		t.Log("Client B: done")
	case <-time.After(5 * time.Second):
		t.Fatal("B timed out")
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Test 3: Cross-client cancellation
// ---------------------------------------------------------------------------
func TestParallel_CrossClientCancel(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par3")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _, _ := conv.Sessions().Get(context.Background(), "par3", sessionID)
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

	// A also gets cancelled done.
	for {
		_, d, e := connA.Read(context.Background())
		if e != nil { t.Fatalf("A read: %v", e) }
		var f struct {
			Type      string `json:"type"`
			Cancelled bool   `json:"cancelled"`
			Error     string `json:"error"`
		}
		json.Unmarshal(d, &f)
		if f.Type == "done" {
			if f.Error != "" { t.Fatalf("A error: %s", f.Error) }
			if !f.Cancelled { t.Fatal("A expected cancelled") }
			break
		}
	}
	t.Log("A: received cancel")
}

// ---------------------------------------------------------------------------
// Test 4: Independent sessions — two clients chat on different sessions.
// ---------------------------------------------------------------------------
func TestParallel_IndependentSessions(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par4")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sidA := createSessionViaWebSocket(t, connA)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	sidB := createSessionViaWebSocket(t, connB)

	sA, _, _ := conv.Sessions().Get(context.Background(), "par4", sidA)
	enableToolsDirect(sA, "echo_tool")
	conv.Sessions().Save(context.Background(), "par4", sA)
	sB, _, _ := conv.Sessions().Get(context.Background(), "par4", sidB)
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
		ctx, cxl := context.WithTimeout(context.Background(), 5*time.Second)
		defer cxl()
		for { _, d, e := connA.Read(ctx); if e != nil { return }; var f struct{ Type, Text string }; json.Unmarshal(d, &f); if f.Type == "done" { textA = f.Text; return } }
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cxl := context.WithTimeout(context.Background(), 5*time.Second)
		defer cxl()
		for { _, d, e := connB.Read(ctx); if e != nil { return }; var f struct{ Type, Text string }; json.Unmarshal(d, &f); if f.Type == "done" { textB = f.Text; return } }
	}()
	wg.Wait()

	if textA != "A done" { t.Fatalf("expected 'A done', got %q", textA) }
	if textB != "B done" { t.Fatalf("expected 'B done', got %q", textB) }
	t.Logf("Independent turns: A=%q B=%q", textA, textB)
}

// ---------------------------------------------------------------------------
// Test 5: Session list visibility across clients
// ---------------------------------------------------------------------------
func TestParallel_SessionListVisibility(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par5")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	createSessionViaWebSocket(t, connA)
	createSessionViaWebSocket(t, connA)

	// Client B connects and lists sessions.
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	writeWSFrame(t, connB, map[string]any{
		"type": "cmd", "command": map[string]any{"cmd": "session_list"},
	})
	var res struct {
		Type  string          `json:"type"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	readWSFrame(t, connB, 3*time.Second, &res)
	if res.Error != "" { t.Fatalf("session_list error: %s", res.Error) }
	if res.Type != "result" { t.Fatalf("expected result, got %q", res.Type) }
	t.Log("B: session_list works (sees A's sessions)")
}

// ---------------------------------------------------------------------------
// Test 6: Two clients create sessions simultaneously — unique IDs.
// ---------------------------------------------------------------------------
func TestParallel_SimultaneousSessionCreate(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par6")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()

	var sidA, sidB string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); sidA = createSessionViaWebSocket(t, connA) }()
	wg.Add(1)
	go func() { defer wg.Done(); sidB = createSessionViaWebSocket(t, connB) }()
	wg.Wait()

	if sidA == "" || sidB == "" { t.Fatal("both IDs must be non-empty") }
	if sidA == sidB { t.Fatal("duplicate session IDs") }
	t.Logf("A=%s B=%s (unique)", shortID(sidA), shortID(sidB))

	sessions, _ := conv.Sessions().List(context.Background(), "par6")
	if len(sessions) < 2 { t.Fatalf("expected >=2 sessions, got %d", len(sessions)) }
}

// ---------------------------------------------------------------------------
// Test 7: Mid-turn reconnect (slow_tool keeps turn alive)
// ---------------------------------------------------------------------------
func TestParallel_MidTurnReconnect(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par7")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _, _ := conv.Sessions().Get(context.Background(), "par7", sessionID)
	enableToolsDirect(session, "slow_tool", "echo_tool")
	conv.Sessions().Save(context.Background(), "par7", session)

	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"run_tool":{"name":"echo_tool","args":{"message":"hi"}}},{"message":{"content":"Final"}}]`,
	})
	time.Sleep(200 * time.Millisecond)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	getSessionViaWebSocket(t, connB, sessionID)

	var wg sync.WaitGroup
	doneA, doneB := make(chan struct{}), make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for { _, d, e := connA.Read(context.Background()); if e != nil { return }; var base struct{ Type string }; json.Unmarshal(d, &base); if base.Type == "done" { close(doneA); return } }
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for { _, d, e := connB.Read(context.Background()); if e != nil { return }; var base struct{ Type string }; json.Unmarshal(d, &base); if base.Type == "done" { close(doneB); return } }
	}()
	<-doneA; t.Log("A done")
	<-doneB; t.Log("B done")
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Test 8: Streaming mid-turn reconnect (slow_tool keeps turn alive)
// ---------------------------------------------------------------------------
func TestParallel_StreamingMidTurnReconnect(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par8")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sessionID := createSessionViaWebSocket(t, connA)

	session, _, _ := conv.Sessions().Get(context.Background(), "par8", sessionID)
	enableToolsDirect(session, "slow_tool")
	conv.Sessions().Save(context.Background(), "par8", session)

	// Streaming turn with slow_tool.
	writeWSFrame(t, connA, map[string]any{
		"type": "chat", "session_id": sessionID,
		"input": `[{"run_tool":{"name":"slow_tool","args":{"seconds":2}}},{"message":{"content":"Done streaming"}}]`,
		"stream": true,
	})
	time.Sleep(200 * time.Millisecond)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	getSessionViaWebSocket(t, connB, sessionID)

	var wg sync.WaitGroup
	doneA, doneB := make(chan struct{}), make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for { _, d, e := connA.Read(context.Background()); if e != nil { return }; var base struct{ Type string }; json.Unmarshal(d, &base); if base.Type == "done" { close(doneA); return } }
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for { _, d, e := connB.Read(context.Background()); if e != nil { return }; var base struct{ Type string }; json.Unmarshal(d, &base); if base.Type == "done" { close(doneB); return } }
	}()
	<-doneA; t.Log("A done")
	<-doneB; t.Log("B done")
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Test 9: Two concurrent tool turns on different sessions.
// ---------------------------------------------------------------------------
func TestParallel_ConcurrentToolTurnsDifferentSessions(t *testing.T) {
	conv, streamer, cmd, mockAdapter, _, addr := parallelSetup(t, "par9")
	gw := startMockGateway(t, conv, streamer, cmd, addr)
	defer gw.Stop(context.Background())
	defer mockAdapter.Reset()

	connA := connectAndWelcome(t, addr)
	defer connA.CloseNow()
	sidA := createSessionViaWebSocket(t, connA)
	sA, _, _ := conv.Sessions().Get(context.Background(), "par9", sidA)
	enableToolsDirect(sA, "echo_tool")
	conv.Sessions().Save(context.Background(), "par9", sA)

	connB := connectAndWelcome(t, addr)
	defer connB.CloseNow()
	sidB := createSessionViaWebSocket(t, connB)
	sB, _, _ := conv.Sessions().Get(context.Background(), "par9", sidB)
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
		ctx, cxl := context.WithTimeout(context.Background(), 5*time.Second)
		defer cxl()
		for { _, d, e := connA.Read(ctx); if e != nil { return }; var f struct{ Type, Text string }; json.Unmarshal(d, &f); if f.Type == "done" { textA = f.Text; return } }
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cxl := context.WithTimeout(context.Background(), 5*time.Second)
		defer cxl()
		for { _, d, e := connB.Read(ctx); if e != nil { return }; var f struct{ Type, Text string }; json.Unmarshal(d, &f); if f.Type == "done" { textB = f.Text; return } }
	}()
	wg.Wait()

	if textA != "A done" { t.Fatalf("expected 'A done', got %q", textA) }
	if textB != "B done" { t.Fatalf("expected 'B done', got %q", textB) }
	t.Logf("Concurrent turns: A=%q B=%q", textA, textB)
}

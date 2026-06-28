package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
)

// ---------------------------------------------------------------------------
// Reconnection via session_switch — this is the exact scenario described
// in the issue: a web UI reload causes the old WebSocket to close, then
// the new connection sends session_switch to restore the session. If the
// session has an active turn (from a previous streaming request), the new
// connection should be subscribed to the running turn and receive the
// remaining events.
// ---------------------------------------------------------------------------

// slowAdapter streams with delays so we can trigger the scenario.
type slowAdapter struct {
	mu          sync.Mutex
	streamCalls int
	chunks      []string
}

func newSlowAdapter(chunks []string) *slowAdapter {
	if len(chunks) == 0 {
		chunks = []string{"chunk1 ", "chunk2 ", "chunk3 ", "chunk4 ", "done!"}
	}
	return &slowAdapter{chunks: chunks}
}

func (a *slowAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *slowAdapter) Stream(ctx context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.mu.Lock()
	a.streamCalls++
	a.mu.Unlock()

	ch := make(chan agent.StreamResult, 10)
	go func() {
		defer close(ch)
		for _, chunk := range a.chunks {
			select {
			case <-ctx.Done():
				return
			case <-time.After(80 * time.Millisecond):
				ch <- agent.StreamResult{Delta: chunk}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Millisecond):
			ch <- agent.StreamResult{Done: true}
		}
	}()
	return ch, nil
}

// newSlowGatewayComponents creates components for the reconnect-via-switch tests.
func newSlowGatewayComponents(chunks []string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	adapter := newSlowAdapter(chunks)
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)
	return conv, streamer, cmd
}

// wsDialWithRetry dials a WebSocket URL with retries.
func wsDialWithRetry(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err == nil {
			return c
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ws dial %s: %v", url, lastErr)
	return nil
}

// wsReadFrame reads a single JSON frame from a WebSocket connection.
func wsReadFrame(t *testing.T, ctx context.Context, c *websocket.Conn) FrameResponse {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var frame FrameResponse
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("ws unmarshal %q: %v", string(data), err)
	}
	return frame
}

// wsWrite sends a JSON frame on a WebSocket connection.
func wsWrite(t *testing.T, ctx context.Context, c *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("ws marshal: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: TCP client disconnects mid-stream, new client reconnects via
// session_switch and receives remaining events.
// ---------------------------------------------------------------------------

func TestTCPGateway_ReconnectViaSessionSwitch(t *testing.T) {
	chunks := []string{"alpha ", "beta ", "gamma ", "delta ", "omega"}
	conv, streamer, cmd := newSlowGatewayComponents(chunks)
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Create a session.
	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.SessionID()

	// Client A: connect and send a streaming request.
	connA := dialRetry(t, addr)
	defer connA.Close()
	rA := bufio.NewReader(connA)

	_, _ = fmt.Fprintf(connA, `{"session_id":%q,"input":"hello","stream":true}`+"\n", sessionID)

	// Read one chunk to confirm streaming has started.
	connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame := readFrame(t, rA)
	if frame.Delta == "" {
		t.Fatalf("client A expected delta, got: %+v", frame)
	}
	// Check we didn't get done
	if frame.Done {
		t.Fatal("client A got done too early")
	}

	// Client A disconnects abruptly (simulating a web page reload).
	connA.Close()

	// Wait briefly — the turn continues in the background.
	time.Sleep(150 * time.Millisecond)

	// Client B: connect and send session_switch to the same session.
	// This simulates the web UI reconnecting and restoring the session.
	connB := dialRetry(t, addr)
	defer connB.Close()
	rB := bufio.NewReader(connB)

	writeToConn(t, connB, map[string]any{
		"session_id": sessionID,
		"command": map[string]any{
			"cmd":    "session_switch",
			"params": map[string]string{"id": sessionID},
		},
	})

	// Client B should first receive the session_switch response
	// (command_result event), then be subscribed to the active turn
	// and receive the remaining delta chunks + done frame.
	connB.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Collect all frames from client B.
	var delta strings.Builder
	var gotDone bool
	var sawCommandResult bool

	for !gotDone {
		line, err := rB.ReadBytes('\n')
		if err != nil {
			t.Fatalf("client B read: %v", err)
		}
		var f FrameResponse
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatalf("client B unmarshal: %v", err)
		}
		if f.Event == "command_result" {
			sawCommandResult = true
			continue
		}
		if f.Delta != "" {
			delta.WriteString(f.Delta)
		}
		if f.Error != "" {
			t.Fatalf("client B error: %s", f.Error)
		}
		gotDone = f.Done
	}

	if !sawCommandResult {
		t.Fatal("expected command_result event for session_switch")
	}
	if !strings.Contains(delta.String(), "gamma") {
		t.Fatalf("client B expected tail chunks including 'gamma', got: %q", delta.String())
	}
	if !strings.Contains(delta.String(), "omega") {
		t.Fatalf("client B expected final chunk 'omega', got: %q", delta.String())
	}
	t.Logf("Client B received: %q", delta.String())
}

// ---------------------------------------------------------------------------
// Test: WebSocket client disconnects mid-turn, new WS client reconnects
// via session_switch and receives remaining events.
// ---------------------------------------------------------------------------

func TestWebSocketGateway_ReconnectViaSessionSwitch(t *testing.T) {
	chunks := []string{"alpha ", "beta ", "gamma ", "delta ", "omega"}
	conv, streamer, cmd := newSlowGatewayComponents(chunks)
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Create a session.
	session, err := conv.Sessions.GetOrCreate(context.Background(), "ws", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.SessionID()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"

	// Client A: connect and send a streaming request.
	connA := wsDialWithRetry(t, ctx, url)
	wsWrite(t, ctx, connA, map[string]any{
		"session_id": sessionID,
		"input":      "hello",
		"stream":     true,
	})

	// Read one chunk to confirm streaming.
	frame := wsReadFrame(t, ctx, connA)
	if frame.Delta == "" {
		t.Fatalf("client A expected delta, got: %+v", frame)
	}
	if frame.Done {
		t.Fatal("client A got done too early")
	}

	// Client A disconnects (simulating page reload).
	connA.CloseNow()

	// Wait briefly — turn continues.
	time.Sleep(150 * time.Millisecond)

	// Client B: connect and send session_switch.
	connB := wsDialWithRetry(t, ctx, url)
	defer connB.CloseNow()

	wsWrite(t, ctx, connB, map[string]any{
		"session_id": sessionID,
		"command": map[string]any{
			"cmd":    "session_switch",
			"params": map[string]string{"id": sessionID},
		},
	})

	// Collect frames from client B.
	var delta strings.Builder
	var gotDone bool
	var sawCommandResult bool

	for !gotDone {
		frame := wsReadFrame(t, ctx, connB)
		if frame.Event == "command_result" {
			sawCommandResult = true
			continue
		}
		if frame.Delta != "" {
			delta.WriteString(frame.Delta)
		}
		if frame.Error != "" {
			t.Fatalf("client B error: %s", frame.Error)
		}
		gotDone = frame.Done
	}

	if !sawCommandResult {
		t.Fatal("expected command_result event for session_switch")
	}
	if !strings.Contains(delta.String(), "gamma") {
		t.Fatalf("client B expected tail chunks including 'gamma', got: %q", delta.String())
	}
	if !strings.Contains(delta.String(), "omega") {
		t.Fatalf("client B expected 'omega', got: %q", delta.String())
	}
	t.Logf("Client B received: %q", delta.String())
}

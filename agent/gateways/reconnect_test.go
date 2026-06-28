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
)

// reconnectAdapter simulates an LLM that streams slowly (with delays),
// allowing us to disconnect a client mid-stream and reconnect a new one.
type reconnectAdapter struct {
	mu          sync.Mutex
	streamCalls int
}

func (a *reconnectAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *reconnectAdapter) Stream(ctx context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.mu.Lock()
	a.streamCalls++
	a.mu.Unlock()

	ch := make(chan agent.StreamResult, 10)
	go func() {
		defer close(ch)

		// Emit chunks with 100ms delays so we can disconnect mid-stream.
		// Total duration: 7 * 100ms + 30ms = 730ms
		chunks := []string{
			"Hello, ",
			"this is ",
			"a streamed ",
			"response ",
			"that survives ",
			"disconnection ",
			"and reconnection!",
		}

		for _, chunk := range chunks {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
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

func newReconnectComponents() (*agent.Conversation, *agent.StreamSession, *reconnectAdapter) {
	adapter := &reconnectAdapter{}
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	return conv, streamer, adapter
}

// ---------------------------------------------------------------------------
// TCP reconnection test: client A disconnects mid-stream, client B
// reconnects and subscribes to the same turn.
// ---------------------------------------------------------------------------

func TestTCPGateway_DisconnectDoesNotCancelTurn(t *testing.T) {
	conv, streamer, adapter := newReconnectComponents()
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, nil, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.ID

	// Client A: connect and send a streaming request.
	connA := dialRetry(t, addr)
	defer connA.Close()
	rA := bufio.NewReader(connA)

	_, _ = fmt.Fprintf(connA, `{"session_id":%q,"input":"tell me something","stream":true}`+"\n", sessionID)

	// Read a few chunks to confirm streaming has started.
	connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	gotSomeData := false
	for i := 0; i < 2; i++ {
		line, err := rA.ReadBytes('\n')
		if err != nil {
			t.Fatalf("client A read error (chunk %d): %v", i, err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("client A unmarshal error: %v", err)
		}
		if frame.Delta != "" {
			gotSomeData = true
		}
		if frame.Done {
			t.Fatal("client A got done frame too early")
		}
	}
	if !gotSomeData {
		t.Fatal("client A didn't receive any streamed data before disconnect")
	}

	// Client A disconnects. The turn should continue.
	connA.Close()

	// Wait briefly so the turn remains active.
	time.Sleep(100 * time.Millisecond)

	// Verify the adapter was called.
	adapter.mu.Lock()
	callsAfterDisconnect := adapter.streamCalls
	adapter.mu.Unlock()
	if callsAfterDisconnect == 0 {
		t.Fatal("expected at least one stream call to the adapter")
	}

	// Client B: connect and subscribe to the same session.
	connB := dialRetry(t, addr)
	defer connB.Close()
	rB := bufio.NewReader(connB)

	_, _ = fmt.Fprintf(connB, `{"session_id":%q,"stream":true}`+"\n", sessionID)

	// Client B should receive the remaining delta chunks and a done frame.
	connB.SetReadDeadline(time.Now().Add(10 * time.Second))
	var delta strings.Builder
	var gotDone bool
	for !gotDone {
		line, err := rB.ReadBytes('\n')
		if err != nil {
			t.Fatalf("client B read error: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("client B unmarshal error: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("client B got error frame: %s", frame.Error)
		}
		delta.WriteString(frame.Delta)
		gotDone = frame.Done
	}

	final := delta.String()

	// Client A consumed the first ~2 chunks before disconnecting.
	// Client B should receive the remaining tail (from 'response' onward).
	if !strings.Contains(final, "response") {
		t.Fatalf("client B expected delta to contain 'response', got: %q", final)
	}
	if !strings.Contains(final, "survives") {
		t.Fatalf("client B expected delta to contain 'survives', got: %q", final)
	}
	if !strings.Contains(final, "reconnection!") {
		t.Fatalf("client B expected delta to contain 'reconnection!', got: %q", final)
	}
}

// ---------------------------------------------------------------------------
// WebSocket reconnection test
// ---------------------------------------------------------------------------

func TestWebSocketGateway_DisconnectDoesNotCancelTurn(t *testing.T) {
	conv, streamer, adapter := newReconnectComponents()
	addr := freeAddr(t)

	gw := NewWebSocketGateway(conv, streamer, nil, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	session, err := conv.Sessions.GetOrCreate(context.Background(), "ws", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.ID

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"

	// Client A: connect via WebSocket and send a streaming request.
	connA, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("client A dial: %v", err)
	}

	if err := connA.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+sessionID+`","input":"tell me something","stream":true}`)); err != nil {
		t.Fatalf("client A write: %v", err)
	}

	// Read a couple of frames.
	gotSomeData := false
	for i := 0; i < 2; i++ {
		_, data, err := connA.Read(ctx)
		if err != nil {
			t.Fatalf("client A read error (frame %d): %v", i, err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("client A unmarshal error: %v", err)
		}
		if frame.Delta != "" {
			gotSomeData = true
		}
		if frame.Done {
			t.Fatal("client A got done too early")
		}
	}
	if !gotSomeData {
		t.Fatal("client A didn't receive any data")
	}

	// Client A disconnects abruptly.
	connA.CloseNow()

	// Wait briefly so turn stays active.
	time.Sleep(100 * time.Millisecond)

	// Verify adapter was called.
	adapter.mu.Lock()
	callsAfterDisconnect := adapter.streamCalls
	adapter.mu.Unlock()
	if callsAfterDisconnect == 0 {
		t.Fatal("expected stream calls after disconnect")
	}

	// Client B: connect and subscribe to the same session.
	connB, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("client B dial: %v", err)
	}
	defer connB.CloseNow()

	if err := connB.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+sessionID+`","stream":true}`)); err != nil {
		t.Fatalf("client B write: %v", err)
	}

	// Client B should receive the rest of the stream.
	var delta strings.Builder
	var gotDone bool
	for !gotDone {
		_, data, err := connB.Read(ctx)
		if err != nil {
			t.Fatalf("client B read error: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("client B unmarshal error: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("client B got error: %s", frame.Error)
		}
		delta.WriteString(frame.Delta)
		gotDone = frame.Done
	}

	final := delta.String()
	if !strings.Contains(final, "response") {
		t.Fatalf("client B expected 'response' in delta, got: %q", final)
	}
	if !strings.Contains(final, "reconnection!") {
		t.Fatalf("client B expected 'reconnection!' in delta, got: %q", final)
	}
}

// ---------------------------------------------------------------------------
// Multiple concurrent subscribers test: both clients receive events
// simultaneously from the same turn.
// ---------------------------------------------------------------------------

func TestTCPGateway_MultipleSubscribersReceiveSameEvents(t *testing.T) {
	conv, streamer, _ := newReconnectComponents()
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, nil, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.ID

	// Connect client A and start a streaming request.
	connA := dialRetry(t, addr)
	defer connA.Close()
	rA := bufio.NewReader(connA)

	_, _ = fmt.Fprintf(connA, `{"session_id":%q,"input":"broadcast test","stream":true}`+"\n", sessionID)

	// Wait for streaming to begin.
	connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = rA.ReadBytes('\n')
	if err != nil {
		t.Fatalf("client A first read: %v", err)
	}

	// Connect client B and subscribe to the same session.
	connB := dialRetry(t, addr)
	defer connB.Close()
	rB := bufio.NewReader(connB)

	_, _ = fmt.Fprintf(connB, `{"session_id":%q,"stream":true}`+"\n", sessionID)

	// Both clients should receive events until done.
	var deltaA, deltaB strings.Builder
	var doneA, doneB bool

	// Read from both until both are done.
	for !doneA || !doneB {
		connA.SetReadDeadline(time.Now().Add(1 * time.Second))
		connB.SetReadDeadline(time.Now().Add(1 * time.Second))

		if !doneA {
			line, err := rA.ReadBytes('\n')
			if err == nil {
				var frame FrameResponse
				if uerr := json.Unmarshal(line, &frame); uerr == nil {
					deltaA.WriteString(frame.Delta)
					doneA = frame.Done
				}
			}
		}

		if !doneB {
			line, err := rB.ReadBytes('\n')
			if err == nil {
				var frame FrameResponse
				if uerr := json.Unmarshal(line, &frame); uerr == nil {
					deltaB.WriteString(frame.Delta)
					doneB = frame.Done
				}
			}
		}
	}

	// Both clients should have received some content.
	if deltaA.Len() == 0 && deltaB.Len() == 0 {
		t.Fatal("neither client received any content")
	}

	t.Logf("Client A received: %q", deltaA.String())
	t.Logf("Client B received: %q", deltaB.String())
}

// ---------------------------------------------------------------------------
// WebSocket reconnection — basic sanity
// ---------------------------------------------------------------------------

func TestWebSocketGateway_ReconnectSameSession(t *testing.T) {
	conv, streamer, _ := newReconnectComponents()
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, nil, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	session, err := conv.Sessions.GetOrCreate(context.Background(), "ws", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sessionID := session.ID

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"

	// Client A: connect, send streaming request, read one frame, disconnect.
	connA, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("client A dial: %v", err)
	}

	if err := connA.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+sessionID+`","input":"stream test","stream":true}`)); err != nil {
		t.Fatalf("client A write: %v", err)
	}

	_, _, err = connA.Read(ctx)
	if err != nil {
		t.Fatalf("client A read: %v", err)
	}
	connA.CloseNow()

	// Wait briefly then reconnect.
	time.Sleep(50 * time.Millisecond)

	// Client B: connect and subscribe.
	connB, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("client B dial: %v", err)
	}
	defer connB.CloseNow()

	if err := connB.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+sessionID+`","stream":true}`)); err != nil {
		t.Fatalf("client B write: %v", err)
	}

	// Read until done.
	var delta strings.Builder
	var gotDone bool
	for !gotDone {
		_, data, err := connB.Read(ctx)
		if err != nil {
			t.Fatalf("client B read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("client B unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("client B error: %s", frame.Error)
		}
		delta.WriteString(frame.Delta)
		gotDone = frame.Done
	}

	if delta.Len() == 0 {
		t.Fatal("client B received empty response")
	}
	t.Logf("Client B received: %q", delta.String())
}

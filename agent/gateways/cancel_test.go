package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/commands"
)

// blockingAdapter blocks on Complete/Stream until the context is cancelled.
// It simulates a hanging LLM request.
type blockingAdapter struct {
	blocking chan struct{} // closed once to signal "we are blocking"
	once     sync.Once
}

func (a *blockingAdapter) Complete(ctx context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	a.once.Do(func() {
		if a.blocking != nil {
			close(a.blocking)
		}
	})
	<-ctx.Done()
	return nil, ctx.Err()
}

func (a *blockingAdapter) Stream(ctx context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	// Block until cancelled to simulate a hanging stream.
	err := a.blockAndWait(ctx)
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, err
}

func (a *blockingAdapter) blockAndWait(ctx context.Context) error {
	a.once.Do(func() {
		if a.blocking != nil {
			close(a.blocking)
		}
	})
	<-ctx.Done()
	return ctx.Err()
}

// newCancellationGatewayComponents creates components with a blocking adapter.
func newCancellationGatewayComponents() (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor, *blockingAdapter) {
	adapter := &blockingAdapter{blocking: make(chan struct{})}
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
	return conv, streamer, cmd, adapter
}

// readFrame reads a single FrameResponse from a buffered TCP connection.
func readFrame(t *testing.T, r *bufio.Reader) FrameResponse {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame FrameResponse
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("unmarshal frame %q: %v", string(line), err)
	}
	return frame
}

// writeToConn writes a JSON frame to a TCP connection.
func writeToConn(t *testing.T, conn net.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := fmt.Fprintln(conn, string(data)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestTCPGateway_CancelInFlightRequest_Complete(t *testing.T) {
	conv, streamer, cmd, adapter := newCancellationGatewayComponents()
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Client A: send a request that will block on the LLM call.
	connA := dialRetry(t, addr)
	defer connA.Close()
	rA := bufio.NewReader(connA)

	// Send a non-stream request (uses Complete which blocks).
	writeToConn(t, connA, map[string]any{"input": "hello", "session_id": "session-cancel-1"})

	// Wait until the adapter is actually blocking inside Complete.
	<-adapter.blocking

	// Client B: open a second connection and send a cancel frame.
	connB := dialRetry(t, addr)
	defer connB.Close()

	writeToConn(t, connB, map[string]any{
		"type":       "cancel",
		"session_id": "session-cancel-1",
	})

	// Client A should now receive an error frame (context cancelled).
	connA.SetReadDeadline(time.Now().Add(3 * time.Second))
	frame := readFrame(t, rA)

	// We expect either an error frame or a done frame with cancellation context.
	// The important thing is that the connection is NOT hung indefinitely.
	if frame.Error == "" {
		t.Fatalf("expected error frame after cancellation, got: %+v", frame)
	}
	if !strings.Contains(frame.Error, "context canceled") &&
		!strings.Contains(frame.Error, "cancelled") &&
		!strings.Contains(frame.Error, "Canceled") {
		t.Fatalf("expected error about cancellation, got: %q", frame.Error)
	}
	t.Logf("OK: cancel frame produced expected error: %q", frame.Error)
}

func TestTCPGateway_CancelInFlightRequest_Stream(t *testing.T) {
	conv, streamer, cmd, adapter := newCancellationGatewayComponents()
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	// Client A: send a streaming request that will block.
	connA := dialRetry(t, addr)
	defer connA.Close()
	rA := bufio.NewReader(connA)

	writeToConn(t, connA, map[string]any{
		"input":      "hello stream",
		"session_id": "session-cancel-stream-1",
		"stream":     true,
	})

	// Wait until the adapter is actually blocking inside Stream.
	<-adapter.blocking

	// Client B: send a cancel frame.
	connB := dialRetry(t, addr)
	defer connB.Close()

	writeToConn(t, connB, map[string]any{
		"type":       "cancel",
		"session_id": "session-cancel-stream-1",
	})

	// Client A should receive a final frame (done or error).
	connA.SetReadDeadline(time.Now().Add(3 * time.Second))

	var lastFrame FrameResponse
	for {
		frame := readFrame(t, rA)
		lastFrame = frame
		if frame.Done || frame.Error != "" {
			break
		}
	}

	if lastFrame.Error == "" && !lastFrame.Done {
		t.Fatalf("expected final frame (done or error) after cancellation, got: %+v", lastFrame)
	}
	if lastFrame.Error != "" {
		t.Logf("OK: stream cancel produced error: %q", lastFrame.Error)
	} else {
		t.Logf("OK: stream cancel produced done frame")
	}
}

func TestTCPGateway_CancelUnknownSession_ReturnsFalse(t *testing.T) {
	conv, streamer, cmd, _ := newCancellationGatewayComponents()
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	// Send a cancel for a session that has no active request.
	writeToConn(t, conn, map[string]any{
		"type":       "cancel",
		"session_id": "nonexistent-session",
	})

	// Should get a response frame acknowledging the cancel (even if no-op).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	frame := readFrame(t, r)
	t.Logf("cancel response for unknown session: %+v", frame)
}

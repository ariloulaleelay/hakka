package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/commands"
)

type fakeAdapter struct {
	reply string
}

func (f *fakeAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: f.reply},
		FinishReason: "stop",
	}, nil
}

func (f *fakeAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult, 2)
	ch <- agent.StreamResult{Delta: f.reply}
	ch <- agent.StreamResult{Done: true}
	close(ch)
	return ch, nil
}

func newGatewayComponents(reply string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	return newNamedGatewayComponents(reply, "tcp")
}

// newNamedGatewayComponents creates components with a specific namespace.
// Used by tests that need to match gateway namespaces (e.g. "tcp" vs "ws").
func newNamedGatewayComponents(reply, ns string) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", &fakeAdapter{reply: reply})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)
	return conv, streamer, cmd
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func dialRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s: %v", addr, lastErr)
	return nil
}

// --- TCP test cases ---------------------------------------------------------

func TestTCPGatewayRoundTrip(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()

	if _, err := fmt.Fprintln(conn, `{"input":"ping"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp FrameResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Output != "pong" {
		t.Fatalf("expected output %q, got: %q", "pong", resp.Output)
	}
	if !resp.Done {
		t.Fatal("expected done=true, got false")
	}
	if resp.SessionID == "" {
		t.Fatal("expected non-empty session_id in response, got empty")
	}
}

func TestTCPGateway_ReturnsSessionIDOnFirstRequest(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ack")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()

	if _, err := fmt.Fprintln(conn, `{"input":"one"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp FrameResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatal("expected non-empty session_id in first response")
	}
}

func TestTCPGateway_ReusesSessionIDAcrossRequests(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ack")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	send := func(body string) FrameResponse {
		if _, err := fmt.Fprintln(conn, body); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp FrameResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	first := send(`{"input":"one"}`)
	sid := first.SessionID

	second := send(fmt.Sprintf(`{"session_id":%q,"input":"two"}`, sid))
	if second.SessionID != sid {
		t.Fatalf("expected session_id %q to persist across requests, got: %q", sid, second.SessionID)
	}
}

// twoStageAdapter returns tool calls on the first Stream call and text on
// the second, simulating a realistic streaming tool round-trip.
type twoStageAdapter struct {
	callCount int
}

func (a *twoStageAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "done"},
		FinishReason: "stop",
	}, nil
}

func (a *twoStageAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.callCount++
	ch := make(chan agent.StreamResult, 4)
	if a.callCount == 1 {
		ch <- agent.StreamResult{ToolCalls: []agent.ToolCall{
			{ID: "tc1", Name: "example_tool", Arguments: `{}`},
		}}
	} else {
		ch <- agent.StreamResult{Delta: "final answer"}
		ch <- agent.StreamResult{Done: true}
	}
	close(ch)
	return ch, nil
}

func TestTCPGatewayStreamToolFallbackUsesStreamNotComplete(t *testing.T) {
	adapter := &twoStageAdapter{}
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "example_tool", Description: "test"},
		Tags:   []string{"all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok":true}`, nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 3}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)

	session, _ := sm.GetOrCreate(context.Background(), ns, "")
	session.EnableTool("example_tool")
	sm.Save(context.Background(), ns, session)

	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintf(conn, `{"session_id":%q,"input":"hello","stream":true}`+"\n", session.ID); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var (
		toolSeen bool
		delta    strings.Builder
		done     bool
	)
	for !done {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Event == "tool" {
			toolSeen = true
		}
		delta.WriteString(frame.Delta)
		done = frame.Done
	}

	if !toolSeen {
		t.Fatal("expected tool event during streaming")
	}
	if !strings.Contains(delta.String(), "final answer") {
		t.Fatalf("expected delta to contain 'final answer', got: %q", delta.String())
	}
}

func TestTCPGatewayStreamingCommandSendsDone(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintln(conn, `{"input":"/model","stream":true}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read command reply: %v", err)
	}
	var reply FrameResponse
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatalf("unmarshal reply %q: %v", line, err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error frame: %s", reply.Error)
	}
	if !strings.Contains(reply.Delta, "current model:") {
		t.Fatalf("expected delta to mention 'current model:', got: %+v", reply)
	}
	if reply.Done {
		t.Fatalf("expected first frame to carry delta only, got done frame: %+v", reply)
	}

	line, err = r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read command done frame: %v", err)
	}
	var done FrameResponse
	if err := json.Unmarshal(line, &done); err != nil {
		t.Fatalf("unmarshal done %q: %v", line, err)
	}
	if done.Error != "" {
		t.Fatalf("unexpected error frame: %s", done.Error)
	}
	if !done.Done {
		t.Fatalf("expected final done frame after streaming command reply, got: %+v", done)
	}
	if done.SessionID == "" {
		t.Fatalf("expected done frame to include session id, got: %+v", done)
	}
}

func TestTCPGatewayStreamingLLMErrorSendsErrorFrame(t *testing.T) {
	adapter := &streamErrorAdapter{err: context.DeadlineExceeded}
	conv, streamer, cmd := newGatewayComponentsWithAdapter(adapter)
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintln(conn, `{"input":"trigger timeout","stream":true}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read error frame: %v", err)
	}
	var frame FrameResponse
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if frame.Error == "" {
		t.Fatalf("expected error frame for streaming LLM error, got: %+v", frame)
	}
	if !strings.Contains(frame.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected deadline error in frame, got: %+v", frame)
	}
	if frame.SessionID == "" {
		t.Fatalf("expected error frame to include session id, got: %+v", frame)
	}
}

// --- WebSocket test cases ---------------------------------------------------

func TestWebSocketGatewayRoundTrip(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ws-pong")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"hello"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp FrameResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Output != "ws-pong" {
		t.Fatalf("expected output %q, got: %q", "ws-pong", resp.Output)
	}
	if !resp.Done || resp.SessionID == "" {
		t.Fatalf("expected done=true and non-empty session_id, got: %+v", resp)
	}
}

func TestWebSocketGatewayStream(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("streamed-payload")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"hi","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var (
		acc  strings.Builder
		done bool
	)
	for !done {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		acc.WriteString(frame.Delta)
		done = frame.Done
	}
	if acc.String() != "streamed-payload" {
		t.Fatalf("expected concatenated payload %q, got: %q", "streamed-payload", acc.String())
	}
}

func TestWebSocketGatewayStreamToolFallbackUsesStreamNotComplete(t *testing.T) {
	adapter := &twoStageAdapter{}
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "example_tool", Description: "test"},
		Tags:   []string{"all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok":true}`, nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 3}
	ns := "tcp"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	streamer := agent.NewStreamSession(conv, ns)
	cmd := commands.New(sm, conv, "", ns)

	session, _ := sm.GetOrCreate(context.Background(), ns, "")
	session.EnableTool("example_tool")
	sm.Save(context.Background(), ns, session)

	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"session_id":"`+session.ID+`","input":"hello","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var (
		toolSeen bool
		delta    strings.Builder
		done     bool
	)
	for !done {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame FrameResponse
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Event == "tool" {
			toolSeen = true
		}
		delta.WriteString(frame.Delta)
		done = frame.Done
	}

	if !toolSeen {
		t.Fatal("expected tool event during streaming")
	}
	if !strings.Contains(delta.String(), "final answer") {
		t.Fatalf("expected delta to contain 'final answer', got: %q", delta.String())
	}
}

func TestWebSocketGatewayStreamingCommandSendsDone(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"/model","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read command reply: %v", err)
	}
	var reply FrameResponse
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("unmarshal reply %q: %v", data, err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error frame: %s", reply.Error)
	}
	if !strings.Contains(reply.Delta, "current model:") {
		t.Fatalf("expected delta to mention 'current model:', got: %+v", reply)
	}
	if reply.Done {
		t.Fatalf("expected first frame to carry delta only, got done frame: %+v", reply)
	}

	_, data, err = c.Read(ctx)
	if err != nil {
		t.Fatalf("read command done frame: %v", err)
	}
	var done FrameResponse
	if err := json.Unmarshal(data, &done); err != nil {
		t.Fatalf("unmarshal done %q: %v", data, err)
	}
	if done.Error != "" {
		t.Fatalf("unexpected error frame: %s", done.Error)
	}
	if !done.Done {
		t.Fatalf("expected final done frame after streaming command reply, got: %+v", done)
	}
	if done.SessionID == "" {
		t.Fatalf("expected done frame to include session id, got: %+v", done)
	}
}

func TestWebSocketGatewayStreamingLLMErrorSendsErrorFrame(t *testing.T) {
	adapter := &streamErrorAdapter{err: context.DeadlineExceeded}
	conv, streamer, cmd := newGatewayComponentsWithAdapter(adapter)
	addr := freeAddr(t)
	gw := NewWebSocketGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := "ws://" + addr + "/ws"
	var (
		c   *websocket.Conn
		err error
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err = websocket.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"trigger timeout","stream":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read error frame: %v", err)
	}
	var frame FrameResponse
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	if frame.Error == "" {
		t.Fatalf("expected error frame for streaming LLM error, got: %+v", frame)
	}
	if !strings.Contains(frame.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("expected deadline error in frame, got: %+v", frame)
	}
	if frame.SessionID == "" {
		t.Fatalf("expected error frame to include session id, got: %+v", frame)
	}
}

// --- Init frame test --------------------------------------------------------

func TestTCPGateway_InitFrameSetsCWDAndReturnsModel(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintln(conn, `{"type":"init","cwd":"/home/user/project","start":true}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp FrameResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Event != "init" {
		t.Fatalf("expected event=init, got: %q", resp.Event)
	}
	if resp.SessionID == "" {
		t.Fatal("expected non-empty session_id in init response")
	}
	if !resp.Done {
		t.Fatal("expected done=true on init response")
	}

	model, _ := resp.Data["model"].(string)
	cwd, _ := resp.Data["cwd"].(string)
	if model == "" {
		t.Fatal("expected non-empty model in init response")
	}
	if cwd != "/home/user/project" {
		t.Fatalf("expected cwd %q, got: %q", "/home/user/project", cwd)
	}

	session, lookupErr := conv.Sessions.GetOrCreate(context.Background(), "tcp", resp.SessionID)
	if lookupErr != nil {
		t.Fatalf("GetOrCreate: %v", lookupErr)
	}
	if session.ClientCWD != "/home/user/project" {
		t.Fatalf("expected session CWD %q, got: %q", "/home/user/project", session.ClientCWD)
	}
}

// --- Non-streaming tool execution test --------------------------------------

func TestTCPGatewayNonStreamToolExecutionRoundTrip(t *testing.T) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", &fakeAdapterWithToolCalls{
		responses: []agent.LLMResponse{
			{
				Message: agent.Message{
					Role: agent.RoleAssistant,
					ToolCalls: []agent.ToolCall{{
						ID: "tc1", Name: "reverse", Arguments: `{"input":"hello"}`,
					}},
				},
			},
			{
				Message:      agent.Message{Role: agent.RoleAssistant, Content: "result is olleh"},
				FinishReason: "stop",
			},
		},
	})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "reverse", Description: "reverse a string"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Input string `json:"input"` }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			runes := []rune(args.Input)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return fmt.Sprintf(`{"reversed":%q}`, string(runes)), nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 5}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)
	streamer := agent.NewStreamSession(conv, "tcp")
	cmd := commands.New(sm, conv, "", "tcp")

	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintln(conn, `{"input":"reverse hello"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var resp FrameResponse
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if resp.Error != "" {
			t.Fatalf("unexpected error: %s", resp.Error)
		}
		if resp.Output != "" {
			break
		}
	}
	if resp.Output != "result is olleh" {
		t.Fatalf("expected output %q, got: %q", "result is olleh", resp.Output)
	}
	if !resp.Done {
		t.Fatal("expected done=true")
	}
	if resp.SessionID == "" {
		t.Fatal("expected non-empty session_id")
	}
}

// --- Session switch with messages test --------------------------------------

func TestTCPGateway_SessionSwitchIncludesMessages(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("unused")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	write := func(body string) {
		if _, err := fmt.Fprintln(conn, body); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	read := func() FrameResponse {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp FrameResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	// Create session A and send a message so it has history.
	write(`{"input":"first message"}`)
	first := read() // output + done
	sidA := first.SessionID

	// Create session B by sending /session create.
	write(`{"input":"/session create"}`)
	createEvent := read() // session_create event
	_ = read()            // reply (output + done)
	_ = createEvent

	// Switch back to session A.
	write(fmt.Sprintf(`{"input":"/session switch %s"}`, sidA))
	switchEvent := read() // session_switch event
	_ = read()            // reply (output + done)

	if switchEvent.Event != "session_switch" {
		t.Fatalf("expected event=session_switch, got: %q", switchEvent.Event)
	}
	if switchEvent.Data == nil {
		t.Fatal("expected data in session_switch event, got nil")
	}
	msgs, ok := switchEvent.Data["messages"].([]any)
	if !ok {
		t.Fatal("expected messages field in session_switch data")
	}
	if len(msgs) == 0 {
		t.Fatal("expected non-empty messages in session_switch data")
	}
}

// --- Helper types -----------------------------------------------------------

type fakeAdapterWithToolCalls struct {
	responses []agent.LLMResponse
	callCount int
}

func (a *fakeAdapterWithToolCalls) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	if a.callCount >= len(a.responses) {
		return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "fallback"}}, nil
	}
	r := a.responses[a.callCount]
	a.callCount++
	return &r, nil
}

func (a *fakeAdapterWithToolCalls) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
}

type streamErrorAdapter struct {
	err error
}

func (a *streamErrorAdapter) Complete(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return nil, a.err
}

func (a *streamErrorAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	return nil, a.err
}

func newGatewayComponentsWithAdapter(adapter agent.LLMAdapter) (*agent.Conversation, *agent.StreamSession, *commands.CommandProcessor) {
	sm := agent.NewSessionManager(nil, "")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)
	streamer := agent.NewStreamSession(conv, "tcp")
	cmd := commands.New(sm, conv, "", "tcp")
	return conv, streamer, cmd
}

func historyHasUserMessage(history []agent.Message, content string) bool {
	for _, msg := range history {
		if msg.Role == agent.RoleUser && msg.Content == content {
			return true
		}
	}
	return false
}

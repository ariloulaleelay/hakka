package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ariloulaleelay/hakka/agent"
)

// TestTCPGatewayInitHandshake verifies that sending a type:"init" frame
// creates/resolves a session, stores the client CWD, and returns metadata
// including the session ID, model name, and confirmed CWD. The response
// must include Done:true so the client's connect-and-read loop terminates.
func TestTCPGatewayInitHandshake(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ok")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	// Send init frame with CWD
	_, err := fmt.Fprintln(conn, `{"type":"init","cwd":"/home/user"}`)
	if err != nil {
		t.Fatalf("write init: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read init response: %v", err)
	}

	var resp struct {
		Event     string `json:"event"`
		SessionID string `json:"session_id"`
		Done      bool   `json:"done"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal init response: %v\nraw: %s", err, string(line))
	}

	if resp.Event != "init" {
		t.Fatalf("expected event=init, got %q", resp.Event)
	}
	if resp.SessionID == "" {
		t.Fatal("expected non-empty session_id in init response")
	}
	if !resp.Done {
		t.Fatal("BUG: init response must have Done=true so client.send() terminates")
	}
	if resp.Data == nil {
		t.Fatal("expected data in init response")
	}
	model, _ := resp.Data["model"].(string)
	if model == "" {
		t.Fatal("expected model name in init response data")
	}
	cwd, _ := resp.Data["cwd"].(string)
	if cwd != "/home/user" {
		t.Fatalf("expected cwd=/home/user, got %q", cwd)
	}

	// Now verify that a subsequent request with this session_id uses the CWD
	sid := resp.SessionID
	_, err = fmt.Fprintf(conn, `{"session_id":%q,"input":"hello"}`+"\n", sid)
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	// Verify the session has the correct CWD
	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Read().ClientCWD != "/home/user" {
		t.Fatalf("expected session.Read().ClientCWD=/home/user, got %q", session.Read().ClientCWD)
	}
}

// TestTCPGatewaySetsCWDOnSession verifies that when a client sends a
// request with a "cwd" field, the session stores it and it appears in
// the conversation history as a system message with the CWD info.
func TestTCPGatewaySetsCWDOnSession(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("ok")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()

	// Send first request WITH cwd
	_, err := fmt.Fprintln(conn, `{"input":"hello","cwd":"/client/project"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp struct {
		SessionID string `json:"session_id"`
		Output    string `json:"output"`
		Done      bool   `json:"done"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	sid := resp.SessionID
	if sid == "" {
		t.Fatal("missing session id")
	}

	// Now fetch the session and check that ClientCWD was stored
	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Read().ClientCWD != "/client/project" {
		t.Fatalf("expected ClientCWD=/client/project, got %q", session.Read().ClientCWD)
	}

	// Check that BuildContext includes the CWD system message with the right phrasing
	ctx := agent.BuildContext(session)
	foundCWD := false
	for _, msg := range ctx {
		if msg.Role == agent.RoleSystem && strings.Contains(msg.Content, "/client/project") {
			foundCWD = true
			if !strings.Contains(msg.Content, "operate relative") {
				t.Fatal("expected CWD message to mention relative paths")
			}
			break
		}
	}
	if !foundCWD {
		t.Fatalf("expected CWD info in BuildContext, got: %+v", ctx)
	}
}

// TestTCPGatewayCWDSurvivesAcrossRequests verifies that once the CWD is
// set, it persists for subsequent requests (the client doesn't need to
// send it every time).
func TestTCPGatewayCWDSurvivesAcrossRequests(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	send := func(body string) map[string]any {
		_, err := fmt.Fprintln(conn, body)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	// First request with CWD
	first := send(`{"input":"first","cwd":"/workspace"}`)
	sid, _ := first["session_id"].(string)
	if sid == "" {
		t.Fatal("missing session id")
	}

	// Second request WITHOUT cwd — CWD should still be set
	second := send(fmt.Sprintf(`{"session_id":%q,"input":"second"}`, sid))
	if second["session_id"] != sid {
		t.Fatalf("session id changed")
	}

	// Verify CWD is still on the session
	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Read().ClientCWD != "/workspace" {
		t.Fatalf("expected ClientCWD=/workspace, got %q", session.Read().ClientCWD)
	}
}

// TestTCPGatewayCWDCannotBeOverriddenByEmptyString verifies that sending
// cwd="" does not erase an already-set CWD.
func TestTCPGatewayCWDCannotBeOverriddenByEmpty(t *testing.T) {
	conv, streamer, cmd := newGatewayComponents("pong")
	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, cmd, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	send := func(body string) map[string]any {
		_, err := fmt.Fprintln(conn, body)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	first := send(`{"input":"first","cwd":"/project"}`)
	sid, _ := first["session_id"].(string)

	// Second request with empty cwd
	send(fmt.Sprintf(`{"session_id":%q,"input":"second","cwd":""}`, sid))

	session, err := conv.Sessions.GetOrCreate(context.Background(), "tcp", sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Read().ClientCWD != "/project" {
		t.Fatalf("expected CWD to remain /project, got %q", session.Read().ClientCWD)
	}
}

// TestWebSocketGatewaySetsCWDOnSession verifies CWD propagation over WebSocket.
func TestWebSocketGatewaySetsCWDOnSession(t *testing.T) {
	conv, streamer, cmd := newNamedGatewayComponents("ws-ok", "ws")
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

	// Send request with cwd
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"input":"hello","cwd":"/ws/project"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp struct {
		SessionID string `json:"session_id"`
		Output    string `json:"output"`
		Done      bool   `json:"done"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	sid := resp.SessionID
	if sid == "" {
		t.Fatal("missing session id")
	}

	session, err := conv.Sessions.GetOrCreate(ctx, "ws", sid)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Read().ClientCWD != "/ws/project" {
		t.Fatalf("expected ClientCWD=/ws/project, got %q", session.Read().ClientCWD)
	}
}
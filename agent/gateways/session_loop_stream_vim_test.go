package gateways

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
)

// streamVimAdapter simulates an LLM that streams partial text, requests a
// vim tool, then on the second stream returns a final answer.
type streamVimAdapter struct {
	callCount int
}

func (a *streamVimAdapter) Complete(_ context.Context, msgs []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: "I looked at the file."},
		FinishReason: "stop",
		Usage:        &agent.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (a *streamVimAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	a.callCount++
	ch := make(chan agent.StreamResult, 4)
	if a.callCount == 1 {
		ch <- agent.StreamResult{Delta: "Let me check the "}
		ch <- agent.StreamResult{Delta: "file..."}
		ch <- agent.StreamResult{
			ToolCalls: []agent.ToolCall{{
				ID: "tc-vim", Name: "vim_run_command", Arguments: `{"command": "echo 'hello'"}`,
			}},
		}
	} else {
		ch <- agent.StreamResult{Delta: "I looked at the file."}
		ch <- agent.StreamResult{Done: true}
	}
	close(ch)
	return ch, nil
}

func makeRouter(adapter agent.LLMAdapter) *agent.Router {
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	return agent.NewRouter(reg)
}

func readLine(t *testing.T, r *bufio.Reader) FrameResponse {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f FrameResponse
	if err := json.Unmarshal(line, &f); err != nil {
		t.Fatalf("unmarshal %q: %v", string(line), err)
	}
	return f
}

func TestStreamVimToolFlow(t *testing.T) {
	adapter := &streamVimAdapter{}
	sm := agent.NewSessionManager(nil, "")
	router := makeRouter(adapter)
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "vim_run_command", Description: "run vim command"},
		Tags:   []string{"vim", "all"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			return `{"ok": true}`, nil
		},
	})
	cfg := agent.EngineConfig{MaxToolIterations: 5}
	conv := agent.NewConversation(sm, router, tools, "tcp", cfg)
	streamer := agent.NewStreamSession(conv, "tcp")

	addr := freeAddr(t)
	gw := NewTCPGateway(conv, streamer, nil, addr)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop(context.Background())

	session, err := sm.GetOrCreate(context.Background(), "tcp", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	session.EnableTool("vim_run_command")
	if err := sm.Save(context.Background(), "tcp", session); err != nil {
		t.Fatalf("save: %v", err)
	}

	conn := dialRetry(t, addr)
	defer conn.Close()
	r := bufio.NewReader(conn)

	if _, err := fmt.Fprintf(conn, `{"session_id":%q,"input":"check file","stream":true}`+"\n", session.SessionID()); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var (
		gotToolStart bool
		gotToolOk    bool
		gotDone      bool
		deltaText    strings.Builder
	)
	for !gotDone {
		frame := readLine(t, r)
		if frame.Error != "" {
			t.Fatalf("frame error: %s", frame.Error)
		}
		if frame.Event == "tool" && frame.Status == "start" {
			gotToolStart = true
		}
		if frame.Event == "tool" && frame.Status == "ok" {
			gotToolOk = true
		}
		if frame.Delta != "" {
			deltaText.WriteString(frame.Delta)
		}
		gotDone = frame.Done
	}

	if !gotToolStart {
		t.Fatal("expected tool start event")
	}
	if !gotToolOk {
		t.Fatal("expected tool ok event")
	}
	final := deltaText.String()
	if !strings.Contains(final, "Let me check the file...") {
		t.Fatalf("expected partial stream text 'Let me check the file...', got: %q", final)
	}
	if !strings.Contains(final, "I looked at the file.") {
		t.Fatalf("expected final response 'I looked at the file.', got: %q", final)
	}
	if !gotDone {
		t.Fatal("expected done frame")
	}
}

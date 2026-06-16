package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/you/hakka/agent/event"
)

// fakeAdapter returns scripted responses, one per call. If toolCalls are
// set on a response that turn requests tools; otherwise it's a final
// assistant message.
type fakeAdapter struct {
	responses []LLMResponse
	calls     int
	lastMsgs  []Message
	lastTools []ToolSchema
}

func (f *fakeAdapter) Complete(_ context.Context, msgs []Message, tools []ToolSchema, _ CompleteOptions) (*LLMResponse, error) {
	f.lastMsgs = msgs
	f.lastTools = tools
	if f.calls >= len(f.responses) {
		return nil, errors.New("fake: ran out of scripted responses")
	}
	r := f.responses[f.calls]
	f.calls++
	return &r, nil
}

func (f *fakeAdapter) Stream(_ context.Context, msgs []Message, _ []ToolSchema, _ CompleteOptions) (<-chan StreamResult, error) {
	f.lastMsgs = msgs
	if f.calls >= len(f.responses) {
		return nil, errors.New("fake: ran out of scripted responses")
	}
	r := f.responses[f.calls]
	f.calls++
	ch := make(chan StreamResult, 4)
	content := r.Message.Content
	mid := len(content) / 2
	if mid > 0 {
		ch <- StreamResult{Delta: content[:mid]}
	}
	if mid < len(content) {
		ch <- StreamResult{Delta: content[mid:]}
	}
	for _, tc := range r.Message.ToolCalls {
		ch <- StreamResult{ToolCalls: []ToolCall{tc}}
	}
	if len(r.Message.ToolCalls) == 0 {
		ch <- StreamResult{Done: true}
	}
	close(ch)
	return ch, nil
}

func newTestComponents(t *testing.T, responses []LLMResponse) (*Conversation, *StreamSession, *fakeAdapter, *ToolRegistry) {
	t.Helper()
	adapter := &fakeAdapter{responses: responses}
	sm := NewSessionManager(nil, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 4, Logger: testLogger(t)}
	ns := "testns"
	conv := NewConversation(sm, router, tools, ns, cfg)
	streamer := NewStreamSession(conv, ns)
	return conv, streamer, adapter, tools
}

func executeSync(conv *Conversation, ctx context.Context, sessionID, input string) (*Session, string, error) {
	eventCh, err := conv.Execute(ctx, sessionID, input)
	if err != nil {
		return nil, "", err
	}
	var returnedSessionID string
	var reply string
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				return nil, "", te.Err
			}
			returnedSessionID = te.SessionID
			reply = te.Reply
		}
	}
	if returnedSessionID == "" {
		returnedSessionID = sessionID
	}
	session, lookupErr := conv.Sessions.GetOrCreate(ctx, conv.Namespace, returnedSessionID)
	if lookupErr != nil {
		return nil, reply, lookupErr
	}
	return session, reply, nil
}

func executeSyncWithTools(t *testing.T, conv *Conversation, ctx context.Context, sessionID, input string, toolNames ...string) (*Session, string, error) {
	t.Helper()
	session, err := conv.Sessions.GetOrCreate(ctx, conv.Namespace, sessionID)
	if err != nil {
		return nil, "", err
	}
	for _, name := range toolNames {
		session.EnableTool(name)
	}
	if err := conv.Sessions.Save(ctx, conv.Namespace, session); err != nil {
		return nil, "", err
	}
	return executeSync(conv, ctx, sessionID, input)
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestEngineChatNoTools(t *testing.T) {
	conv, _, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "hello back"}, FinishReason: "stop"},
	})
	session, reply, err := executeSync(conv, context.Background(), "", "hi")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if reply != "hello back" {
		t.Fatalf("expected reply %q, got: %q", "hello back", reply)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", adapter.calls)
	}
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant) in session, got %d", len(session.Messages))
	}
	if adapter.lastMsgs[0].Role != RoleSystem {
		t.Fatalf("system prompt not forwarded: %+v", adapter.lastMsgs[0])
	}
}

func TestEngineChatToolLoop(t *testing.T) {
	conv, _, adapter, tools := newTestComponents(t, []LLMResponse{
		{
			Message: Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID: "call-1", Name: "add", Arguments: `{"a":2,"b":3}`,
				}},
			},
			FinishReason: "tool_calls",
		},
		{
			Message:      Message{Role: RoleAssistant, Content: "the answer is 5"},
			FinishReason: "stop",
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "add"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct{ A, B float64 }
			_ = json.Unmarshal(raw, &a)
			b, _ := json.Marshal(map[string]float64{"sum": a.A + a.B})
			return string(b), nil
		},
	})
	_, reply, err := executeSyncWithTools(t, conv, context.Background(), "test-tool-loop", "add 2 and 3", "add")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if reply != "the answer is 5" {
		t.Fatalf("expected final reply %q, got: %q", "the answer is 5", reply)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected 2 LLM calls (tool request + final), got %d", adapter.calls)
	}
	var sawTool bool
	for _, m := range adapter.lastMsgs {
		if m.Role == RoleTool && strings.Contains(m.Content, `"sum":5`) {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatal("expected tool result with sum=5 in the final LLM call history")
	}
}

func TestEngineChatWithStreamSessionExecute(t *testing.T) {
	_, streamer, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "streamed hello"}, FinishReason: "stop"},
	})
	eventCh, err := streamer.Execute(context.Background(), "stream-test", "hi")
	if err != nil {
		t.Fatalf("StreamSession.Execute: %v", err)
	}
	var reply string
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
			reply = te.Reply
		}
	}
	if reply != "streamed hello" {
		t.Fatalf("expected reply %q, got: %q", "streamed hello", reply)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 Stream call, got %d", adapter.calls)
	}
}

func TestEngineChatTokenTracking(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "first"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		},
	})
	ctx := context.Background()
	session, _, err := executeSync(conv, ctx, "token-session", "hello")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if session.TotalTokenUsage() != 5 {
		t.Fatalf("expected 5 tokens after first call, got %d", session.TotalTokenUsage())
	}
}

func TestEngineChatTokensAccumulateAcrossTurns(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "first"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	})
	ctx := context.Background()
	session, _, err := executeSync(conv, ctx, "acc-session", "first msg")
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if session.TotalTokenUsage() != 15 {
		t.Fatalf("expected 15 tokens after first turn, got %d", session.TotalTokenUsage())
	}
}

// TestStreamSession_AutoRename verifies that auto-rename fires after a
// streaming turn with enough user messages. This is a regression test:
// StreamSession.runStream was not calling autoRenameIfNeeded (only
// Conversation.runEvents did), so streaming users never saw auto-rename.
func TestStreamSession_AutoRename(t *testing.T) {
	adapter := &namingTestAdapter{
		responses: []response{
			{msg: Message{Role: RoleAssistant, Content: "hello back"}},
		},
	}
	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	tools := NewToolRegistry()
	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)
	streamer := NewStreamSession(conv, "testns")

	// Seed the session with 2 user messages (so threshold is met).
	session, _ := sm.GetOrCreate(context.Background(), "testns", "stream-auto")
	session.Append(Message{Role: RoleUser, Content: "first message"})
	session.Append(Message{Role: RoleAssistant, Content: "first response"})
	session.Append(Message{Role: RoleUser, Content: "second message"})
	session.Append(Message{Role: RoleAssistant, Content: "second response"})
	_ = sm.Save(context.Background(), "testns", session)

	// Run a third turn via streaming — this should trigger auto-rename.
	eventCh, err := streamer.Execute(context.Background(), "stream-auto", "third message")
	if err != nil {
		t.Fatalf("StreamSession.Execute: %v", err)
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
		}
	}

	// Verify the session was renamed.
	session, _ = sm.GetOrCreate(context.Background(), "testns", "stream-auto")
	if session.Name != "My Test Session" {
		t.Fatalf("BUG CONFIRMED: expected session.Name = %q after auto-rename via stream, got %q", "My Test Session", session.Name)
	}
	if !adapter.NamingRequested {
		t.Fatal("BUG CONFIRMED: expected naming LLM call to have been made during streaming turn, but it was not")
	}
}

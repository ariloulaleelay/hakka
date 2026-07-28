package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
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

// ---------------------------------------------------------------------------
// LLM response duration tracking — informational metric
// ---------------------------------------------------------------------------

// TestUsageDurationIsMeasured verifies that the Duration field on the
// assistant message's Usage is populated after a non-streaming turn.
func TestUsageDurationIsMeasured(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "measured reply"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		},
	})
	session, _, err := executeSync(conv, context.Background(), "duration-test", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var found bool
	for _, m := range session.Messages() {
		if m.Role == RoleAssistant && m.Content == "measured reply" {
			found = true
			if m.Usage == nil {
				t.Fatal("BUG CONFIRMED: assistant message has nil Usage — duration not stored")
			}
			if m.Usage.Duration <= 0 {
				t.Fatalf("BUG CONFIRMED: expected Duration > 0, got %v", m.Usage.Duration)
			}
			t.Logf("OK: Duration = %v", m.Usage.Duration)
			break
		}
	}
	if !found {
		t.Fatal("expected assistant message not found in session")
	}
}

// TestUsageDurationTrackedInEvent verifies that the Duration is reported
// in the UsageReported event emitted during a turn.
func TestUsageDurationTrackedInEvent(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "event duration"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		},
	})
	eventCh, err := conv.Execute(context.Background(), "duration-event-test", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var foundUsage bool
	for evt := range eventCh {
		if ur, ok := evt.(event.UsageReported); ok {
			foundUsage = true
			if ur.Usage.Duration <= 0 {
				t.Fatalf("BUG CONFIRMED: expected Duration > 0 in UsageReported event, got %v", ur.Usage.Duration)
			}
			t.Logf("OK: UsageReported Duration = %v", ur.Usage.Duration)
		}
	}
	if !foundUsage {
		t.Fatal("BUG CONFIRMED: expected UsageReported event to be emitted")
	}
}

// TestUsageDurationJSONRoundTrip verifies that Duration survives JSON
// serialization/deserialization (relevant for SQLite store).
func TestUsageDurationJSONRoundTrip(t *testing.T) {
	msg := Message{
		Role:    RoleAssistant,
		Content: "hello world",
		Usage:   &Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5, Duration: 1234567890}, // ~1.23 seconds
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.Usage == nil {
		t.Fatal("BUG CONFIRMED: Usage lost after JSON round-trip")
	}
	if restored.Usage.Duration != 1234567890 {
		t.Fatalf("expected Duration=1234567890, got %v", restored.Usage.Duration)
	}
}

// TestUsageDurationViaStream verifies that the Duration field is populated
// when using the streaming path (StreamSession.Execute).
func TestUsageDurationViaStream(t *testing.T) {
	_, streamer, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "stream duration"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 7, CompletionTokens: 4, TotalTokens: 11},
		},
	})
	eventCh, err := streamer.Execute(context.Background(), "stream-duration", "hi")
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

	// Retrieve the session and check the assistant message has duration set.
	sm := streamer.conv.sessions
	session, err := sm.GetOrCreate(context.Background(), "testns", "stream-duration")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	var found bool
	for _, m := range session.Messages() {
		if m.Role == RoleAssistant && m.Content == "stream duration" {
			found = true
			if m.Usage == nil {
				t.Fatal("BUG CONFIRMED: streaming assistant message has nil Usage")
			}
			if m.Usage.Duration <= 0 {
				t.Fatalf("BUG CONFIRMED: expected Duration > 0 for streaming path, got %v", m.Usage.Duration)
			}
			t.Logf("OK: Stream Duration = %v", m.Usage.Duration)
			break
		}
	}
	if !found {
		t.Fatal("expected streaming assistant message not found in session")
	}
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
		ch <- StreamResult{ToolCalls: []ToolCall{tc}, Usage: r.Usage, FinishReason: r.FinishReason}
	}
	if len(r.Message.ToolCalls) == 0 {
		ch <- StreamResult{Done: true, Usage: r.Usage, FinishReason: r.FinishReason}
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
	session, lookupErr := conv.sessions.GetOrCreate(ctx, conv.namespace, returnedSessionID)
	if lookupErr != nil {
		return nil, reply, lookupErr
	}
	return session, reply, nil
}

func executeSyncWithTools(t *testing.T, conv *Conversation, ctx context.Context, sessionID, input string, toolNames ...string) (*Session, string, error) {
	t.Helper()
	session, err := conv.sessions.GetOrCreate(ctx, conv.namespace, sessionID)
	if err != nil {
		return nil, "", err
	}
	for _, name := range toolNames {
		session.EnableTool(name)
	}
	if err := conv.sessions.Save(ctx, conv.namespace, session); err != nil {
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
	if len(session.Messages()) != 2 {
		t.Fatalf("expected 2 messages (user + assistant) in session, got %d", len(session.Messages()))
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

// TestMessageUsageStored verifies that per-message token usage from the
// provider is stored on the assistant Message, not just accumulated in
// Session.TotalTokens.
func TestMessageUsageStored(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "hi there"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	})
	session, _, err := executeSync(conv, context.Background(), "usage-on-msg", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Find the assistant message — it should have Usage set.
	var found bool
	for _, m := range session.Messages() {
		if m.Role == RoleAssistant && m.Content == "hi there" {
			found = true
			if m.Usage == nil {
				t.Fatal("BUG CONFIRMED: assistant message has nil Usage — provider-reported tokens not stored on message")
			}
			if m.Usage.PromptTokens != 10 {
				t.Fatalf("expected PromptTokens=10, got %d", m.Usage.PromptTokens)
			}
			if m.Usage.CompletionTokens != 5 {
				t.Fatalf("expected CompletionTokens=5, got %d", m.Usage.CompletionTokens)
			}
			if m.Usage.TotalTokens != 15 {
				t.Fatalf("expected TotalTokens=15, got %d", m.Usage.TotalTokens)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected assistant message not found in session")
	}
	if session.TotalTokenUsage() != 15 {
		t.Fatalf("expected 15 total tokens on session, got %d", session.TotalTokenUsage())
	}
}

// TestMessageUsageJSONRoundTrip verifies that per-message usage survives
// JSON serialization/deserialization (relevant for SQLite store).
func TestMessageUsageJSONRoundTrip(t *testing.T) {
	msg := Message{
		Role:    RoleAssistant,
		Content: "hello world",
		Usage:   &Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.Usage == nil {
		t.Fatal("BUG CONFIRMED: Usage lost after JSON round-trip")
	}
	if restored.Usage.PromptTokens != 2 {
		t.Fatalf("expected PromptTokens=2, got %d", restored.Usage.PromptTokens)
	}
	if restored.Usage.CompletionTokens != 3 {
		t.Fatalf("expected CompletionTokens=3, got %d", restored.Usage.CompletionTokens)
	}
	if restored.Usage.TotalTokens != 5 {
		t.Fatalf("expected TotalTokens=5, got %d", restored.Usage.TotalTokens)
	}
}

// TestMessageUsageViaStream verifies that per-message token usage is stored
// even when using the streaming path (StreamSession.Execute).
func TestMessageUsageViaStream(t *testing.T) {
	_, streamer, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "streamed reply"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 7, CompletionTokens: 4, TotalTokens: 11},
		},
	})
	eventCh, err := streamer.Execute(context.Background(), "stream-usage", "hi")
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

	// Retrieve the session and check the assistant message has usage.
	sm := streamer.conv.sessions
	session, err := sm.GetOrCreate(context.Background(), "testns", "stream-usage")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	var found bool
	for _, m := range session.Messages() {
		if m.Role == RoleAssistant && m.Content == "streamed reply" {
			found = true
			if m.Usage == nil {
				t.Fatal("BUG CONFIRMED: streaming assistant message has nil Usage")
			}
			if m.Usage.TotalTokens != 11 {
				t.Fatalf("expected TotalTokens=11 via stream, got %d", m.Usage.TotalTokens)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected streaming assistant message not found in session")
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
	if session.SessionName() != "My Test Session" {
		t.Fatalf("BUG CONFIRMED: expected session.SessionName() = %q after auto-rename via stream, got %q", "My Test Session", session.SessionName())
	}
	if !adapter.NamingRequested {
		t.Fatal("BUG CONFIRMED: expected naming LLM call to have been made during streaming turn, but it was not")
	}
}

// ---------------------------------------------------------------------------
// Save-failure propagation tests
// ---------------------------------------------------------------------------

// errStore is a SessionStore wrapper that fails Put calls once
// failAfterPuts puts have succeeded.
type errStore struct {
	SessionStore
	failCount    int // number of Puts that should fail (0 = never)
	putCalls     int
}

func (es *errStore) Put(ctx context.Context, namespace string, s *Session) error {
	es.putCalls++
	if es.failCount > 0 && es.putCalls > es.failCount {
		return errors.New("disk full")
	}
	return es.SessionStore.Put(ctx, namespace, s)
}

// TestEngineChat_SaveFailurePropagatesToTurnFinished verifies that when the
// final session save fails after a successful LLM turn, the error is
// propagated via TurnFinished.Err — not silently swallowed.
func TestEngineChat_SaveFailurePropagatesToTurnFinished(t *testing.T) {
	adapter := &fakeAdapter{
		responses: []LLMResponse{
			{Message: Message{Role: RoleAssistant, Content: "hello"}, FinishReason: "stop"},
		},
	}
	memStore := NewMemoryStore()
	// Seed the session first so prepareWithInput succeeds.
	seedSession := NewSession("testns", "sys")
	seedSession.Update(func(d *SessionData) { d.ID = "save-fail-test" })
	if err := memStore.Put(context.Background(), "testns", seedSession); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Now wrap with errStore that lets the initial creates succeed but
	// fails after 2 successful Puts (session creation + user message).
	failStore := &errStore{SessionStore: memStore, failCount: 1}
	sm := NewSessionManager(failStore, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 4, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	eventCh, err := conv.Execute(context.Background(), "save-fail-test", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var turnErr error
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			turnErr = te.Err
		}
	}

	if turnErr == nil {
		t.Fatal("BUG CONFIRMED: TurnFinished.Err is nil — save failure was silently swallowed")
	}
	if !strings.Contains(turnErr.Error(), "disk full") {
		t.Fatalf("expected error containing 'disk full', got: %v", turnErr)
	}
}

// TestEngineChat_SaveFailureBeforeAutoRename verifies that auto-rename is
// skipped when the final session save fails.
func TestEngineChat_SaveFailureBeforeAutoRename(t *testing.T) {
	adapter := &namingTestAdapter{
		responses: []response{
			{msg: Message{Role: RoleAssistant, Content: "hello back"}},
		},
	}
	memStore := NewMemoryStore()
	// Seed the session with 2 user messages to enable auto-rename.
	seedSession := NewSession("testns", "sys")
	seedSession.Update(func(d *SessionData) { d.ID = "auto-fail" })
	seedSession.Append(Message{Role: RoleUser, Content: "first"})
	seedSession.Append(Message{Role: RoleAssistant, Content: "resp1"})
	seedSession.Append(Message{Role: RoleUser, Content: "second"})
	seedSession.Append(Message{Role: RoleAssistant, Content: "resp2"})
	if err := memStore.Put(context.Background(), "testns", seedSession); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// After seeding, fail on every Put (user message save will also fail — that's OK,
	// the turn error should still propagate).
	failStore := &errStore{SessionStore: memStore, failCount: 1}
	sm := NewSessionManager(failStore, "sys")
	tools := NewToolRegistry()
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)
	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	eventCh, err := conv.Execute(context.Background(), "auto-fail", "third")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var turnErr error
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			turnErr = te.Err
		}
	}
	if turnErr == nil {
		t.Fatal("BUG CONFIRMED: TurnFinished.Err is nil — save failure was silently swallowed")
	}
	if !strings.Contains(turnErr.Error(), "disk full") {
		t.Fatalf("expected error containing 'disk full', got: %v", turnErr)
	}

	// Verify the adapter's naming was NOT requested (auto-rename skipped because save failed).
	if adapter.NamingRequested {
		t.Fatal("auto-rename should NOT have been requested — session save failed so naming would be lost")
	}
}

// ---------------------------------------------------------------------------
// Execute with empty input — no user message appended (used by /continue)
// ---------------------------------------------------------------------------

func TestConversationExecuteEmptyInput(t *testing.T) {
	conv, _, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "first reply"}, FinishReason: "stop"},
		{Message: Message{Role: RoleAssistant, Content: "continued"}, FinishReason: "stop"},
	})

	// First, execute a normal turn to add some history.
	session, _, err := executeSync(conv, context.Background(), "resume-test", "hello")
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	initialMsgCount := len(session.Messages())

	// Now resume — should NOT add a user message.
	eventCh, err := conv.Execute(context.Background(), "resume-test", "")
	if err != nil {
		t.Fatalf("Conversation.Execute with empty input: %v", err)
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
	if reply != "continued" {
		t.Fatalf("expected reply %q, got: %q", "continued", reply)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected 2 LLM calls total (1 Execute + 1 empty Execute), got %d", adapter.calls)
	}

	// Verify no new user message was appended.
	session, _ = conv.sessions.GetOrCreate(context.Background(), "testns", "resume-test")
	if len(session.Messages()) != initialMsgCount+1 {
		t.Fatalf("expected %d messages (initial + new assistant reply), got %d", initialMsgCount+1, len(session.Messages()))
	}
	lastMsg := session.Messages()[len(session.Messages())-1]
	if lastMsg.Role != RoleAssistant {
		t.Fatalf("expected last message to be assistant, got %s", lastMsg.Role)
	}
}

// ---------------------------------------------------------------------------
// Execute with empty input — no user message appended (used by /continue)
// ---------------------------------------------------------------------------

func TestStreamSessionExecuteEmptyInput(t *testing.T) {
	_, streamer, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "first stream reply"}, FinishReason: "stop"},
		{Message: Message{Role: RoleAssistant, Content: "stream continued"}, FinishReason: "stop"},
	})

	// First, execute a normal turn to create the session.
	eventCh, err := streamer.Execute(context.Background(), "stream-empty-test", "hello")
	if err != nil {
		t.Fatalf("first StreamSession.Execute: %v", err)
	}
	for evt := range eventCh {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
		}
	}

	// Count existing messages.
	sm := streamer.conv.sessions
	session, _ := sm.GetOrCreate(context.Background(), "testns", "stream-empty-test")
	initialMsgCount := len(session.Messages())

	// Now execute with empty input — should NOT add a user message.
	eventCh2, err := streamer.Execute(context.Background(), "stream-empty-test", "")
	if err != nil {
		t.Fatalf("StreamSession.Execute with empty input: %v", err)
	}
	var reply string
	for evt := range eventCh2 {
		if te, ok := evt.(event.TurnFinished); ok {
			if te.Err != nil {
				t.Fatalf("TurnFinished error: %v", te.Err)
			}
			reply = te.Reply
		}
	}
	if reply != "stream continued" {
		t.Fatalf("expected reply %q, got: %q", "stream continued", reply)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected 2 Stream calls total (1 Execute + 1 empty Execute), got %d", adapter.calls)
	}

	// Verify no new user message was appended.
	session, _ = sm.GetOrCreate(context.Background(), "testns", "stream-empty-test")
	if len(session.Messages()) != initialMsgCount+1 {
		t.Fatalf("expected %d messages (initial + new assistant reply), got %d", initialMsgCount+1, len(session.Messages()))
	}
	lastMsg := session.Messages()[len(session.Messages())-1]
	if lastMsg.Role != RoleAssistant {
		t.Fatalf("expected last message to be assistant, got %s", lastMsg.Role)
	}
}


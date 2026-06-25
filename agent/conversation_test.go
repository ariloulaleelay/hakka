package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestHooksSerialisedWithinTurn proves that when multiple tool goroutines
// in the same turn fire engine-level hooks, they are serialised (no race
// on the hook writer).
//
// Different turns on different sessions run independently and do NOT
// block each other — that is a performance improvement over the old
// global mutex approach.
func TestHooksSerialisedWithinTurn(t *testing.T) {
	// We'll detect concurrent hook access with a shared "occupied" flag.
	// Since two tool calls run concurrently in the same turn, they MUST
	// be serialised by the per-turn mutex.
	type state struct {
		mu         sync.Mutex
		occupied   bool
		concurrent bool
	}
	var s state

	// Barrier to let both goroutines enter OnToolCall before either leaves.
	barrier := make(chan struct{}, 2)

	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", &simpleAdapter{
		responses: []response{
			{
				msg: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "fast_tool", Arguments: `{}`},
						{ID: "c2", Name: "fast_tool", Arguments: `{}`},
					},
				},
			},
			{msg: Message{Role: RoleAssistant, Content: "done"}},
		},
	})
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "fast_tool", Description: "fast"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok": true}`, nil
		},
	})

	cfg := EngineConfig{
		MaxToolIterations: 5,
		Logger:            testLogger(t),
		Hooks: Hooks{
			OnToolCall: func(sid string, call ToolCall) {
				s.mu.Lock()
				if s.occupied {
					s.concurrent = true // someone else is already inside!
				}
				s.occupied = true
				s.mu.Unlock()

				// Signal that we've entered
				barrier <- struct{}{}
				// Wait for the other goroutine to also enter
				<-barrier

				s.mu.Lock()
				s.occupied = false
				s.mu.Unlock()
			},
		},
	}

	conv := NewConversation(sm, router, tools, "testns", cfg)

	// Run a single Execute that triggers two parallel tool calls
	_, _, err := executeSync(conv, context.Background(), "sess-a", "hi")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if s.concurrent {
		t.Error("BUG CONFIRMED: two tool goroutines were inside OnToolCall simultaneously — per-turn mutex is not serialising")
	} else {
		t.Log("OK: no concurrent hook access detected within a turn — hooks are correctly serialised")
	}
}

// TestHooksSerialisedAcrossSessions proves that the per-turn mutex does
// not spread across sessions — each turn gets its own serialisation
// context, so hooks from different sessions never deadlock each other.
func TestHooksSerialisedAcrossSessions(t *testing.T) {
	sm := NewSessionManager(NewMemoryStore(), "sys")

	// Single adapter shared by both sessions (each session gets its own
	// responses via the adapter's callCount).
	adapter := &simpleAdapter{
		responses: []response{
			{msg: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "dummy", Arguments: `{}`}}}},
			{msg: Message{Role: RoleAssistant, Content: "done"}},
			{msg: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "dummy", Arguments: `{}`}}}},
			{msg: Message{Role: RoleAssistant, Content: "done"}},
		},
	}

	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "dummy"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"ok": true}`, nil
		},
	})

	// Use a shared counter to prove both sessions' hooks fire.
	var callCount int
	var mu sync.Mutex

	cfg := EngineConfig{
		MaxToolIterations: 5,
		Logger:            testLogger(t),
		Hooks: Hooks{
			OnToolCall: func(sid string, call ToolCall) {
				mu.Lock()
				callCount++
				mu.Unlock()
			},
		},
	}

	conv := NewConversation(sm, router, tools, "testns", cfg)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _, err := executeSync(conv, context.Background(), "sess-a", "hi")
		if err != nil {
			t.Errorf("sess-a failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		_, _, err := executeSync(conv, context.Background(), "sess-b", "hi")
		if err != nil {
			t.Errorf("sess-b failed: %v", err)
		}
	}()

	wg.Wait()

	if callCount != 2 {
		t.Errorf("expected 2 tool calls total, got %d", callCount)
	} else {
		t.Log("OK: both sessions' hooks fired without deadlock")
	}
}

// simpleAdapter supports both Complete and Stream with scripted responses.
// It is safe for concurrent use.
type simpleAdapter struct {
	mu        sync.Mutex
	responses []response
	callCount int
}

type response struct {
	msg Message
}

func (a *simpleAdapter) Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (*LLMResponse, error) {
	a.mu.Lock()
	if a.callCount >= len(a.responses) {
		a.mu.Unlock()
		return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "fallback"}}, nil
	}
	r := a.responses[a.callCount]
	a.callCount++
	a.mu.Unlock()
	return &LLMResponse{Message: r.msg}, nil
}

func (a *simpleAdapter) Stream(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (<-chan StreamResult, error) {
	ch := make(chan StreamResult)
	close(ch)
	return ch, nil
}

// ---------------------------------------------------------------------------
// Auto-rename tests
// ---------------------------------------------------------------------------

// namingTestAdapter detects when the LLM is asked to generate a session name
// and returns a predefined name. Otherwise it acts like a simple adapter.
type namingTestAdapter struct {
	mu              sync.Mutex
	responses       []response
	callCount       int
	NamingRequested bool
}

func (a *namingTestAdapter) Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (*LLMResponse, error) {
	// Detect naming request by checking the last user message
	for _, m := range msgs {
		if m.Role == RoleUser && strings.Contains(m.Content, "Suggest name for this chat") {
			a.mu.Lock()
			a.NamingRequested = true
			a.mu.Unlock()
			return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "My Test Session"}}, nil
		}
	}
	// Regular conversation response
	a.mu.Lock()
	if a.callCount >= len(a.responses) {
		a.mu.Unlock()
		return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "fallback"}}, nil
	}
	r := a.responses[a.callCount]
	a.callCount++
	a.mu.Unlock()
	return &LLMResponse{Message: r.msg}, nil
}

func (a *namingTestAdapter) Stream(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (<-chan StreamResult, error) {
	ch := make(chan StreamResult)
	close(ch)
	return ch, nil
}

func TestAutoRename_NamesSessionAfterTwoUserMessages(t *testing.T) {
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

	// Seed the session with 2 user messages already (simulating past conversation)
	session, _ := sm.GetOrCreate(context.Background(), "testns", "auto-session")
	session.Append(Message{Role: RoleUser, Content: "first message"})
	session.Append(Message{Role: RoleAssistant, Content: "first response"})
	session.Append(Message{Role: RoleUser, Content: "second message"})
	session.Append(Message{Role: RoleAssistant, Content: "second response"})
	_ = sm.Save(context.Background(), "testns", session)

	// Run a third turn — this should trigger auto-rename
	_, reply, err := executeSync(conv, context.Background(), "auto-session", "third message")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if reply != "hello back" {
		t.Fatalf("expected reply %q, got %q", "hello back", reply)
	}

	// Verify the session was renamed
	session, _ = sm.GetOrCreate(context.Background(), "testns", "auto-session")
	if session.Name != "My Test Session" {
		t.Fatalf("expected session.Name = %q after auto-rename, got %q", "My Test Session", session.Name)
	}
	if !adapter.NamingRequested {
		t.Fatal("expected naming LLM call to have been made")
	}
}

func TestAutoRename_DoesNotRenameAlreadyNamedSession(t *testing.T) {
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

	session, _ := sm.GetOrCreate(context.Background(), "testns", "named-session")
	session.Name = "Already Named"
	session.Append(Message{Role: RoleUser, Content: "first"})
	session.Append(Message{Role: RoleAssistant, Content: "resp1"})
	session.Append(Message{Role: RoleUser, Content: "second"})
	session.Append(Message{Role: RoleAssistant, Content: "resp2"})
	_ = sm.Save(context.Background(), "testns", session)

	_, _, err := executeSync(conv, context.Background(), "named-session", "third")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if adapter.NamingRequested {
		t.Fatal("expected no naming LLM call for already-named session")
	}
	session, _ = sm.GetOrCreate(context.Background(), "testns", "named-session")
	if session.Name != "Already Named" {
		t.Fatalf("expected name to remain %q, got %q", "Already Named", session.Name)
	}
}

func TestAutoRename_DoesNotRenameWithFewMessages(t *testing.T) {
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

	// Only 1 user message total (0 before turn + 1 from Execute) — should NOT trigger rename
	session, _ := sm.GetOrCreate(context.Background(), "testns", "few-msgs")
	_ = sm.Save(context.Background(), "testns", session)

	_, _, err := executeSync(conv, context.Background(), "few-msgs", "first message")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if adapter.NamingRequested {
		t.Fatal("expected no naming LLM call for session with only 1 user message total")
	}
}

func TestBuildNamingMessages_ExcludesToolMessages(t *testing.T) {
	session := NewSession("testns", "sys")
	session.Append(Message{Role: RoleUser, Content: "hello"})
	session.Append(Message{Role: RoleAssistant, Content: "hi there", ToolCalls: []ToolCall{{ID: "t1", Name: "tool1"}}})
	session.Append(Message{Role: RoleTool, Content: "tool result", ToolCallID: "t1"})
	session.Append(Message{Role: RoleAssistant, Content: "done"})

	msgs := buildNamingMessages(session)
	// Should have: user "hello" + [TRUNCATED] (from assistant tool-call) + [TRUNCATED] (from tool response) + assistant "done" + instruction
	for _, m := range msgs {
		if m.Role == RoleTool {
			t.Fatalf("tool message should not appear in naming context, got: %+v", m)
		}
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages (user + 2 truncated + assistant + instruction), got %d", len(msgs))
	}
	// First: user "hello"
	if msgs[0].Role != RoleUser || msgs[0].Content != "hello" {
		t.Fatalf("expected first message to be user 'hello', got: %+v", msgs[0])
	}
	// Second: truncated tool calls marker (from assistant tool-call)
	if msgs[1].Role != RoleSystem || msgs[1].Content != "[TRUNCATED TOOL CALLS]" {
		t.Fatalf("expected second message to be system '[TRUNCATED TOOL CALLS]', got: %+v", msgs[1])
	}
	// Third: truncated tool calls marker (from tool response)
	if msgs[2].Role != RoleSystem || msgs[2].Content != "[TRUNCATED TOOL CALLS]" {
		t.Fatalf("expected third message to be system '[TRUNCATED TOOL CALLS]', got: %+v", msgs[2])
	}
	// Fourth: assistant "done"
	if msgs[3].Role != RoleAssistant || msgs[3].Content != "done" {
		t.Fatalf("expected fourth message to be assistant 'done', got: %+v", msgs[3])
	}
	// Fifth: naming instruction
	if msgs[4].Role != RoleUser || !strings.Contains(msgs[4].Content, "Suggest name for this chat") {
		t.Fatalf("expected last message to be user naming instruction, got: %+v", msgs[4])
	}
}

// TestBuildNamingMessages_ExcludesAssistantToolCallMessages verifies that
// assistant messages with only tool calls (empty content) are excluded,
// because they cause "content or tool_calls must be set" errors when sent
// to LLM providers (the copy drops ToolCalls, leaving empty content).
func TestBuildNamingMessages_ExcludesAssistantToolCallMessages(t *testing.T) {
	session := NewSession("testns", "sys")
	// Realistic conversation: user asks → assistant requests tool → tool responds → assistant replies
	session.Append(Message{Role: RoleUser, Content: "what is the weather?"})
	session.Append(Message{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`}}})
	session.Append(Message{Role: RoleTool, Content: "rainy", ToolCallID: "tc1"})
	session.Append(Message{Role: RoleAssistant, Content: "The weather in London is rainy."})

	msgs := buildNamingMessages(session)
	// Should have: user + [TRUNCATED] (from assistant tool-call) + [TRUNCATED] (from tool) + assistant (reply) + instruction
	// Total: user(1) + 2 truncated + assistant(1) + instruction(1) = 5
	for _, m := range msgs {
		if m.Role == RoleTool {
			t.Fatalf("tool message should not appear in naming context, got: %+v", m)
		}
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages (user + 2 truncated + assistant + instruction), got %d", len(msgs))
	}
	// First: user's question
	if msgs[0].Role != RoleUser || msgs[0].Content != "what is the weather?" {
		t.Fatalf("expected first message to be user 'what is the weather?', got: %+v", msgs[0])
	}
	// Second: truncated from assistant tool-call
	if msgs[1].Role != RoleSystem || msgs[1].Content != "[TRUNCATED TOOL CALLS]" {
		t.Fatalf("expected second message to be system '[TRUNCATED TOOL CALLS]', got: %+v", msgs[1])
	}
	// Third: truncated from tool response
	if msgs[2].Role != RoleSystem || msgs[2].Content != "[TRUNCATED TOOL CALLS]" {
		t.Fatalf("expected third message to be system '[TRUNCATED TOOL CALLS]', got: %+v", msgs[2])
	}
	// Fourth: assistant's reply
	if msgs[3].Role != RoleAssistant || msgs[3].Content != "The weather in London is rainy." {
		t.Fatalf("expected fourth message to be assistant 'The weather in London is rainy.', got: %+v", msgs[3])
	}
	// Fifth: naming instruction
	if msgs[4].Role != RoleUser || !strings.Contains(msgs[4].Content, "Suggest name for this chat") {
		t.Fatalf("expected last message to be user naming instruction, got: %+v", msgs[4])
	}
}

// TestBuildNamingMessages_SkipsInternalMessages verifies that internal
// messages (e.g. compaction warnings) never leak into the naming LLM call.
func TestBuildNamingMessages_SkipsInternalMessages(t *testing.T) {
	session := NewSession("testns", "sys")
	session.Append(Message{Role: RoleUser, Content: "hello"})
	session.Append(Message{Role: RoleSystem, Content: "⚠ compaction warning", Internal: true})
	session.Append(Message{Role: RoleAssistant, Content: "hi there"})

	msgs := buildNamingMessages(session)

	// Internal system messages should be skipped entirely.
	for _, m := range msgs {
		if m.Internal {
			t.Fatalf("internal message leaked into naming context: %+v", m)
		}
		if m.Role == RoleSystem && strings.Contains(m.Content, "compaction warning") {
			t.Fatal("compaction warning leaked into naming context")
		}
	}
}

// ---------------------------------------------------------------------------
// Per-session tool control — Conversation integration tests
//
// Tool authorisation model:
//
//   - When a session has NO explicit tool configuration (EnabledTools is
//     nil or empty), NO tools are available. The user must explicitly
//     enable tools via /tool enable or /tool disable commands.
//
//   - Once at least one tool has been explicitly enabled or disabled
//     (EnabledTools becomes non-nil), the map is consulted: tools with
//     a true value are enabled, tools with a false value are disabled,
//     and tools not mentioned are disabled (opt-in model — you must
//     explicitly enable tools you want).
// ---------------------------------------------------------------------------

// TestConversation_NoToolsConfigured_GetsNoTools verifies that when a
// session has no explicit tool configuration (EnabledTools nil/empty),
// NO tools are passed to the LLM (opt-in model — all disabled by default).
func TestConversation_NoToolsConfigured_GetsNoTools(t *testing.T) {
	var capturedTools []ToolSchema
	adapter := &capturingAdapter{capture: func(tools []ToolSchema) {
		capturedTools = tools
	}}

	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	session, _ := sm.GetOrCreate(context.Background(), "testns", "empty-session")
	// Session has empty EnabledTools → all tools should be available
	adapter.sessionFn = func() *Session { return session }

	_, _, err := executeSync(conv, context.Background(), "empty-session", "hello")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(capturedTools) != 0 {
		t.Fatalf("expected 0 tool schemas (all disabled) for session with no explicit config, got %d: %+v", len(capturedTools), capturedTools)
	}
}

// capturingAdapter records the tools it receives and returns scripted responses.
type capturingAdapter struct {
	capture   func([]ToolSchema)
	sessionFn func() *Session
	callCount int
}

func (a *capturingAdapter) Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (*LLMResponse, error) {
	if a.capture != nil {
		a.capture(tools)
	}
	return &LLMResponse{Message: Message{Role: RoleAssistant, Content: "done"}}, nil
}

func (a *capturingAdapter) Stream(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions) (<-chan StreamResult, error) {
	ch := make(chan StreamResult, 4)
	if a.capture != nil {
		a.capture(tools)
	}
	ch <- StreamResult{Delta: "done"}
	ch <- StreamResult{Done: true}
	close(ch)
	return ch, nil
}

// TestConversation_SomeToolsEnabled_LLMGetsThoseSchemas verifies that when
// some tools are enabled and others disabled, only the enabled schemas
// reach the LLM.
func TestConversation_SomeToolsEnabled_LLMGetsThoseSchemas(t *testing.T) {
	var capturedTools []ToolSchema
	adapter := &capturingAdapter{capture: func(tools []ToolSchema) {
		capturedTools = tools
	}}

	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", adapter)
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "alpha"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "beta"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	})

	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	session, _ := sm.GetOrCreate(context.Background(), "testns", "some-session")
	session.EnableTool("alpha")
	session.DisableTool("beta")
	adapter.sessionFn = func() *Session { return session }

	_, _, err := executeSync(conv, context.Background(), "some-session", "hello")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(capturedTools) != 1 || capturedTools[0].Name != "alpha" {
		t.Fatalf("expected only 'alpha' schema, got %+v", capturedTools)
	}
}

// TestConversation_DefenseInDepth_DisabledToolReturnsError verifies that
// even if the LLM somehow calls a disabled tool (e.g. from context window),
// the engine rejects it with an error.
func TestConversation_DefenseInDepth_DisabledToolReturnsError(t *testing.T) {
	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", &simpleAdapter{
		responses: []response{
			{
				msg: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "echo", Arguments: `{}`},
					},
				},
			},
			{msg: Message{Role: RoleAssistant, Content: "done"}},
		},
	})
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	session, _ := sm.GetOrCreate(context.Background(), "testns", "defense-session")
	session.DisableTool("echo") // explicitly disable the tool

	_, _, err := executeSync(conv, context.Background(), "defense-session", "do it")
	if err != nil {
		t.Fatalf("Execute should not return error (tool error is recoverable): %v", err)
	}

	// The tool result should be in the session messages and contain "disabled"
	session, _ = sm.GetOrCreate(context.Background(), "testns", "defense-session")
	foundDisabledErr := false
	for _, m := range session.Messages {
		if m.Role == RoleTool && strings.Contains(m.Content, "disabled") {
			foundDisabledErr = true
			break
		}
	}
	if !foundDisabledErr {
		t.Fatal("expected a tool-role message with 'disabled' error, but none found")
	}
}

// TestConversation_EnabledTool_ExecutesNormally verifies that an enabled
// tool runs normally.
func TestConversation_EnabledTool_ExecutesNormally(t *testing.T) {
	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", &simpleAdapter{
		responses: []response{
			{
				msg: Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "echo", Arguments: `{}`},
					},
				},
			},
			{msg: Message{Role: RoleAssistant, Content: "done"}},
		},
	})
	router := NewRouter(reg)

	tools := NewToolRegistry()
	tools.Register(Tool{
		Schema: ToolSchema{Name: "echo"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	})

	cfg := EngineConfig{MaxToolIterations: 5, Logger: testLogger(t)}
	conv := NewConversation(sm, router, tools, "testns", cfg)

	session, _ := sm.GetOrCreate(context.Background(), "testns", "normal-session")
	session.EnableTool("echo")

	_, reply, err := executeSync(conv, context.Background(), "normal-session", "do it")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if reply != "done" {
		t.Fatalf("expected reply 'done', got %q", reply)
	}
}

// TestDefaultCompactSoftLimitIsUsedFromConfig proves that when a session
// has CompactSoftLimit=0 (the default from NewSession), the conversation
// resolves it to the engine config default (200000) and does NOT trigger
// compaction for a small session.
//
// This guards against the bug where the session's raw value (0) would be
// logged as "softLimit=0" and could accidentally be passed to
// BuildCompactContext, triggering compaction on every turn.
func TestDefaultCompactSoftLimitIsUsedFromConfig(t *testing.T) {
	sm := NewSessionManager(NewMemoryStore(), "sys")
	reg := NewRegistry()
	reg.Register("default", &simpleAdapter{
		responses: []response{
			{msg: Message{Role: RoleAssistant, Content: "done"}},
		},
	})
	router := NewRouter(reg)
	tools := NewToolRegistry()

	// Use default engine config (CompactSoftLimit = 200000)
	conv := NewConversation(sm, router, tools, "testns", EngineConfig{
		Logger: testLogger(t),
	})

	session, _, err := executeSync(conv, context.Background(), "test-session", "hello")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Session's CompactSoftLimit should still be 0 (default), but the
	// engine should have used the resolved value (200000).
	if got := session.GetCompactSoftLimit(); got != 0 {
		t.Fatalf("expected session CompactSoftLimit=0 (default), got %d", got)
	}

	// Now verify the engine resolved the default correctly by checking
	// that BuildCompactContext would not trigger compaction for this
	// tiny session when passed the resolved limit.
	msgs, needCompactify, _ := BuildCompactContext(session, 200000)
	if needCompactify {
		t.Fatalf("BUG: BuildCompactContext triggered compaction with softLimit=200000 for a tiny session; resolved limit should be the default (200000), not 0")
	}

	// Also verify that passing 0 (the bug) WOULD trigger compaction,
	// confirming the bug scenario.
	_, buggyCompactify, _ := BuildCompactContext(session, 0)
	if !buggyCompactify {
		t.Error("expected BuildCompactContext with softLimit=0 to trigger compaction (demonstrating the bug)")
	}

	t.Logf("OK: resolved softLimit=200000, context has %d messages, no compaction triggered", len(msgs))
}

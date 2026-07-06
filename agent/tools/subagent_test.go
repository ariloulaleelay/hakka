package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// subagent_run tests
// ---------------------------------------------------------------------------

func TestSubagentRun_Basic(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	// Create a parent session with some history.
	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{Role: agent.RoleUser, Content: "What is the capital of France?"})
	parent.Append(agent.Message{Role: agent.RoleAssistant, Content: "The capital of France is Paris."})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}

	// Enable some tools on the parent (including subagent_run).
	parent.EnableTool("read_file")
	parent.EnableTool("subagent_run")

	// Set up router with a mock adapter that returns a deterministic response.
	adapter := &askAdapter{
		t:        t,
		response: "The subagent processed your task. Result: 42.",
	}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(router, tools, cfg, nil)

	// Build a context that mimics what the engine injects before calling
	// a tool handler.
	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "Process this data and return the answer.",
	})

	// The result should contain the subagent's reply and the metadata footer.
	if !strings.Contains(res, "Subagent result:") {
		t.Fatalf("expected 'Subagent result:' header, got: %q", res)
	}
	if !strings.Contains(res, "Result: 42") {
		t.Fatalf("expected subagent reply 'Result: 42' in output, got: %q", res)
	}
	if !strings.Contains(res, "Tokens:") {
		t.Fatalf("expected token count in output, got: %q", res)
	}
	if !strings.Contains(res, "Cost:") {
		t.Fatalf("expected cost in output, got: %q", res)
	}
	if !strings.Contains(res, "Messages:") {
		t.Fatalf("expected message count in output, got: %q", res)
	}
	if !strings.Contains(res, "Session:") {
		t.Fatalf("expected short session ID in output, got: %q", res)
	}

	// Verify the parent session was NOT modified (fork semantics).
	if len(parent.Messages()) != 2 {
		t.Fatalf("expected parent session to have 2 messages (unchanged), got %d", len(parent.Messages()))
	}
}

func TestSubagentRun_InheritsHistory(t *testing.T) {
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{Role: agent.RoleUser, Content: "First message"})
	parent.Append(agent.Message{Role: agent.RoleAssistant, Content: "First reply"})
	parent.Append(agent.Message{Role: agent.RoleUser, Content: "Second message"})
	parent.Append(agent.Message{Role: agent.RoleAssistant, Content: "Second reply"})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	// Use a spy adapter that records the messages it received so we can
	// verify the child received the full history + task.
	var receivedMsgs []agent.Message
	var callCount int
	spyAdapter := &spyAdapter{
		t: t,
		fn: func(msgs []agent.Message) string {
			callCount++
			// Only capture messages from the first LLM call (the main turn,
			// not the auto-rename call that follows).
			if callCount == 1 {
				receivedMsgs = msgs
			}
			return "Done."
		},
	}

	reg := agent.NewRegistry()
	reg.Register("default", spyAdapter)
	router := agent.NewRouter(reg)

	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	_ = runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "Final task.",
	})

	// Collect all user messages from what the child LLM received.
	var userMessages []string
	for _, m := range receivedMsgs {
		if m.Role == agent.RoleUser {
			userMessages = append(userMessages, m.Content)
		}
	}

	// The child should see the parent's history PLUS the task.
	if len(userMessages) < 3 {
		t.Fatalf("expected at least 3 user messages (2 from parent + task), got %d: %v",
			len(userMessages), userMessages)
	}
	if userMessages[0] != "First message" {
		t.Fatalf("expected first message 'First message', got: %q", userMessages[0])
	}
	if userMessages[1] != "Second message" {
		t.Fatalf("expected second message 'Second message', got: %q", userMessages[1])
	}
	if userMessages[len(userMessages)-1] != "Final task." {
		t.Fatalf("expected last message 'Final task.', got: %q", userMessages[len(userMessages)-1])
	}
}

func TestSubagentRun_MissingTask(t *testing.T) {

	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()

	tool := SubagentRun(router, tools, agent.EngineConfig{}, nil)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{})
	if !strings.Contains(errMsg, "task") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing task, got: %q", errMsg)
	}
}

func TestSubagentRun_NoParentSession(t *testing.T) {
	// Call without a parent session in context — should fail gracefully.

	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()

	tool := SubagentRun(router, tools, agent.EngineConfig{}, nil)

	// Context WITHOUT SessionView.
	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{
		"task": "do something",
	})
	if !strings.Contains(errMsg, "no parent session") {
		t.Fatalf("expected 'no parent session' error, got: %q", errMsg)
	}
}

func TestSubagentRun_RecursionBlocked(t *testing.T) {
	// Verify that the subagent session has subagent_run blocked.
	// We can't inspect the child directly, but we can verify the handler
	// succeeds (the child LLM will never see subagent_run in its schemas,
	// but the handler itself doesn't need it).
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "test")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	adapter := &askAdapter{t: t, response: "done"}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "test recursion prevention",
	})
	if !strings.Contains(res, "done") {
		t.Fatalf("expected subagent to complete successfully, got: %q", res)
	}
}


func TestSubagentRun_WithTools(t *testing.T) {
	// Test that the child subagent can use tools that the parent has enabled.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "test")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")
	parent.EnableTool("echo_tool")

	// The child LLM will first call echo_tool, then return a final response.
	adapter := &askAdapter{t: t, response: "The subagent completed."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	// Create a tool registry that includes echo_tool.
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "echo_tool",
			Description: "Echoes back the input message.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "The message"},
				},
				"required": []string{"message"},
			},
		},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Message string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return "Echo: " + a.Message, nil
		},
		Tags: []string{"utility"},
	})

	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "do something",
	})

	if !strings.Contains(res, "Subagent result:") {
		t.Fatalf("expected 'Subagent result:' header, got: %q", res)
	}
	if !strings.Contains(res, "The subagent completed.") {
		t.Fatalf("expected subagent completion message, got: %q", res)
	}
}

func TestSubagentRun_ForkStripsUnresolvedToolCalls(t *testing.T) {
	// Reproduce the real-world scenario: the parent session has an assistant
	// message at the end with tool_calls whose tool results have not been
	// appended yet (because they're in-flight — e.g. subagent_run itself).
	// Forking must strip those unresolved calls so the child gets a valid
	// message sequence. Meanwhile, earlier resolved tool rounds survive.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	// A completed tool round — resolved tool_calls + matching tool result.
	parent.Append(agent.Message{Role: agent.RoleUser, Content: "What is the weather in Paris?"})
	parent.Append(agent.Message{
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{
			{ID: "call_weather_1", Name: "shell", Arguments: `{"cmd":"curl wttr.in/Paris"}`},
		},
	})
	parent.Append(agent.Message{Role: agent.RoleTool, Content: "Paris: 20°C, Sunny", ToolCallID: "call_weather_1", Name: "shell"})
	parent.Append(agent.Message{Role: agent.RoleAssistant, Content: "The weather in Paris is 20°C and sunny."})
	// An unresolved tool round — this is the in-flight message that called
	// subagent_run (and possibly other tools). Its results haven't been
	// appended yet.
	parent.Append(agent.Message{
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{
			{ID: "call_sub_1", Name: "subagent_run", Arguments: `{"task":"do Tokyo"}`},
			{ID: "call_other_1", Name: "read_file", Arguments: `{"path":"notes.txt"}`},
		},
	})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")
	parent.EnableTool("echo_tool")

	adapter := &askAdapter{t: t, response: "Done."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "Process this data.",
	})
	if !strings.Contains(res, "Subagent result:") {
		t.Fatalf("expected successful subagent result, got: %q", res)
	}
	if !strings.Contains(res, "Done.") {
		t.Fatalf("expected subagent reply 'Done.', got: %q", res)
	}
}

func TestForkMessages_StripsUnresolvedToolCalls(t *testing.T) {
	// Unit test for forkMessages directly.
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "shell", Arguments: `{"cmd":"echo hi"}`},
		}},
		{Role: agent.RoleTool, Content: "hi", ToolCallID: "call_1", Name: "shell"},
		{Role: agent.RoleAssistant, Content: "done"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "call_2", Name: "subagent_run", Arguments: `{"task":"x"}`},
			{ID: "call_3", Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	}

	forked := forkMessages(msgs)
	// Should have 4 messages: user, assistant(call_1), tool(call_1), assistant("done")
	// The last assistant with unresolved calls should be stripped (empty content + all calls unresolved).
	if len(forked) != 4 {
		t.Fatalf("expected 4 messages after stripping unresolved calls, got %d: %+v", len(forked), forked)
	}
	// The resolved tool calls (call_1) should survive.
	if len(forked[1].ToolCalls) != 1 || forked[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected preserved resolved tool call call_1, got: %+v", forked[1].ToolCalls)
	}
}

func TestForkMessages_PreservesResolvedToolCalls(t *testing.T) {
	// When all tool calls are resolved, no messages should be removed.
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "shell", Arguments: `{"cmd":"echo hi"}`},
		}},
		{Role: agent.RoleTool, Content: "hi", ToolCallID: "call_1", Name: "shell"},
		{Role: agent.RoleAssistant, Content: "done"},
	}

	forked := forkMessages(msgs)
	if len(forked) != 4 {
		t.Fatalf("expected 4 messages preserved, got %d: %+v", len(forked), forked)
	}
}

func TestForkMessages_PreservesTextContentWhenStripping(t *testing.T) {
	// If the last assistant message has both text content and unresolved
	// tool calls, the text should survive.
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleAssistant, Content: "I'm thinking...", ToolCalls: []agent.ToolCall{
			{ID: "call_1", Name: "subagent_run", Arguments: `{"task":"x"}`},
		}},
	}

	forked := forkMessages(msgs)
	if len(forked) != 2 {
		t.Fatalf("expected 2 messages (text content should survive), got %d: %+v", len(forked), forked)
	}
	if forked[1].Content != "I'm thinking..." {
		t.Fatalf("expected text content 'I'm thinking...', got: %q", forked[1].Content)
	}
	if len(forked[1].ToolCalls) != 0 {
		t.Fatalf("expected tool calls to be stripped, got: %+v", forked[1].ToolCalls)
	}
}

// ---------------------------------------------------------------------------
// spyAdapter records messages and delegates response to a callback
// ---------------------------------------------------------------------------

type spyAdapter struct {
	t  *testing.T
	fn func(msgs []agent.Message) string
}

func (s *spyAdapter) Complete(_ context.Context, msgs []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (*agent.LLMResponse, error) {
	reply := s.fn(msgs)
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: reply},
		FinishReason: "stop",
		Usage:        &agent.Usage{PromptTokens: len(msgs), CompletionTokens: len(reply), TotalTokens: len(msgs) + len(reply)},
	}, nil
}

func (s *spyAdapter) Stream(_ context.Context, _ []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions) (<-chan agent.StreamResult, error) {
	ch := make(chan agent.StreamResult)
	close(ch)
	return ch, nil
}

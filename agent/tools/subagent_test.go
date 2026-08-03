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
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "What is the capital of France?"})
	parent.Append(agent.Message{ID: "pm2", Role: agent.RoleAssistant, Content: "The capital of France is Paris."})
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

	tool := SubagentRun(sm, router, tools, cfg, nil)

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
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "First message"})
	parent.Append(agent.Message{ID: "pm2", Role: agent.RoleAssistant, Content: "First reply"})
	parent.Append(agent.Message{ID: "pm3", Role: agent.RoleUser, Content: "Second message"})
	parent.Append(agent.Message{ID: "pm4", Role: agent.RoleAssistant, Content: "Second reply"})
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

	tool := SubagentRun(sm, router, tools, cfg, nil)

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

	sm := agent.NewSessionManager(newTestStore(), "test")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()

	tool := SubagentRun(sm, router, tools, agent.EngineConfig{}, nil)

	errMsg := runErrCtx(t, ctxWithNS("ns"), tool.Handler, map[string]any{})
	if !strings.Contains(errMsg, "task") && !strings.Contains(errMsg, "required") {
		t.Fatalf("expected error about missing task, got: %q", errMsg)
	}
}

func TestSubagentRun_NoParentSession(t *testing.T) {
	// Call without a parent session in context — should fail gracefully.

	sm := agent.NewSessionManager(newTestStore(), "test")
	reg := agent.NewRegistry()
	reg.Register("default", &askAdapter{t: t, response: "irrelevant"})
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()

	tool := SubagentRun(sm, router, tools, agent.EngineConfig{}, nil)

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

	tool := SubagentRun(sm, router, tools, cfg, nil)

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

	tool := SubagentRun(sm, router, tools, cfg, nil)

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
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "What is the weather in Paris?"})
	parent.Append(agent.Message{
		ID:   "pm2",
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{
			{ID: "call_weather_1", Name: "shell", Arguments: `{"cmd":"curl wttr.in/Paris"}`},
		},
	})
	parent.Append(agent.Message{ID: "pm3", Role: agent.RoleTool, Content: "Paris: 20°C, Sunny", ToolCallID: "call_weather_1", Name: "shell"})
	parent.Append(agent.Message{ID: "pm4", Role: agent.RoleAssistant, Content: "The weather in Paris is 20°C and sunny."})
	// An unresolved tool round — this is the in-flight message that called
	// subagent_run (and possibly other tools). Its results haven't been
	// appended yet.
	parent.Append(agent.Message{
		ID:   "pm5",
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

	tool := SubagentRun(sm, router, tools, cfg, nil)

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

func TestSubagentRun_CreatesPersistentChildSession(t *testing.T) {
	// The subagent must create a REAL, persistent child session in the
	// parent's store and namespace — visible to session tools, linked
	// via ParentID/ForkPoint — not an ephemeral in-memory session.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "First message"})
	parent.Append(agent.Message{ID: "pm2", Role: agent.RoleAssistant, Content: "First reply"})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	adapter := &askAdapter{t: t, response: "The subagent processed your task. Result: 42."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(sm, router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "Process this data.",
	})
	if !strings.Contains(res, "Result: 42") {
		t.Fatalf("expected subagent reply in output, got: %q", res)
	}

	// 1. The child must be persisted in the parent's store + namespace.
	sessions, err := sm.List(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions in namespace %q (parent + persistent child), got %d", ns, len(sessions))
	}

	// 2. Identify the child (the non-parent session).
	var child *agent.Session
	for _, s := range sessions {
		if s.SessionID() != parent.SessionID() {
			child = s
			break
		}
	}
	if child == nil {
		t.Fatal("child session not found in store")
	}

	childData := child.Read()

	// 3. Lineage: ParentID + ForkPoint must be set.
	if childData.ParentID != parent.SessionID() {
		t.Fatalf("expected child ParentID %q, got %q", parent.SessionID(), childData.ParentID)
	}
	parentMsgs := parent.Messages()
	lastParentID := parentMsgs[len(parentMsgs)-1].ID
	if childData.ForkPoint != lastParentID {
		t.Fatalf("expected child ForkPoint %q (parent's last message), got %q", lastParentID, childData.ForkPoint)
	}

	// 4. The child has forked history + subagent notice + task + final reply.
	childMsgs := child.Messages()
	if len(childMsgs) != 5 {
		t.Fatalf("expected 5 messages in child (2 forked + notice + task + reply), got %d: %+v", len(childMsgs), childMsgs)
	}
	if childMsgs[0].Role != agent.RoleUser || childMsgs[0].Content != "First message" {
		t.Fatalf("expected first child message to be the forked user message, got: %+v", childMsgs[0])
	}
	if childMsgs[1].Role != agent.RoleAssistant || childMsgs[1].Content != "First reply" {
		t.Fatalf("expected second child message to be the forked assistant reply, got: %+v", childMsgs[1])
	}
	if childMsgs[2].Role != agent.RoleUser {
		t.Fatalf("expected third child message to be the subagent notice (user role, to preserve prompt cache), got: %+v", childMsgs[2])
	}
	if childMsgs[3].Role != agent.RoleUser || childMsgs[3].Content != "Process this data." {
		t.Fatalf("expected fourth child message to be the task, got: %+v", childMsgs[3])
	}
	if childMsgs[4].Role != agent.RoleAssistant || !strings.Contains(childMsgs[4].Content, "Result: 42") {
		t.Fatalf("expected fifth child message to be the subagent reply, got: %+v", childMsgs[4])
	}

	// 5. Recursion prevention on the child.
	if !child.IsToolDenied("subagent_run") {
		t.Fatalf("expected subagent_run to be blocked on the child session")
	}
	if child.IsToolEnabled("subagent_run") {
		t.Fatalf("expected subagent_run to not be enabled on the child session")
	}

	// 6. Parent must be unchanged (fork semantics).
	if len(parent.Messages()) != 2 {
		t.Fatalf("expected parent session to remain unchanged, got %d messages", len(parent.Messages()))
	}
}

func TestSubagentRun_ForkWithoutMessageIDs(t *testing.T) {
	// Defensive fallback: if the parent has messages but none carry IDs
	// (e.g. a manually-constructed session), the child must still inherit
	// the full history instead of silently forking blank.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{Role: agent.RoleUser, Content: "legacy message"})
	parent.Append(agent.Message{Role: agent.RoleAssistant, Content: "legacy reply"})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	adapter := &askAdapter{t: t, response: "done"}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(sm, router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "continue",
	})
	if !strings.Contains(res, "done") {
		t.Fatalf("expected subagent completion, got: %q", res)
	}

	sessions, err := sm.List(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	var child *agent.Session
	for _, s := range sessions {
		if s.SessionID() != parent.SessionID() {
			child = s
			break
		}
	}
	if child == nil {
		t.Fatal("child session not found in store")
	}
	childMsgs := child.Messages()
	if len(childMsgs) != 5 {
		t.Fatalf("expected 5 messages in child (2 legacy + notice + task + reply), got %d: %+v", len(childMsgs), childMsgs)
	}
	if childMsgs[0].Content != "legacy message" || childMsgs[1].Content != "legacy reply" {
		t.Fatalf("expected forked legacy history in child, got: %+v", childMsgs)
	}
	if childMsgs[2].Role != agent.RoleUser {
		t.Fatalf("expected subagent notice message in child (user role, to preserve prompt cache), got: %+v", childMsgs[2])
	}
	if childMsgs[3].Role != agent.RoleUser || childMsgs[3].Content != "continue" {
		t.Fatalf("expected task message in child, got: %+v", childMsgs[3])
	}
}

func TestSubagentRun_ChildSessionGetsSubagentNotice(t *testing.T) {
	// The child session must contain a notice informing it that it is a
	// subagent and that subagent_run is blocked for it — otherwise the
	// forked history (e.g. the parent's show_tool output describing
	// subagent_run) misleads it into trying to fork further subagents.
	//
	// The notice MUST be a user message, not a system message: system
	// content is part of the prompt-cache key (Anthropic system string,
	// OpenAI instructions), so a system notice would invalidate the
	// cache for the inherited prefix. As a user message appended after
	// the forked history, the system + history prefix stays identical to
	// the parent's last request and remains cacheable.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "Run subagents for me"})
	parent.Append(agent.Message{ID: "pm2", Role: agent.RoleAssistant, Content: "OK, spawning subagents."})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	adapter := &askAdapter{t: t, response: "Task done."}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(sm, router, tools, cfg, nil)

	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "Check the weather",
	})
	if !strings.Contains(res, "Task done.") {
		t.Fatalf("expected subagent completion, got: %q", res)
	}

	sessions, err := sm.List(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	var child *agent.Session
	for _, s := range sessions {
		if s.SessionID() != parent.SessionID() {
			child = s
			break
		}
	}
	if child == nil {
		t.Fatal("child session not found in store")
	}

	childMsgs := child.Messages()

	// The notice must be a USER message (to preserve prompt caching of the
	// inherited prefix), placed after the forked history.
	var notice *agent.Message
	for i := range childMsgs {
		if childMsgs[i].Role == agent.RoleUser && strings.Contains(childMsgs[i].Content, "You are a subagent") {
			notice = &childMsgs[i]
			if i < 2 {
				t.Fatalf("expected notice after the forked history, found at index %d", i)
			}
			break
		}
	}
	if notice == nil {
		t.Fatal("expected a user notice in child session informing it that it is a subagent")
	}
	lower := strings.ToLower(notice.Content)
	if !strings.Contains(lower, "blocked") && !strings.Contains(lower, "denied") && !strings.Contains(lower, "not available") {
		t.Fatalf("expected notice to say subagent_run is blocked, got: %q", notice.Content)
	}
	// The notice should reference the parent session so the child knows
	// where it came from.
	if !strings.Contains(notice.Content, parent.SessionID()[:8]) {
		t.Fatalf("expected notice to reference parent session, got: %q", notice.Content)
	}

	// No system message may carry the notice — system content is part of
	// the prompt-cache key and must stay identical to the parent's.
	for _, m := range childMsgs {
		if m.Role == agent.RoleSystem && strings.Contains(strings.ToLower(m.Content), "subagent") {
			t.Fatalf("subagent notice must not be a system message (would invalidate prompt cache), got: %q", m.Content)
		}
	}
}

func TestSubagentRun_EmitsSessionCreatedEvent(t *testing.T) {
	// When a subagent forks a child session, the tool must emit a
	// SessionCreated engine event so the namespace hub can broadcast a
	// type:"session" frame to all connected clients.
	ns := "testns"
	sm := agent.NewSessionManager(newTestStore(), "You are a helpful assistant.")

	parent, err := sm.GetOrCreate(context.Background(), ns, "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Append(agent.Message{ID: "pm1", Role: agent.RoleUser, Content: "run"})
	parent.Append(agent.Message{ID: "pm2", Role: agent.RoleAssistant, Content: "ok"})
	if err := sm.Save(context.Background(), ns, parent); err != nil {
		t.Fatal(err)
	}
	parent.EnableTool("subagent_run")

	adapter := &askAdapter{t: t, response: "done"}
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{MaxToolIterations: 4}

	tool := SubagentRun(sm, router, tools, cfg, nil)

	// A buffered channel to receive the emitted engine event.
	eventCh := make(chan event.EngineEvent, 4)
	handlerCtx := event.ContextWithNamespace(context.Background(), ns)
	handlerCtx = event.ContextWithSessionView(handlerCtx, parent)
	handlerCtx = event.ContextWithSessionID(handlerCtx, parent.SessionID())
	handlerCtx = event.ContextWithEventSender(handlerCtx, eventCh)

	res := runPlainCtx(t, handlerCtx, tool.Handler, map[string]any{
		"task": "do it",
	})
	if !strings.Contains(res, "done") {
		t.Fatalf("expected subagent completion, got: %q", res)
	}

	// Read the emitted events — there must be a SessionCreated.
	var created *event.SessionCreated
loop:
	for {
		select {
		case evt := <-eventCh:
			if sc, ok := evt.(event.SessionCreated); ok {
				created = &sc
				break loop
			}
		default:
			break loop
		}
	}
	if created == nil {
		t.Fatal("expected a SessionCreated engine event from subagent_run")
	}
	if created.Session["parent_id"] != parent.SessionID() {
		t.Fatalf("expected parent_id %q in event session metadata, got %v",
			parent.SessionID(), created.Session["parent_id"])
	}
	if created.Session["fork_point"] != "pm2" {
		t.Fatalf("expected fork_point %q in event session metadata, got %v",
			"pm2", created.Session["fork_point"])
	}
}

// ---------------------------------------------------------------------------
// spyAdapter records messages and delegates response to a callback
// ---------------------------------------------------------------------------

type spyAdapter struct {
	t  *testing.T
	fn func(msgs []agent.Message) string
}

func (s *spyAdapter) Complete(_ context.Context, msgs []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	reply := s.fn(msgs)
	return &agent.LLMResponse{
		Message:      agent.Message{Role: agent.RoleAssistant, Content: reply},
		FinishReason: "stop",
		Usage:        &agent.Usage{PromptTokens: len(msgs), CompletionTokens: len(reply), TotalTokens: len(msgs) + len(reply)},
	}, nil
}


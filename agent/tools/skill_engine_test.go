package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// scriptedAdapter returns canned responses, one per call, and records the
// messages passed to each call.
type scriptedAdapter struct {
	responses []agent.LLMResponse
	calls     int
	lastMsgs  [][]agent.Message
}

func (a *scriptedAdapter) Complete(_ context.Context, msgs []agent.Message, _ []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	a.lastMsgs = append(a.lastMsgs, msgs)
	if a.calls >= len(a.responses) {
		return nil, fmt.Errorf("scriptedAdapter: out of responses")
	}
	r := a.responses[a.calls]
	a.calls++
	return &r, nil
}

// TestEngine_LoadSkill_ContentInjectedInSameTurn verifies the full engine
// loop: the LLM calls load_skill, and the NEXT LLM call within the same
// turn receives the skill's content as system context.
func TestEngine_LoadSkill_ContentInjectedInSameTurn(t *testing.T) {
	adapter := &scriptedAdapter{responses: []agent.LLMResponse{
		{
			Message: agent.Message{
				Role: agent.RoleAssistant,
				ToolCalls: []agent.ToolCall{{
					ID: "c1", Name: "load_skill", Arguments: `{"name":"go-testing"}`,
				}},
			},
			FinishReason: "tool_calls",
		},
		{Message: agent.Message{Role: agent.RoleAssistant, Content: "done"}, FinishReason: "stop"},
	}}

	tools := agent.NewToolRegistry()
	tools.Register(LoadSkill())
	tools.Register(SearchSkills())

	sm := agent.NewSessionManager(agent.NewMemoryStore(), "sys")
	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)
	conv := agent.NewConversation(sm, router, tools, "testns", agent.EngineConfig{MaxToolIterations: 4})

	// Session whose cwd has a skills/ registry with go-testing.
	session := newTestSkillSession(t)
	session.EnableTool(context.Background(), "load_skill")
	if err := sm.Store.Put(context.Background(), "testns", session); err != nil {
		t.Fatal(err)
	}

	eventCh, err := conv.Execute(context.Background(), session.SessionID(), "load the go-testing skill")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for evt := range eventCh {
		if tf, ok := evt.(event.TurnFinished); ok && tf.Err != nil {
			t.Fatalf("turn error: %v", tf.Err)
		}
	}

	if adapter.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", adapter.calls)
	}

	// The second call's context must contain the loaded skill content.
	var found bool
	for _, m := range adapter.lastMsgs[1] {
		if m.Role == agent.RoleSystem && strings.Contains(m.Content, "## Skill: go-testing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skill content in second LLM call, got %+v", adapter.lastMsgs[1])
	}
}

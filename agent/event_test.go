package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

func TestExecuteEvents_NoTools(t *testing.T) {
	conv, _, adapter, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "hello back"}, FinishReason: "stop"},
	})

	eventCh, err := conv.Execute(context.Background(), "", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []event.EngineEvent
	for e := range eventCh {
		events = append(events, e)
	}

	// Expected: ContextEstimated (before LLM call) + TurnFinished
	// No UsageReported because Usage is nil
	if len(events) != 2 {
		t.Fatalf("expected 2 events (ContextEstimated + TurnFinished), got %d", len(events))
	}

	_, ok := events[0].(event.ContextEstimated)
	if !ok {
		t.Fatalf("expected ContextEstimated, got %T", events[0])
	}

	turnEv, ok := events[1].(event.TurnFinished)
	if !ok {
		t.Fatalf("expected last event to be event.TurnFinished, got %T", events[1])
	}
	if turnEv.Reply != "hello back" {
		t.Fatalf("unexpected turn reply: %q", turnEv.Reply)
	}
	if turnEv.Err != nil {
		t.Fatalf("unexpected error: %v", turnEv.Err)
	}

	if adapter.calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", adapter.calls)
	}
}

func TestExecuteEvents_ToolLoop(t *testing.T) {
	conv, _, adapter, tools := newTestComponents(t, []LLMResponse{
		{
			Message: Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID:        "call-1",
					Name:      "greet",
					Arguments: `{"name":"world"}`,
				}},
			},
			FinishReason: "tool_calls",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
		{
			Message:      Message{Role: RoleAssistant, Content: "Hello, world!"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		},
	})

	tools.Register(Tool{
		Schema: ToolSchema{Name: "greet"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			return `{"greeting":"Hello, world!"}`, nil
		},
		ExecSnippet: func(args json.RawMessage) string {
			return `name="world"`
		},
	})

	// Pre-create session and enable the "greet" tool
	session, err := conv.sessions.GetOrCreate(context.Background(), "testns", "")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	session.EnableTool("greet")
	if err := conv.sessions.Save(context.Background(), "testns", session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	eventCh, err := conv.Execute(context.Background(), session.SessionID(), "say hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []event.EngineEvent
	for e := range eventCh {
		events = append(events, e)
	}

	// Expected: ContextEstimated, UsageReported (tool call), event.ToolCallStarted,
	// event.ToolCallFinished, ContextEstimated, UsageReported (final), event.TurnFinished
	if len(events) != 7 {
		t.Fatalf("expected 7 events, got %d: %+v", len(events), events)
	}

	// First event: context estimated
	_, ok := events[0].(event.ContextEstimated)
	if !ok {
		t.Fatalf("expected ContextEstimated, got %T", events[0])
	}

	// Second event: usage report for tool call (iteration 1)
	_, ok = events[1].(event.UsageReported)
	if !ok {
		t.Fatalf("expected UsageReported, got %T", events[1])
	}

	// Third event: tool started
	startEv, ok := events[2].(event.ToolCallStarted)
	if !ok {
		t.Fatalf("expected event.ToolCallStarted, got %T", events[2])
	}
	if startEv.Name != "greet" {
		t.Fatalf("expected tool 'greet', got %q", startEv.Name)
	}
	if startEv.ExecSnippet != `name="world"` {
		t.Fatalf("expected snippet 'name=\"world\"', got %q", startEv.ExecSnippet)
	}

	// Fourth event: tool finished
	finishEv, ok := events[3].(event.ToolCallFinished)
	if !ok {
		t.Fatalf("expected event.ToolCallFinished, got %T", events[3])
	}
	if finishEv.Result.IsError() {
		t.Fatalf("unexpected tool error: %v", finishEv.Result.Err)
	}
	if !strings.Contains(finishEv.Result.Output, "Hello, world!") {
		t.Fatalf("unexpected tool result: %q", finishEv.Result.Output)
	}

	// Fifth event: usage report for final LLM response
	_, ok = events[5].(event.UsageReported)
	if !ok {
		t.Fatalf("expected UsageReported, got %T", events[5])
	}

	// Sixth event: turn finished
	turnEv, ok := events[6].(event.TurnFinished)
	if !ok {
		t.Fatalf("expected event.TurnFinished, got %T", events[6])
	}
	if turnEv.Reply != "Hello, world!" {
		t.Fatalf("expected 'Hello, world!', got %q", turnEv.Reply)
	}
	if turnEv.Err != nil {
		t.Fatalf("unexpected error: %v", turnEv.Err)
	}

	if adapter.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", adapter.calls)
	}
}

func TestExecuteEvents_MaxIterations(t *testing.T) {
	loop := LLMResponse{
		Message: Message{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID: "x", Name: "noop2", Arguments: `{}`,
			}},
		},
	}
	conv, _, _, tools := newTestComponents(t, []LLMResponse{loop, loop, loop, loop, loop})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "noop2"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "{}", nil
		},
	})

	eventCh, err := conv.Execute(context.Background(), "", "go")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var lastEvent event.EngineEvent
	for e := range eventCh {
		lastEvent = e
	}

	turnEv, ok := lastEvent.(event.TurnFinished)
	if !ok {
		t.Fatalf("expected last event to be event.TurnFinished, got %T", lastEvent)
	}
	if !errors.Is(turnEv.Err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", turnEv.Err)
	}
}

func TestExecuteEvents_ErrorResult(t *testing.T) {
	conv, _, _, tools := newTestComponents(t, []LLMResponse{
		{
			Message: Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID: "call-err", Name: "failing", Arguments: `{}`,
				}},
			},
			FinishReason: "tool_calls",
		},
		{
			Message:      Message{Role: RoleAssistant, Content: "recovered"},
			FinishReason: "stop",
		},
	})

	tools.Register(Tool{
		Schema: ToolSchema{Name: "failing"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", nil
		},
	})

	eventCh, err := conv.Execute(context.Background(), "", "run")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []event.EngineEvent
	for e := range eventCh {
		events = append(events, e)
	}

	_ = events
}

func TestExecuteEvents_StreamSession(t *testing.T) {
	_, streamer, _, _ := newTestComponents(t, []LLMResponse{
		{Message: Message{Role: RoleAssistant, Content: "streamed hello"}},
	})

	eventCh, err := streamer.Execute(context.Background(), "", "hi")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []event.EngineEvent
	for e := range eventCh {
		events = append(events, e)
	}

	// Expected: ContextEstimated, then event.TextDelta (one or two), event.TurnFinished
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	// First event should be ContextEstimated
	_, ok := events[0].(event.ContextEstimated)
	if !ok {
		t.Fatalf("expected ContextEstimated, got %T", events[0])
	}

	// The fake adapter sends content in two chunks (mid split), so we may have
	// two event.TextDelta events
	var gotText string
	for _, e := range events[1:len(events)-1] {
		deltaEv, ok := e.(event.TextDelta)
		if !ok {
			t.Fatalf("expected event.TextDelta, got %T", e)
		}
		gotText += deltaEv.Delta
	}

	if gotText != "streamed hello" {
		t.Fatalf("expected 'streamed hello', got %q", gotText)
	}

	turnEv, ok := events[len(events)-1].(event.TurnFinished)
	if !ok {
		t.Fatalf("expected last event to be event.TurnFinished, got %T", events[len(events)-1])
	}
	if turnEv.Err != nil {
		t.Fatalf("unexpected error: %v", turnEv.Err)
	}
}

// TestTurnFinishedCarriesSessionStats verifies that TurnFinished events
// carry consolidated session statistics (TotalCost, MessageCount,
// EstimatedContextTokens, Model) for client-side display.
func TestTurnFinishedCarriesSessionStats(t *testing.T) {
	conv, _, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message:      Message{Role: RoleAssistant, Content: "stats check"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Cost: 0.0001},
		},
	})

	eventCh, err := conv.Execute(context.Background(), "", "hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var turnFinished *event.TurnFinished
	for e := range eventCh {
		if tf, ok := e.(event.TurnFinished); ok {
			turnFinished = &tf
		}
	}

	if turnFinished == nil {
		t.Fatal("expected TurnFinished event")
	}

	// TotalTokens should be accumulated
	if turnFinished.TotalTokens <= 0 {
		t.Fatalf("expected TotalTokens > 0, got %d", turnFinished.TotalTokens)
	}

	// TotalCost should be > 0 since we provided Cost in Usage
	if turnFinished.TotalCost <= 0 {
		t.Fatalf("expected TotalCost > 0, got %f", turnFinished.TotalCost)
	}

	// MessageCount: 1 user message + 1 assistant reply = 2
	if turnFinished.MessageCount != 2 {
		t.Fatalf("expected MessageCount=2, got %d", turnFinished.MessageCount)
	}

	// EstimatedContextTokens should be set by the engine before the LLM call
	if turnFinished.EstimatedContextTokens <= 0 {
		t.Fatalf("expected EstimatedContextTokens > 0, got %d", turnFinished.EstimatedContextTokens)
	}

	// Model should be non-empty (set by EnsureDefaultModel)
	if turnFinished.Model == "" {
		t.Fatal("expected Model to be non-empty")
	}

	if turnFinished.Err != nil {
		t.Fatalf("unexpected error: %v", turnFinished.Err)
	}
	if turnFinished.Reply != "stats check" {
		t.Fatalf("expected reply 'stats check', got %q", turnFinished.Reply)
	}
}

package agent

import (
	"context"
	"testing"
)

// TestTurnRunnerPreservesProviderMetadata verifies that when an adapter
// returns a message with ProviderMetadata (e.g. gemini_signatures or
// reasoning_content), the metadata is persisted on the session's assistant
// message. This is critical for provider-specific round-trip data like
// Gemini's thought signatures — if dropped, the next API call fails.
func TestTurnRunnerPreservesProviderMetadata(t *testing.T) {
	meta := map[string]any{
		"gemini_signatures": []string{"sig-abc", "sig-def"},
	}

	conv, _, _ := newTestComponents(t, []LLMResponse{
		{
			Message: Message{
				Role:             RoleAssistant,
				Content:          "calling tools",
				ToolCalls:        []ToolCall{{ID: "call_1", Name: "search", Arguments: `{"q":"test"}`}},
				ProviderMetadata: meta,
			},
			FinishReason: "tool_calls",
			Usage:        &Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		},
		{
			Message:      Message{Role: RoleAssistant, Content: "final reply"},
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		},
	})

	session, _, err := executeSync(conv, context.Background(), "meta-test", "hello")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var found bool
	for _, m := range session.Messages() {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			found = true
			sigs, ok := m.ProviderMetadata["gemini_signatures"].([]string)
			if !ok {
				var ifaces []interface{}
				ifaces, ok = m.ProviderMetadata["gemini_signatures"].([]interface{})
				if ok {
					t.Fatalf("signatures deserialized as []interface{} (JSON round-trip), got %v", ifaces)
				}
				t.Fatalf("BUG: gemini_signatures missing from ProviderMetadata: %+v", m.ProviderMetadata)
			}
			if len(sigs) != 2 || sigs[0] != "sig-abc" || sigs[1] != "sig-def" {
				t.Fatalf("signatures: %+v (expected [sig-abc, sig-def])", sigs)
			}
			break
		}
	}
	if !found {
		t.Fatal("assistant tool-call message not found in session")
	}
}

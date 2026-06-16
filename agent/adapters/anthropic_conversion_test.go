package adapters

import (
	"encoding/json"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// Contract tests for toAnthropic() — converts hakka internal message history
// to Anthropic Messages API format.
//
// The Anthropic API requires alternating user ↔ assistant roles. Consecutive
// messages of the same role must be merged into a single message with multiple
// content blocks.
// ---------------------------------------------------------------------------

func TestToAnthropicConsecutiveToolResultsMerged(t *testing.T) {
	// Simulate: assistant issues two tool calls, both return results.
	// Two consecutive RoleTool messages must merge into one user message
	// with two tool_result blocks.
	history := []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "toolu_1", Name: "get_weather", Arguments: `{"city":"Paris"}`},
			{ID: "toolu_2", Name: "get_time", Arguments: `{"tz":"UTC"}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "toolu_1", Content: `{"temp":22}`},
		{Role: agent.RoleTool, ToolCallID: "toolu_2", Content: `{"time":"12:00"}`},
	}

	_, msgs := toAnthropic(history)

	// Must produce exactly 2 messages: assistant + user (with merged results)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (assistant + user with 2 tool_result blocks), got %d", len(msgs))
	}

	// Message 0: assistant with two tool_use blocks
	if msgs[0].Role != "assistant" {
		t.Errorf("msg[0] role = %q, want %q", msgs[0].Role, "assistant")
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("msg[0] (assistant) expected 2 content blocks, got %d", len(msgs[0].Content))
	}
	for i, name := range []string{"get_weather", "get_time"} {
		block := msgs[0].Content[i]
		if block.Type != "tool_use" {
			t.Errorf("msg[0].content[%d].type = %q, want %q", i, block.Type, "tool_use")
		}
		if block.Name != name {
			t.Errorf("msg[0].content[%d].name = %q, want %q", i, block.Name, name)
		}
	}

	// Message 1: user with two tool_result blocks
	if msgs[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want %q", msgs[1].Role, "user")
	}
	if len(msgs[1].Content) != 2 {
		t.Fatalf("msg[1] (user) expected 2 content blocks, got %d", len(msgs[1].Content))
	}
	for i, expectedID := range []string{"toolu_1", "toolu_2"} {
		block := msgs[1].Content[i]
		if block.Type != "tool_result" {
			t.Errorf("msg[1].content[%d].type = %q, want %q", i, block.Type, "tool_result")
		}
		if block.ToolUseID != expectedID {
			t.Errorf("msg[1].content[%d].tool_use_id = %q, want %q", i, block.ToolUseID, expectedID)
		}
	}
}

func TestToAnthropicToolResultAfterUserText(t *testing.T) {
	// A plain user text message followed by a tool result should produce
	// TWO separate user messages — they have different block types and
	// merging them would violate the Anthropic protocol (user can't have
	// both text and tool_result in the same message in this order).
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "what's the weather?"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "toolu_1", Name: "get_weather", Arguments: `{}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "toolu_1", Content: `{"temp":22}`},
	}

	_, msgs := toAnthropic(history)

	// Expected structure:
	//   0: user (text)
	//   1: assistant (tool_use)
	//   2: user (tool_result)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user-text, assistant, user-tool_result), got %d", len(msgs))
	}

	if msgs[0].Role != "user" || msgs[0].Content[0].Type != "text" {
		t.Error("msg[0] should be user with text block")
	}
	if msgs[2].Role != "user" || msgs[2].Content[0].Type != "tool_result" {
		t.Error("msg[2] should be user with tool_result block")
	}
}

func TestToAnthropicConsecutiveUserTextMerged(t *testing.T) {
	// Two consecutive user messages with text should merge into a single
	// user message with two text blocks.
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleUser, Content: "world"},
	}

	_, msgs := toAnthropic(history)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want %q", msgs[0].Role, "user")
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 text blocks in merged message, got %d", len(msgs[0].Content))
	}
	for i, expected := range []string{"hello", "world"} {
		if msgs[0].Content[i].Type != "text" {
			t.Errorf("content[%d].type = %q, want %q", i, msgs[0].Content[i].Type, "text")
		}
		if msgs[0].Content[i].Text != expected {
			t.Errorf("content[%d].text = %q, want %q", i, msgs[0].Content[i].Text, expected)
		}
	}
}

func TestToAnthropicUserTextMergeDoesNotAffectToolResult(t *testing.T) {
	// Verify that text-only user messages merge correctly,
	// but tool results still create separate user messages after
	// an assistant turn (ensuring no cross-contamination).
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "first"},
		{Role: agent.RoleUser, Content: "second"},
		{Role: agent.RoleAssistant, Content: "ok", ToolCalls: []agent.ToolCall{
			{ID: "toolu_x", Name: "x", Arguments: "{}"},
		}},
		{Role: agent.RoleTool, ToolCallID: "toolu_x", Content: "done"},
	}

	_, msgs := toAnthropic(history)

	// Expected: user(merged,2 blocks) → assistant(1 tool_use) → user(1 tool_result)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || len(msgs[0].Content) != 2 {
		t.Errorf("msg[0] should be merged user with 2 text blocks")
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].role = %q, want %q", msgs[1].Role, "assistant")
	}
	if len(msgs[1].Content) != 2 {
		t.Fatalf("msg[1] (assistant) expected 2 blocks (text + tool_use), got %d", len(msgs[1].Content))
	}
	if msgs[1].Content[0].Type != "text" || msgs[1].Content[0].Text != "ok" {
		t.Errorf("msg[1].content[0] should be text block with 'ok'")
	}
	if msgs[1].Content[1].Type != "tool_use" || msgs[1].Content[1].Name != "x" {
		t.Errorf("msg[1].content[1] should be tool_use block named 'x'")
	}
	if msgs[2].Role != "user" || msgs[2].Content[0].Type != "tool_result" {
		t.Errorf("msg[2] should be user with tool_result block")
	}
}

func TestToAnthropicConsecutiveAssistantMerged(t *testing.T) {
	// Two consecutive assistant messages (e.g. text + tool calls) should
	// merge into a single assistant message with combined content blocks.
	history := []agent.Message{
		{Role: agent.RoleAssistant, Content: "let me check"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "toolu_a", Name: "search", Arguments: `{"q":"test"}`},
		}},
	}

	_, msgs := toAnthropic(history)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged assistant message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", msgs[0].Role, "assistant")
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d", len(msgs[0].Content))
	}
	if msgs[0].Content[0].Type != "text" || msgs[0].Content[0].Text != "let me check" {
		t.Error("first block should be text")
	}
	if msgs[0].Content[1].Type != "tool_use" || msgs[0].Content[1].Name != "search" {
		t.Error("second block should be tool_use")
	}
}

func TestToAnthropicAlternatingRoles(t *testing.T) {
	// A full alternating conversation should be preserved as-is (no merging needed).
	history := []agent.Message{
		{Role: agent.RoleSystem, Content: "you are a bot"},
		{Role: agent.RoleUser, Content: "hi"},
		{Role: agent.RoleAssistant, Content: "hello"},
		{Role: agent.RoleUser, Content: "how are you?"},
		{Role: agent.RoleAssistant, Content: "i'm fine"},
	}

	system, msgs := toAnthropic(history)

	if system != "you are a bot" {
		t.Errorf("system = %q, want %q", system, "you are a bot")
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	expectedRoles := []string{"user", "assistant", "user", "assistant"}
	for i, role := range expectedRoles {
		if msgs[i].Role != role {
			t.Errorf("msg[%d].role = %q, want %q", i, msgs[i].Role, role)
		}
	}
}

func TestToAnthropicEmptyHistory(t *testing.T) {
	system, msgs := toAnthropic(nil)
	if system != "" {
		t.Errorf("system = %q, want empty", system)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestToAnthropicMultipleSystemMessagesMerged(t *testing.T) {
	history := []agent.Message{
		{Role: agent.RoleSystem, Content: "first rule"},
		{Role: agent.RoleSystem, Content: "second rule"},
	}
	system, msgs := toAnthropic(history)
	if system != "first rule\n\nsecond rule" {
		t.Errorf("system = %q, want %q", system, "first rule\n\nsecond rule")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (only system), got %d", len(msgs))
	}
}

func TestToAnthropicToolResultPreservesToolUseIDAndContent(t *testing.T) {
	// Verify that tool_result blocks carry the correct tool_use_id and content text.
	history := []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "toolu_abc", Name: "add", Arguments: `{"a":1,"b":2}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "toolu_abc", Content: `{"sum":3}`},
	}

	_, msgs := toAnthropic(history)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	// The tool_result block
	block := msgs[1].Content[0]
	if block.ToolUseID != "toolu_abc" {
		t.Errorf("tool_use_id = %q, want %q", block.ToolUseID, "toolu_abc")
	}
	if block.Content != `{"sum":3}` {
		t.Errorf("content = %q, want %q", block.Content, `{"sum":3}`)
	}
}

// TestToAnthropicRoundTripJSON verifies that the output can marshal to
// valid JSON (Anthropic API expects strict JSON structure).
func TestToAnthropicRoundTripJSON(t *testing.T) {
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "compute"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "toolu_99", Name: "calc", Arguments: `{"expr":"1+1"}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "toolu_99", Content: "2"},
	}

	_, msgs := toAnthropic(history)
	raw, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	t.Logf("JSON output: %s", string(raw))

	// Unmarshal back and verify structural integrity
	var decoded []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id,omitempty"`
			Content   string `json:"content,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal round-trip failed: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected 3 messages after round-trip, got %d", len(decoded))
	}
}

package agent

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// identifyChainSpans tests
// ---------------------------------------------------------------------------

func TestIdentifyChainSpans_NoChains(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
		{Role: RoleUser, Content: "how are you?"},
		{Role: RoleAssistant, Content: "I'm fine"},
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 0 {
		t.Fatalf("expected 0 chain spans, got %d: %+v", len(spans), spans)
	}
}

func TestIdentifyChainSpans_SingleChain(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "read file x.go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"x.go"}`}}},
		{Role: RoleTool, Content: "package main", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "The file contains package main."},
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 1 {
		t.Fatalf("expected 1 chain span, got %d: %+v", len(spans), spans)
	}
	sp := spans[0]
	if sp.start != 1 || sp.end != 4 {
		t.Fatalf("expected span [1,4), got [%d,%d)", sp.start, sp.end)
	}
	if len(sp.toolNames) != 1 || sp.toolNames[0] != "read_file" {
		t.Fatalf("expected toolNames=[read_file], got %v", sp.toolNames)
	}
}

func TestIdentifyChainSpans_MultiRoundChain(t *testing.T) {
	// assistant calls tool1 → tool result → assistant calls tool2 → tool result → assistant text
	msgs := []Message{
		{Role: RoleUser, Content: "do something"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "file content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "write_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "written", ToolCallID: "c2", Name: "write_file"},
		{Role: RoleAssistant, Content: "Done with the changes."},
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 1 {
		t.Fatalf("expected 1 chain span, got %d: %+v", len(spans), spans)
	}
	sp := spans[0]
	if sp.start != 1 || sp.end != 6 {
		t.Fatalf("expected span [1,6), got [%d,%d)", sp.start, sp.end)
	}
	if len(sp.toolNames) != 2 {
		t.Fatalf("expected 2 tool names, got %v", sp.toolNames)
	}
}

func TestIdentifyChainSpans_MultipleChains(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task 1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "Result 1."},
		{Role: RoleUser, Content: "task 2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "output", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, Content: "Result 2."},
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 2 {
		t.Fatalf("expected 2 chain spans, got %d: %+v", len(spans), spans)
	}
	if spans[0].start != 1 || spans[0].end != 4 {
		t.Fatalf("span 0: expected [1,4), got [%d,%d)", spans[0].start, spans[0].end)
	}
	if spans[1].start != 5 || spans[1].end != 8 {
		t.Fatalf("span 1: expected [5,8), got [%d,%d)", spans[1].start, spans[1].end)
	}
}

func TestIdentifyChainSpans_IncompleteChain(t *testing.T) {
	// Chain starts but has no final text — should NOT be identified as a span
	msgs := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "output", ToolCallID: "c1", Name: "shell"},
		// No final assistant text — chain is incomplete
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 0 {
		t.Fatalf("expected 0 spans for incomplete chain, got %d: %+v", len(spans), spans)
	}
}

func TestIdentifyChainSpans_IncompleteChainWithSecondRound(t *testing.T) {
	// Chain with two tool-call rounds but no final text
	msgs := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "write_file", Arguments: `{}`}}},
		// No tool result and no final text — incomplete
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 0 {
		t.Fatalf("expected 0 spans for incomplete chain, got %d: %+v", len(spans), spans)
	}
}

func TestIdentifyChainSpans_CompleteChainFollowedByIncomplete(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task 1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "ok", ToolCallID: "c1", Name: "shell"},
		{Role: RoleAssistant, Content: "Done."},
		{Role: RoleUser, Content: "task 2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c2", Name: "read_file"},
		// No final text — chain 2 incomplete
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 1 {
		t.Fatalf("expected 1 complete chain span, got %d: %+v", len(spans), spans)
	}
	if spans[0].start != 1 || spans[0].end != 4 {
		t.Fatalf("span 0: expected [1,4), got [%d,%d)", spans[0].start, spans[0].end)
	}
}

func TestIdentifyChainSpans_AssistantWithoutToolCalls(t *testing.T) {
	// Assistant text without tool calls is NOT the start of a chain
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"}, // no tool calls
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 0 {
		t.Fatalf("expected 0 spans, got %d", len(spans))
	}
}

func TestIdentifyChainSpans_DuplicateToolNames(t *testing.T) {
	// Same tool called multiple times in one chain — should appear once in toolNames
	msgs := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "shell", Arguments: `{}`},
			{ID: "c2", Name: "shell", Arguments: `{}`},
		}},
		{Role: RoleTool, Content: "out1", ToolCallID: "c1", Name: "shell"},
		{Role: RoleTool, Content: "out2", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, Content: "Done."},
	}
	spans := identifyChainSpans(msgs)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if len(spans[0].toolNames) != 1 || spans[0].toolNames[0] != "shell" {
		t.Fatalf("expected toolNames=[shell], got %v", spans[0].toolNames)
	}
}

// ---------------------------------------------------------------------------
// CompactMessages tests
// ---------------------------------------------------------------------------

func TestCompactMessages_KeepZero(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "Here is the file."},
	}
	result := CompactMessages(msgs, 0)
	// Should have: user + system(summary) + assistant(final text)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages after compaction, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleUser || result[0].Content != "task" {
		t.Fatalf("msg 0: expected user 'task', got %+v", result[0])
	}
	if result[1].Role != RoleSystem {
		t.Fatalf("msg 1: expected system, got %+v", result[1])
	}
	if !strings.Contains(result[1].Content, "read_file") {
		t.Fatalf("msg 1: expected system to mention 'read_file', got %q", result[1].Content)
	}
	if result[2].Role != RoleAssistant || result[2].Content != "Here is the file." {
		t.Fatalf("msg 2: expected assistant 'Here is the file.', got %+v", result[2])
	}
}

func TestCompactMessages_KeepOne(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task 1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "Result 1."},
		{Role: RoleUser, Content: "task 2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "output", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, Content: "Result 2."},
	}
	result := CompactMessages(msgs, 1)
	// Chain 1 (older) should be compacted, chain 2 (newer) kept intact
	// Expected: user1 + system(chain1 summary) + assistant(chain1 final) + user2 + full chain2
	if len(result) != 7 {
		t.Fatalf("expected 7 messages, got %d: %+v", len(result), result)
	}
	// user "task 1"
	if result[0].Role != RoleUser || result[0].Content != "task 1" {
		t.Fatalf("msg 0: expected user 'task 1', got %+v", result[0])
	}
	// system summary of chain 1
	if result[1].Role != RoleSystem || !strings.Contains(result[1].Content, "read_file") {
		t.Fatalf("msg 1: expected system with 'read_file', got %+v", result[1])
	}
	// assistant final of chain 1
	if result[2].Role != RoleAssistant || result[2].Content != "Result 1." {
		t.Fatalf("msg 2: expected assistant 'Result 1.', got %+v", result[2])
	}
	// user "task 2" — should still be there
	if result[3].Role != RoleUser || result[3].Content != "task 2" {
		t.Fatalf("msg 3: expected user 'task 2', got %+v", result[3])
	}
	// chain 2 kept intact: assistant(tc), tool, assistant(text)
	if result[4].Role != RoleAssistant || len(result[4].ToolCalls) != 1 {
		t.Fatalf("msg 4: expected assistant with tool_calls, got %+v", result[4])
	}
	if result[5].Role != RoleTool {
		t.Fatalf("msg 5: expected tool, got %+v", result[5])
	}
	if result[6].Role != RoleAssistant || result[6].Content != "Result 2." {
		t.Fatalf("msg 6: expected assistant 'Result 2.', got %+v", result[6])
	}
}

func TestCompactMessages_KeepAll(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "ok", ToolCallID: "c1", Name: "shell"},
		{Role: RoleAssistant, Content: "Done."},
	}
	result := CompactMessages(msgs, 5) // keep more than exist
	if len(result) != 4 {
		t.Fatalf("expected all 4 messages, got %d: %+v", len(result), result)
	}
	// All original messages should be present
	for i, m := range msgs {
		if result[i].Role != m.Role || result[i].Content != m.Content {
			t.Fatalf("msg %d: expected %+v, got %+v", i, m, result[i])
		}
	}
}

func TestCompactMessages_NoChains(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	result := CompactMessages(msgs, 0)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (no compaction), got %d", len(result))
	}
}

func TestCompactMessages_WithSystemPrompt(t *testing.T) {
	// System prompt at start should be preserved
	msgs := []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleSystem, Content: "CWD: /project"},
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "Done."},
	}
	result := CompactMessages(msgs, 0)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleSystem || result[0].Content != "You are helpful." {
		t.Fatalf("msg 0: expected system prompt, got %+v", result[0])
	}
	if result[1].Role != RoleSystem || result[1].Content != "CWD: /project" {
		t.Fatalf("msg 1: expected CWD system message, got %+v", result[1])
	}
	if result[2].Role != RoleUser || result[2].Content != "task" {
		t.Fatalf("msg 2: expected user 'task', got %+v", result[2])
	}
	if result[3].Role != RoleSystem || !strings.Contains(result[3].Content, "read_file") {
		t.Fatalf("msg 3: expected system with 'read_file', got %+v", result[3])
	}
	if result[4].Role != RoleAssistant || result[4].Content != "Done." {
		t.Fatalf("msg 4: expected assistant 'Done.', got %+v", result[4])
	}
}

func TestCompactMessages_WithIncompleteChain(t *testing.T) {
	// Incomplete chain at the end should be preserved as-is
	msgs := []Message{
		{Role: RoleUser, Content: "task 1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "done", ToolCallID: "c1", Name: "shell"},
		{Role: RoleAssistant, Content: "Result 1."},
		{Role: RoleUser, Content: "task 2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c2", Name: "read_file"},
		// No final text — incomplete chain, should not be touched
	}
	result := CompactMessages(msgs, 0)
	// Chain 1 compacted, incomplete chain untouched
	// Expected: user1 + system(chain1) + assistant(chain1 final) + user2 + assistant(tc) + tool
	if len(result) != 6 {
		t.Fatalf("expected 6 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleUser || result[0].Content != "task 1" {
		t.Fatalf("msg 0: expected user 'task 1', got %+v", result[0])
	}
	if result[1].Role != RoleSystem || !strings.Contains(result[1].Content, "shell") {
		t.Fatalf("msg 1: expected system summary, got %+v", result[1])
	}
	if result[2].Role != RoleAssistant || result[2].Content != "Result 1." {
		t.Fatalf("msg 2: expected assistant 'Result 1.', got %+v", result[2])
	}
	// Incomplete chain preserved
	if result[3].Role != RoleUser || result[3].Content != "task 2" {
		t.Fatalf("msg 3: expected user 'task 2', got %+v", result[3])
	}
	if result[4].Role != RoleAssistant || len(result[4].ToolCalls) != 1 {
		t.Fatalf("msg 4: expected assistant with tool_calls, got %+v", result[4])
	}
	if result[5].Role != RoleTool || result[5].Content != "data" {
		t.Fatalf("msg 5: expected tool 'data', got %+v", result[5])
	}
}

func TestCompactMessages_MultiRoundChain(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "write_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "written", ToolCallID: "c2", Name: "write_file"},
		{Role: RoleAssistant, Content: "All done."},
	}
	result := CompactMessages(msgs, 0)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (user + system + assistant), got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleUser {
		t.Fatalf("msg 0: expected user, got %+v", result[0])
	}
	if result[1].Role != RoleSystem {
		t.Fatalf("msg 1: expected system, got %+v", result[1])
	}
	// System message should mention both tools
	if !strings.Contains(result[1].Content, "read_file") || !strings.Contains(result[1].Content, "write_file") {
		t.Fatalf("msg 1: expected system to mention both tools, got %q", result[1].Content)
	}
	if result[2].Role != RoleAssistant || result[2].Content != "All done." {
		t.Fatalf("msg 2: expected assistant 'All done.', got %+v", result[2])
	}
}

// ---------------------------------------------------------------------------
// Integration: CompactContext with BuildContext-like behavior
// ---------------------------------------------------------------------------

func TestCompactContext_PreservesSystemPromptAndCWD(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	s.ClientCWD = "/workspace"
	s.Append(Message{Role: RoleUser, Content: "task"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}})
	s.Append(Message{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"})
	s.Append(Message{Role: RoleAssistant, Content: "Here it is."})

	result := CompactContext(s, 0)

	// Should have: system_prompt + CWD + user + system(summary) + assistant(final)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleSystem || result[0].Content != "You are helpful." {
		t.Fatalf("msg 0: expected system prompt, got %+v", result[0])
	}
	if result[1].Role != RoleSystem || !strings.Contains(result[1].Content, "/workspace") {
		t.Fatalf("msg 1: expected CWD message, got %+v", result[1])
	}
	if result[2].Role != RoleUser {
		t.Fatalf("msg 2: expected user, got %+v", result[2])
	}
	if result[3].Role != RoleSystem || !strings.Contains(result[3].Content, "read_file") {
		t.Fatalf("msg 3: expected system summary, got %+v", result[3])
	}
	if result[4].Role != RoleAssistant || result[4].Content != "Here it is." {
		t.Fatalf("msg 4: expected assistant, got %+v", result[4])
	}
}

func TestCompactContext_KeepZeroCompactsAll(t *testing.T) {
	// keepChains=0 means "compact all chains"
	s := NewSession("testns", "sys prompt")
	s.Append(Message{Role: RoleUser, Content: "task"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}}})
	s.Append(Message{Role: RoleTool, Content: "output", ToolCallID: "c1", Name: "shell"})
	s.Append(Message{Role: RoleAssistant, Content: "Done."})

	result := CompactContext(s, 0)
	// Expected: system_prompt + CWD + user + system(summary) + assistant(final)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleSystem || result[0].Content != "sys prompt" {
		t.Fatalf("msg 0: expected system prompt, got %+v", result[0])
	}
	// CWD message
	if result[1].Role != RoleSystem {
		t.Fatalf("msg 1: expected CWD system message, got %+v", result[1])
	}
	if result[2].Role != RoleUser || result[2].Content != "task" {
		t.Fatalf("msg 2: expected user 'task', got %+v", result[2])
	}
	if result[3].Role != RoleSystem || !strings.Contains(result[3].Content, "shell") {
		t.Fatalf("msg 3: expected system summary with 'shell', got %+v", result[3])
	}
	if result[4].Role != RoleAssistant || result[4].Content != "Done." {
		t.Fatalf("msg 4: expected assistant 'Done.', got %+v", result[4])
	}
}

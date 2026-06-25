package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeCompactifyCall(sess *Session, ranges []compactRange) {
	for _, r := range ranges {
		callID := fmt.Sprintf("compactify-%d", len(sess.Messages))
		args, _ := json.Marshal(struct {
			RangeStart int    `json:"range_start"`
			RangeEnd   int    `json:"range_end"`
			Summary    string `json:"summary,omitempty"`
		}{
			RangeStart: r.from,
			RangeEnd:   r.to,
		})

		sess.Append(Message{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:        callID,
				Name:      "context_compactify",
				Arguments: string(args),
			}},
		})
		sess.Append(Message{
			Role:       RoleTool,
			Content:    fmt.Sprintf("Noted: [%d,%d].", r.from, r.to),
			ToolCallID: callID,
			Name:       "context_compactify",
		})
	}
}

// ---------------------------------------------------------------------------
// extractCompactifyRanges tests
// ---------------------------------------------------------------------------

func TestExtractCompactifyRanges_NoCalls(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 0 {
		t.Fatalf("expected 0 ranges, got %v", ranges)
	}
}

func TestExtractCompactifyRanges_SingleCall(t *testing.T) {
	args, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 5, RangeEnd: 20})

	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "context_compactify", Arguments: string(args)},
		}},
		{Role: RoleTool, Content: "noted", ToolCallID: "c1", Name: "context_compactify"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 1 {
		t.Fatalf("expected 1 range, got %d: %v", len(ranges), ranges)
	}
	if ranges[0].from != 5 || ranges[0].to != 20 {
		t.Fatalf("expected [5,20], got [%d,%d]", ranges[0].from, ranges[0].to)
	}
}

func TestExtractCompactifyRanges_MultipleRangesInOneCall(t *testing.T) {
	args1, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 5, RangeEnd: 10})

	args2, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 15, RangeEnd: 20})

	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "context_compactify", Arguments: string(args1)},
		}},
		{Role: RoleTool, Content: "noted", ToolCallID: "c1", Name: "context_compactify"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c2", Name: "context_compactify", Arguments: string(args2)},
		}},
		{Role: RoleTool, Content: "noted", ToolCallID: "c2", Name: "context_compactify"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ranges, got %d: %v", len(ranges), ranges)
	}
}

func TestExtractCompactifyRanges_MultipleCalls(t *testing.T) {
	args1, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 1, RangeEnd: 3})

	args2, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 8, RangeEnd: 12})

	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "context_compactify", Arguments: string(args1)}}},
		{Role: RoleTool, Content: "ok", ToolCallID: "c1", Name: "context_compactify"},
		{Role: RoleUser, Content: "more work"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "context_compactify", Arguments: string(args2)}}},
		{Role: RoleTool, Content: "ok", ToolCallID: "c2", Name: "context_compactify"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 2 {
		t.Fatalf("expected 2 ranges, got %d: %v", len(ranges), ranges)
	}
	if ranges[0].from != 1 || ranges[0].to != 3 {
		t.Fatalf("expected range 0 [1,3], got [%d,%d]", ranges[0].from, ranges[0].to)
	}
	if ranges[1].from != 8 || ranges[1].to != 12 {
		t.Fatalf("expected range 1 [8,12], got [%d,%d]", ranges[1].from, ranges[1].to)
	}
}

// ---------------------------------------------------------------------------
// computeEffectiveInRange tests
// ---------------------------------------------------------------------------

func TestComputeEffectiveInRange_FullRoundCompacted(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "done"},
	}
	merged := []compactRange{{from: 1, to: 2}} // assistant + tool = full round
	inRange := computeEffectiveInRange(msgs, merged)
	if !inRange[1] || !inRange[2] {
		t.Fatalf("expected full round [1,2] in-range, got %v", inRange)
	}
}

func TestComputeEffectiveInRange_PartialRoundNotCompacted(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "done"},
	}
	// Only compact the assistant, not the tool result — this creates a broken chain
	merged := []compactRange{{from: 1, to: 1}}
	inRange := computeEffectiveInRange(msgs, merged)
	if inRange[1] || inRange[2] {
		t.Fatalf("expected partial round NOT compacted, got inRange[1]=%v inRange[2]=%v", inRange[1], inRange[2])
	}
}

func TestComputeEffectiveInRange_UserNeverCompacted(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "important"},
		{Role: RoleAssistant, Content: "done"},
	}
	merged := []compactRange{{from: 0, to: 1}}
	inRange := computeEffectiveInRange(msgs, merged)
	if inRange[0] {
		t.Fatal("user message should never be compacted")
	}
	// assistant should still be compacted
	if !inRange[1] {
		t.Fatal("assistant message should be compacted")
	}
}

func TestComputeEffectiveInRange_MultiToolRound(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "read_file", Arguments: `{}`},
			{ID: "c2", Name: "shell", Arguments: `{}`},
		}},
		{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleTool, Content: "out", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, Content: "done"},
	}
	// Compact only first tool result — should fail atomic check
	merged := []compactRange{{from: 2, to: 2}}
	inRange := computeEffectiveInRange(msgs, merged)
	if inRange[1] || inRange[2] || inRange[3] {
		t.Fatalf("expected round NOT compacted when only one tool result in range, got %v", inRange[1:4])
	}

	// Compact the whole round — should work
	merged2 := []compactRange{{from: 1, to: 3}}
	inRange2 := computeEffectiveInRange(msgs, merged2)
	if !inRange2[1] || !inRange2[2] || !inRange2[3] {
		t.Fatalf("expected full round compacted, got %v", inRange2[1:4])
	}
}

// ---------------------------------------------------------------------------
// buildCompactedView tests
// ---------------------------------------------------------------------------

func TestBuildCompactedView_NoCompaction(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	inRange := []bool{false, false}
	summaryAt := []string{"", ""}

	view, origIdx := buildCompactedView(rawMsgs, inRange, summaryAt)

	if len(view) != 2 {
		t.Fatalf("expected 2 view messages, got %d", len(view))
	}
	if view[0].Content != "hello" || view[1].Content != "hi" {
		t.Fatalf("unexpected view content: %+v", view)
	}
	if origIdx[0] != 0 || origIdx[1] != 1 {
		t.Fatalf("unexpected origIdx: %v", origIdx)
	}
}

func TestBuildCompactedView_CompactifyMessagesFiltered(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "done"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "context_compactify", Arguments: `{"range_start":1,"range_end":2}`}}},
		{Role: RoleTool, Content: "Noted.", ToolCallID: "c2", Name: "context_compactify"},
		{Role: RoleUser, Content: "more"},
	}
	inRange := []bool{false, false, false, false, false, false, false}
	summaryAt := make([]string, len(rawMsgs))

	view, origIdx := buildCompactedView(rawMsgs, inRange, summaryAt)

	// Compactify call (idx 4) and result (idx 5) should be filtered out.
	// Regular tool-call round (indices 1-2) should remain.
	if len(view) != 5 {
		t.Fatalf("expected 5 messages (regular tool round + users), got %d: %+v", len(view), view)
	}
	// Check that compactify messages are gone: we should see user (0), assistant+tool round (1-3), user (6)
	if view[0].Content != "task" {
		t.Fatalf("view[0] = %q, want 'task'", view[0].Content)
	}
	if view[4].Content != "more" {
		t.Fatalf("view[4] = %q, want 'more'", view[4].Content)
	}
	// User messages should map to original indices
	if origIdx[0] != 0 || origIdx[4] != 6 {
		t.Fatalf("unexpected origIdx: %v", origIdx)
	}
}

func TestBuildCompactedView_CompactedRangeBecomesMarker(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, Content: "done"},
	}
	inRange := []bool{false, true, true, false} // indices 1-2 compacted
	summaryAt := []string{"", "Reading project files", "Reading project files", ""}

	view, origIdx := buildCompactedView(rawMsgs, inRange, summaryAt)

	if len(view) != 3 {
		t.Fatalf("expected 3 view messages (user + marker + assistant), got %d: %+v", len(view), view)
	}
	// view[0] is user
	if view[0].Content != "task" {
		t.Fatalf("view[0] = %q, want 'task'", view[0].Content)
	}
	if origIdx[0] != 0 {
		t.Fatalf("origIdx[0] = %d, want 0", origIdx[0])
	}
	// view[1] is the compaction marker
	if !strings.Contains(view[1].Content, "Compacted") || !strings.Contains(view[1].Content, "Reading project files") {
		t.Fatalf("view[1] should be compaction marker with summary, got %q", view[1].Content)
	}
	if origIdx[1] != -1 {
		t.Fatalf("marker should have origIdx=-1, got %d", origIdx[1])
	}
	// view[2] is the final assistant message
	if view[2].Content != "done" {
		t.Fatalf("view[2] = %q, want 'done'", view[2].Content)
	}
	if origIdx[2] != 3 {
		t.Fatalf("origIdx[2] = %d, want 3", origIdx[2])
	}
}

func TestBuildCompactedView_MixedCompactifyAndRegularTools(t *testing.T) {
	// Assistant message with both context_compactify and read_file calls.
	// Only read_file should remain in the view.
	rawMsgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "context_compactify", Arguments: `{"range_start":2,"range_end":2}`},
			{ID: "c2", Name: "read_file", Arguments: `{"path":"x"}`},
		}},
		{Role: RoleTool, Content: "data", ToolCallID: "c2", Name: "read_file"},
	}
	inRange := []bool{false, false, false}
	summaryAt := make([]string, len(rawMsgs))

	view, _ := buildCompactedView(rawMsgs, inRange, summaryAt)

	// Assistant (idx 1) should remain, but with only read_file tool call
	if len(view) != 3 {
		t.Fatalf("expected 3 view messages, got %d", len(view))
	}
	if len(view[1].ToolCalls) != 1 || view[1].ToolCalls[0].Name != "read_file" {
		t.Fatalf("expected assistant with only read_file, got %+v", view[1].ToolCalls)
	}
}

func TestBuildCompactedView_InternalMessagesFiltered(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleSystem, Content: "debug", Internal: true},
		{Role: RoleAssistant, Content: "hi"},
	}
	inRange := []bool{false, false, false}
	summaryAt := make([]string, len(rawMsgs))

	view, _ := buildCompactedView(rawMsgs, inRange, summaryAt)

	if len(view) != 2 {
		t.Fatalf("expected 2 messages (internal filtered), got %d", len(view))
	}
}

func TestBuildCompactedView_EmptyInput(t *testing.T) {
	view, origIdx := buildCompactedView(nil, nil, nil)
	if len(view) != 0 || len(origIdx) != 0 {
		t.Fatalf("expected empty, got view=%v origIdx=%v", view, origIdx)
	}
}

func TestBuildCompactedView_InternalMessagesDontGetIndexPrefixes(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleSystem, Content: "internal", Internal: true},
		{Role: RoleAssistant, Content: "hello"},
	}
	inRange := []bool{false, false, false}
	summaryAt := make([]string, len(rawMsgs))

	view, _ := buildCompactedView(rawMsgs, inRange, summaryAt)
	if len(view) != 2 {
		t.Fatalf("expected 2 view messages, got %d", len(view))
	}
	// verifies internal messages don't appear in view at all
}

// ---------------------------------------------------------------------------
// buildSummaryLookup tests
// ---------------------------------------------------------------------------

func TestBuildSummaryLookup_Empty(t *testing.T) {
	result := buildSummaryLookup(5, nil)
	if len(result) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(result))
	}
	for _, s := range result {
		if s != "" {
			t.Fatalf("expected empty string, got %q", s)
		}
	}
}

func TestBuildSummaryLookup_SingleRange(t *testing.T) {
	merged := []compactRange{{from: 1, to: 3, summary: "exploration"}}
	result := buildSummaryLookup(5, merged)
	if result[0] != "" || result[1] != "exploration" || result[2] != "exploration" || result[3] != "exploration" || result[4] != "" {
		t.Fatalf("unexpected summaryAt: %v", result)
	}
}

func TestBuildSummaryLookup_MultipleRanges(t *testing.T) {
	merged := []compactRange{
		{from: 0, to: 1, summary: "greeting"},
		{from: 4, to: 5, summary: "tools"},
	}
	result := buildSummaryLookup(6, merged)
	if result[0] != "greeting" || result[1] != "greeting" || result[2] != "" || result[4] != "tools" || result[5] != "tools" {
		t.Fatalf("unexpected summaryAt: %v", result)
	}
}

func TestBuildCompactContext_NoCompactionUnderLimit(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleUser, Content: "hello"})
	s.Append(Message{Role: RoleAssistant, Content: "hi there"})

	result, needCompactify, _ := BuildCompactContext(s, 100000)

	if needCompactify {
		t.Fatal("expected needCompactify=false")
	}
	if len(result) < 4 {
		t.Fatalf("expected at least 4 messages, got %d: %+v", len(result), result)
	}
	if result[0].Role != RoleSystem || result[0].Content != "You are helpful." {
		t.Fatalf("msg 0: expected system prompt, got %+v", result[0])
	}
	if result[len(result) - 2].Role != RoleUser || result[len(result) - 2].Content != "hello" {
		t.Fatalf("msg 3: expected user 'hello', got %+v", result[len(result) - 2])
	}
	if result[len(result) - 1].Role != RoleAssistant || result[len(result) - 1].Content != "hi there" {
		t.Fatalf("msg 4: expected assistant 'hi there', got %+v", result[len(result) - 1])
	}
}

func TestBuildCompactContext_SoftLimitTriggersWarning(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	bigContent := strings.Repeat("x", 400)
	s.Append(Message{Role: RoleUser, Content: bigContent})
	s.Append(Message{Role: RoleAssistant, Content: "ok"})

	result, needCompactify, _ := BuildCompactContext(s, 10)

	if !needCompactify {
		t.Fatal("expected needCompactify=true because content exceeds soft limit")
	}

	// Warning should appear in the LLM view (ephemeral, not persisted).
	// Count warnings in result, excluding the proactive notice (msg 1).
	warningCount := 0
	for _, m := range result[2:] {
		if m.Role == RoleUser && strings.Contains(m.Content, "context_compactify") {
			warningCount++
		}
	}
	if warningCount == 0 {
		t.Fatal("expected warning in LLM view")
	}

	// Last message of result should be the warning
	last := result[len(result)-1]
	if last.Role != RoleUser {
		t.Fatalf("expected last message to be system warning, got %s", last.Role)
	}
	if !strings.Contains(last.Content, "context_compactify") {
		t.Fatalf("warning should mention context_compactify, got %q", last.Content)
	}

	// Messages should have [N] ~Tt: index prefixes
	foundIndexed := false
	for _, m := range result {
		if m.Role == RoleUser && strings.Contains(m.Content, "[0] ~") && strings.Contains(m.Content, "tokens:\n") {
			foundIndexed = true
			break
		}
	}
	if !foundIndexed {
		t.Fatal("expected user message to have [0] ~Tt: index prefix when soft limit triggered")
	}
}

func TestBuildCompactContext_PastCompactifyCallApplied(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleUser, Content: "read file"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"f.go"}`}}})
	s.Append(Message{Role: RoleTool, Content: "package main", ToolCallID: "c1", Name: "read_file"})
	s.Append(Message{Role: RoleAssistant, Content: "I've read the file."})
	makeCompactifyCall(s, []compactRange{{from: 1, to: 2}})
	s.Append(Message{Role: RoleUser, Content: "now write"})
	s.Append(Message{Role: RoleAssistant, Content: "Writing..."})

	result, needCompactify, _:= BuildCompactContext(s, 100000)

	for _, m := range result {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("context_compactify tool result should be filtered, got %+v", m)
		}
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					t.Fatalf("context_compactify tool call should be filtered, got %+v", m)
				}
			}
		}
	}

	foundMarker := false
	for _, m := range result {
		if m.Role == RoleSystem && strings.Contains(m.Content, "Compacted") {
			foundMarker = true
			if !strings.Contains(m.Content, "read_file") {
				t.Fatalf("compaction marker should mention 'read_file', got %q", m.Content)
			}
			break
		}
	}
	if !foundMarker {
		t.Fatalf("expected a [Compacted ...] marker, got: %+v", result)
	}

	if needCompactify {
		t.Fatal("expected needCompactify=false (well under limit)")
	}
}

func TestBuildCompactContext_UserMessagesNeverCompacted(t *testing.T) {
	s := NewSession("testns", "sys")
	s.Append(Message{Role: RoleUser, Content: "initial task"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c1", Name: "read_file", Arguments: `{}`},
	}})
	s.Append(Message{Role: RoleTool, Content: "data", ToolCallID: "c1", Name: "read_file"})
	s.Append(Message{Role: RoleAssistant, Content: "done reading"})
	s.Append(Message{Role: RoleUser, Content: "second task"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c2", Name: "shell", Arguments: `{}`},
	}})
	s.Append(Message{Role: RoleTool, Content: "output", ToolCallID: "c2", Name: "shell"})
	s.Append(Message{Role: RoleAssistant, Content: "done shelling"})

	makeCompactifyCall(s, []compactRange{{from: 0, to: 7}})

	result, _, _ := BuildCompactContext(s, 100000)

	foundInitial := false
	foundSecond := false
	for _, m := range result {
		if m.Role == RoleUser && m.Content == "initial task" {
			foundInitial = true
		}
		if m.Role == RoleUser && m.Content == "second task" {
			foundSecond = true
		}
	}
	if !foundInitial {
		t.Fatal("expected 'initial task' user message to survive compaction untouched")
	}
	if !foundSecond {
		t.Fatal("expected 'second task' user message to survive compaction untouched")
	}
}

func TestBuildCompactContext_PartialRoundNotCompacted(t *testing.T) {
	// When a compactify range only covers part of a tool-call round,
	// the whole round must stay visible to avoid OpenAI protocol errors.
	s := NewSession("testns", "")
	s.Append(Message{Role: RoleUser, Content: "task"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}})
	s.Append(Message{Role: RoleTool, Content: "result1", ToolCallID: "c1", Name: "read_file"})
	s.Append(Message{Role: RoleAssistant, Content: "done"})

	// Try to compact only index 2 (the tool result) — should fail atomic check
	makeCompactifyCall(s, []compactRange{{from: 2, to: 2}})

	result, _, _ := BuildCompactContext(s, 100000)

	// The tool result must still be present
	foundTool := false
	for _, m := range result {
		if m.Role == RoleTool && m.Content == "result1" {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Fatal("expected tool result to survive — partial round should not be compacted")
	}

	// And no dangling tool_calls on the assistant
	for _, m := range result {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				// If tool_calls exist, their results must be in the result
				hasResult := false
				for _, m2 := range result {
					if m2.Role == RoleTool && m2.ToolCallID == tc.ID {
						hasResult = true
						break
					}
				}
				if !hasResult {
					t.Fatalf("dangling tool_call %s (no matching tool result in view)", tc.ID)
				}
			}
		}
	}
}

func TestBuildCompactContext_NoSoftLimitWhenUnder(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleUser, Content: "hi"})
	s.Append(Message{Role: RoleAssistant, Content: "hello"})

	result, needCompactify, _ := BuildCompactContext(s, 150000)

	if needCompactify {
		t.Fatal("expected needCompactify=false")
	}
	for _, m := range result {
		if m.Role == RoleUser && strings.HasPrefix(m.Content, "[") {
			t.Fatalf("expected no index prefix, got %q", m.Content)
		}
	}
	// The proactive compactify notice is always injected (msg 1), but
	// there should be no SECOND system message about compactify (fresh warning).
	freshWarningCount := 0
	for _, m := range result[2:] { // skip system prompt (0) and proactive notice (1)
		if strings.Contains(m.Content, "context_compactify") {
			freshWarningCount++
		}
	}
	if freshWarningCount > 0 {
		t.Fatalf("expected no fresh compactify warning, got %d", freshWarningCount)
	}
}

func TestBuildCompactContext_EmptySession(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	result, needCompactify, _ := BuildCompactContext(s, 100000)

	if needCompactify {
		t.Fatal("empty session should not trigger compactify")
	}
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages (system prompt + CWD), got %d", len(result))
	}
}

func TestBuildCompactContext_InternalMessagesFiltered(t *testing.T) {
	// Internal messages should never appear in the LLM view,
	// regardless of their role.

	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleSystem, Content: "internal debug note", Internal: true})
	s.Append(Message{Role: RoleUser, Content: "hello"})
	s.Append(Message{Role: RoleAssistant, Content: "hi"})

	result, _, _ := BuildCompactContext(s, 100000)

	for _, m := range result {
		if m.Internal {
			t.Fatalf("internal message leaked into LLM view: %+v", m)
		}
		if m.Role == RoleSystem && strings.Contains(m.Content, "internal debug note") {
			t.Fatal("internal message content leaked into LLM view")
		}
	}
}

func TestBuildCompactContext_InternalMessagesDontGetIndexPrefixes(t *testing.T) {
	// Internal messages must not get [N] index prefixes (they're never
	// shown to the LLM, so they shouldn't affect indexing).

	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleSystem, Content: "internal note", Internal: true})
	bigContent := strings.Repeat("x", 500)
	s.Append(Message{Role: RoleUser, Content: bigContent})
	s.Append(Message{Role: RoleAssistant, Content: "ok"})

	result, _, _ := BuildCompactContext(s, 10)

	// The [N] prefix on the user message should be [1] (index 1 in raw session),
	// NOT [2] — because the internal message at index 0 is skipped.
	foundUser := false
	for _, m := range result {
		if m.Role == RoleUser && strings.Contains(m.Content, "[1] ~") && strings.Contains(m.Content, bigContent) {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatal("user message not found in result")
	}
}

// ---------------------------------------------------------------------------
// Leak prevention tests — compactify must never appear in LLM context
// ---------------------------------------------------------------------------

func TestBuildCompactContext_CompactifyMessagesNeverInOutput(t *testing.T) {
	// Regardless of whether needCompactify is true or false,
	// context_compactify tool calls and results must never appear
	// in the output of BuildCompactContext. The whole process must
	// be transparent to the LLM.

	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleUser, Content: "task"})
	s.Append(Message{Role: RoleAssistant, Content: "done"})
	makeCompactifyCall(s, []compactRange{{from: 0, to: 0}})
	s.Append(Message{Role: RoleUser, Content: "more work"})
	s.Append(Message{Role: RoleAssistant, Content: "ok"})

	// Test with high soft limit (needCompactify=false).
	result, needCompactify, _ := BuildCompactContext(s, 100000)
	if needCompactify {
		t.Fatal("soft limit 100000 should not trigger compaction")
	}

	// Verify no compactify messages leak (except the proactive notice).
	proactiveNotice := "You have access to `context_compactify` tool"
	for _, m := range result {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("context_compactify tool result leaked into LLM view: %+v", m)
		}
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					t.Fatalf("context_compactify tool call leaked into LLM view: %+v", m)
				}
			}
		}
		if m.Role == RoleSystem && strings.Contains(m.Content, "context_compactify") && !strings.Contains(m.Content, proactiveNotice) {
			// Only the fresh warning (for current turn) is allowed in the view.
			// When needCompactify=false, no warning should exist.
			t.Fatalf("unexpected compactify-related system message in view: %+v", m)
		}
	}

	// Test with low soft limit (needCompactify=true).
	// Add big content to trigger.
	bigContent := strings.Repeat("x", 1000)
	s.Append(Message{Role: RoleUser, Content: bigContent})
	s.Append(Message{Role: RoleAssistant, Content: "final"})

	result2, needCompactify2, _ := BuildCompactContext(s, 10)
	if !needCompactify2 {
		t.Fatal("soft limit 10 should trigger compaction")
	}

	// Still no compactify messages should leak.
	for _, m := range result2 {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("context_compactify tool result leaked into LLM view (compact mode): %+v", m)
		}
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					t.Fatalf("context_compactify tool call leaked into LLM view (compact mode): %+v", m)
				}
			}
		}
	}
}

func TestBuildCompactContext_TokenEstimateExcludesCompactifyMessages(t *testing.T) {
	// The token estimate used to decide needCompactify must be computed
	// on the FILTERED view (after compactify messages are removed).
	// Otherwise compactify messages inflate the estimate, creating a
	// positive feedback loop.

	s := NewSession("testns", "")
	// Add enough real content to be near the limit.
	s.Append(Message{Role: RoleUser, Content: strings.Repeat("a", 2000)})
	s.Append(Message{Role: RoleAssistant, Content: strings.Repeat("b", 2000)})

	// Now add a compactify call with very large content in the arguments
	// (simulating a large range). This should NOT affect the token estimate.
	largeArgs := strings.Repeat("x", 10000) // 10KB of fake arguments
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{
		ID:        "c-big",
		Name:      "context_compactify",
		Arguments: largeArgs,
	}}})
	s.Append(Message{Role: RoleTool, Content: "noted", ToolCallID: "c-big", Name: "context_compactify"})

	// The real content is ~4000 chars → ~1000 tokens.
	// The compactify call adds ~10000 chars → ~2500 tokens if counted.
	// With a soft limit of 500, we should see needCompactify=true
	// because 1000 > 500 (compactify messages are excluded from estimate).
	result, needCompactify, _ := BuildCompactContext(s, 500)

	if !needCompactify {
		t.Fatal("expected needCompactify=true (real content ~1000 tokens > soft limit 500)")
	}

	// Verify the compactify messages are NOT in the result.
	for _, m := range result {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("compactify result leaked: %+v", m)
		}
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					t.Fatalf("compactify call leaked: %+v", m)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Schema in compaction mode tests
// ---------------------------------------------------------------------------

func TestCompactionModeOffersCompactify(t *testing.T) {
	schemas := []ToolSchema{
		{Name: "read_file"},
		{Name: "shell"},
		{Name: "write_file"},
	}

	needCompactify := true
	var turnSchemas []ToolSchema
	if needCompactify {
		turnSchemas = append(schemas, contextCompactifySchema)
	} else {
		turnSchemas = schemas
	}

	if len(turnSchemas) != 4 {
		t.Fatalf("expected 4 schemas (3 normal + compactify), got %d", len(turnSchemas))
	}

	seen := make(map[string]bool)
	for _, s := range turnSchemas {
		if seen[s.Name] {
			t.Fatalf("duplicate schema name %q", s.Name)
		}
		seen[s.Name] = true
	}

	hasCompactify := false
	for _, s := range turnSchemas {
		if s.Name == "context_compactify" {
			hasCompactify = true
			break
		}
	}
	if !hasCompactify {
		t.Fatal("expected context_compactify in schemas")
	}
}

func TestCompactionModeNormalSchemasWhenUnderLimit(t *testing.T) {
	schemas := []ToolSchema{
		{Name: "read_file"},
		{Name: "shell"},
	}

	needCompactify := false
	var turnSchemas []ToolSchema
	if needCompactify {
		turnSchemas = []ToolSchema{contextCompactifySchema}
	} else {
		turnSchemas = schemas
	}

	if len(turnSchemas) != 2 {
		t.Fatalf("expected 2 schemas when under limit, got %d", len(turnSchemas))
	}
}

// ---------------------------------------------------------------------------
// Bug #4: Invalid compactify ranges (negative range_start) must not cause
// a panic. The tool handler rejects them, but the call is still recorded
// in the session and extractCompactifyRanges must filter them out.
// ---------------------------------------------------------------------------

func TestExtractCompactifyRanges_RejectsNegativeRangeStart(t *testing.T) {
	// Simulate LLM calling compactify with range_start=-1 (rejected by
	// handler, but call is still recorded in session).
	args, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: -1, RangeEnd: 5})

	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "context_compactify", Arguments: string(args)},
		}},
		{Role: RoleTool, Content: "Error: invalid range", ToolCallID: "c1", Name: "context_compactify"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 0 {
		t.Fatalf("expected 0 ranges (invalid range filtered), got %d: %v", len(ranges), ranges)
	}
}

func TestExtractCompactifyRanges_RejectsInvertedRange(t *testing.T) {
	// range_start > range_end is also invalid.
	args, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 10, RangeEnd: 3})

	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Name: "context_compactify", Arguments: string(args)},
		}},
		{Role: RoleTool, Content: "Error: invalid range", ToolCallID: "c1", Name: "context_compactify"},
	}
	ranges := extractCompactifyRanges(msgs)
	if len(ranges) != 0 {
		t.Fatalf("expected 0 ranges (inverted range filtered), got %d: %v", len(ranges), ranges)
	}
}

func TestBuildCompactContext_InvalidRangeDoesNotCausePanic(t *testing.T) {
	// Full integration: invalid compactify call in session should not
	// cause a panic in BuildCompactContext.
	s := NewSession("testns", "You are helpful.")
	s.Append(Message{Role: RoleUser, Content: "task"})
	s.Append(Message{Role: RoleAssistant, Content: "done"})

	// Record an invalid compactify call (range_start=-1).
	invalidArgs, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: -1, RangeEnd: 3})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: "c-bad", Name: "context_compactify", Arguments: string(invalidArgs)},
	}})
	s.Append(Message{Role: RoleTool, Content: "Error: invalid range", ToolCallID: "c-bad", Name: "context_compactify"})

	s.Append(Message{Role: RoleUser, Content: "more work"})
	s.Append(Message{Role: RoleAssistant, Content: "ok"})

	// Must not panic.
	result, _, _ := BuildCompactContext(s, 100000)

	// The invalid compactify call must be filtered, and normal messages
	// must still appear.
	foundUser := false
	for _, m := range result {
		if m.Role == RoleUser && m.Content == "more work" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatal("valid user messages must survive despite invalid compactify call")
	}

	// No compactify messages leaked.
	for _, m := range result {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("compactify result leaked: %+v", m)
		}
	}
}

// ---------------------------------------------------------------------------
// Bug #1: Warning accumulation — BuildCompactContext appends a new
// Internal warning to the session on every call when needCompactify=true.
// Over many iterations this bloats session storage.
// ---------------------------------------------------------------------------

func TestBuildCompactContext_NoDuplicateWarnings(t *testing.T) {
	s := NewSession("testns", "You are helpful.")
	bigContent := strings.Repeat("x", 500)
	s.Append(Message{Role: RoleUser, Content: bigContent})
	s.Append(Message{Role: RoleAssistant, Content: "ok"})

	// Call BuildCompactContext multiple times — simulating multiple
	// tool-loop iterations where the LLM has not yet called compactify.
	for i := 0; i < 5; i++ {
		result, needCompactify, _ := BuildCompactContext(s, 10)
		if !needCompactify {
			t.Fatalf("iteration %d: expected needCompactify=true", i)
		}
		// The fresh warning must appear in the view.
		found := false
		for _, m := range result {
			if m.Role == RoleSystem && strings.Contains(m.Content, "context_compactify") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("iteration %d: fresh warning must appear in LLM view", i)
		}
	}

	// Count how many internal warnings were persisted to the session.
	// Warnings are now ephemeral (in LLM view only, not persisted) to avoid
	// accumulation. There should be 0 persisted warnings.
	warningCount := 0
	for _, m := range s.AllMessages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "context_compactify") && m.Internal {
			warningCount++
		}
	}
	if warningCount != 0 {
		t.Fatalf("expected 0 internal warnings persisted to session (ephemeral), got %d", warningCount)
	}
}

// ---------------------------------------------------------------------------
// Bug #3: Mixed context_compactify + regular tool calls cause dangling
// tool calls. When the LLM calls compactify alongside another tool
// (e.g. read_file), isCompactifyMessage returns false for the assistant
// (not ALL calls are compactify), so the assistant stays in the view
// with both tool calls. But the compactify tool result is filtered out
// — causing a dangling tool call with no result.
// ---------------------------------------------------------------------------

func TestBuildCompactContext_MixedCompactifyAndRegularTools(t *testing.T) {
	// When the LLM calls context_compactify alongside other tools in the
	// same assistant message, and the compactify range does NOT fully
	// cover the round (so the atomic check leaves the round visible),
	// the context_compactify call must be stripped from the assistant's
	// ToolCalls — otherwise it becomes a dangling tool call (the
	// compactify tool result is always filtered).

	s := NewSession("testns", "You are helpful.")

	readCallID := "c-read"
	compactifyCallID := "c-compactify"

	// The compactify range only covers index 2 (the read_file result),
	// NOT the whole round [1,3]. This fails the atomic check, leaving
	// the entire round visible.
	compactifyArgs, _ := json.Marshal(struct {
		RangeStart int `json:"range_start"`
		RangeEnd   int `json:"range_end"`
	}{RangeStart: 2, RangeEnd: 2})

	s.Append(Message{Role: RoleUser, Content: "read and compact"})

	// Assistant message with mixed tool calls.
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{
		{ID: readCallID, Name: "read_file", Arguments: `{"path":"f.go"}`},
		{ID: compactifyCallID, Name: "context_compactify", Arguments: string(compactifyArgs)},
	}})

	// Tool results (both).
	s.Append(Message{Role: RoleTool, Content: "file content", ToolCallID: readCallID, Name: "read_file"})
	s.Append(Message{Role: RoleTool, Content: "noted", ToolCallID: compactifyCallID, Name: "context_compactify"})

	s.Append(Message{Role: RoleAssistant, Content: "done"})

	result, _, _ := BuildCompactContext(s, 100000)

	// Verify: no context_compactify tool results in the view.
	for _, m := range result {
		if m.Role == RoleTool && m.Name == "context_compactify" {
			t.Fatalf("context_compactify tool result leaked: %+v", m)
		}
	}

	// Verify: no context_compactify in any assistant's ToolCalls.
	for _, m := range result {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					t.Fatalf("context_compactify tool call leaked in assistant ToolCalls: %+v", m)
				}
			}
		}
	}

	// Verify: the read_file call and its result are still intact.
	foundReadCall := false
	foundReadResult := false
	for _, m := range result {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "read_file" && tc.ID == readCallID {
					foundReadCall = true
				}
			}
		}
		if m.Role == RoleTool && m.Name == "read_file" && m.ToolCallID == readCallID {
			foundReadResult = true
		}
	}
	if !foundReadCall {
		t.Fatal("read_file tool call was lost from assistant message")
	}
	if !foundReadResult {
		t.Fatal("read_file tool result was lost from view")
	}

	// Verify: no dangling tool calls (every call has a result).
	for _, m := range result {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				hasResult := false
				for _, m2 := range result {
					if m2.Role == RoleTool && m2.ToolCallID == tc.ID {
						hasResult = true
						break
					}
				}
				if !hasResult {
					t.Fatalf("dangling tool_call %s (%s) — no matching result in view", tc.ID, tc.Name)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// estimateTokens tests — using per-message Usage when available
// ---------------------------------------------------------------------------

func TestEstimateTokens_UsesLastMessageUsagePromptTokens(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "world", Usage: &Usage{PromptTokens: 42, CompletionTokens: 5, TotalTokens: 47}},
	}
	total := estimateTokens(msgs)
	// Should use PromptTokens from the last message with Usage
	if total != 42 {
		t.Fatalf("expected 42 (last Usage.PromptTokens), got %d", total)
	}
}

func TestEstimateTokens_UsesOnlyLastUsage(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: "first", Usage: &Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
		{Role: RoleTool, Content: "result", ToolCallID: "c1"},
		{Role: RoleAssistant, Content: "last", Usage: &Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}},
	}
	total := estimateTokens(msgs)
	// Should use PromptTokens from the last (most recent) Usage
	if total != 100 {
		t.Fatalf("expected 100 (last Usage.PromptTokens), got %d", total)
	}
}

func TestEstimateTokens_FallsBackToHeuristicWhenNoUsage(t *testing.T) {
	content := strings.Repeat("x", 100)
	msgs := []Message{
		{Role: RoleAssistant, Content: content},
	}
	total := estimateTokens(msgs)
	// chars/4 = 100/4 = 25
	if total != 25 {
		t.Fatalf("expected 25 (chars/4 heuristic), got %d", total)
	}
}

func TestEstimateTokens_MixedMessagesNoUsage(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "a"},     // 1/4 = 0
		{Role: RoleAssistant, Content: "bb"},  // 2/4 = 0
		{Role: RoleAssistant, Content: "cccc"}, // 4/4 = 1
	}
	total := estimateTokens(msgs)
	if total != 1 { // 0 + 0 + 1
		t.Fatalf("expected 1 (sum of heuristics), got %d", total)
	}
}

func TestEstimateTokens_EmptyMessages(t *testing.T) {
	total := estimateTokens(nil)
	if total != 0 {
		t.Fatalf("expected 0, got %d", total)
	}
	total = estimateTokens([]Message{})
	if total != 0 {
		t.Fatalf("expected 0, got %d", total)
	}
}

func TestMessageTokens_AlwaysUsesHeuristic(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: "some text", Usage: &Usage{PromptTokens: 999, CompletionTokens: 50, TotalTokens: 1049}}
	total := messageTokens(m)
	// messageTokens should ignore Usage — estimateTokens handles it at the list level
	if total == 999 || total == 1049 {
		t.Fatalf("messageTokens must NOT use Usage (it describes the whole prompt), got %d", total)
	}
	expected := len("some text") / 4 // 8/4 = 2
	if total != expected {
		t.Fatalf("expected %d (chars/4 heuristic), got %d", expected, total)
	}
}

func TestMessageTokens_FallsBackToHeuristic(t *testing.T) {
	content := strings.Repeat("abc", 40) // 120 chars
	m := Message{Role: RoleAssistant, Content: content}
	total := messageTokens(m)
	if total != 30 { // 120/4
		t.Fatalf("expected 30 (chars/4), got %d", total)
	}
}

// ---------------------------------------------------------------------------
// heuristicTokens tests
// ---------------------------------------------------------------------------

func TestHeuristicTokens_SimpleText(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
	}
	total := heuristicTokens(msgs)
	expected := len("hello")/4 + len("hi there")/4
	if total != expected {
		t.Fatalf("expected %d, got %d", expected, total)
	}
}

func TestHeuristicTokens_IncludesToolCalls(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	}
	total := heuristicTokens(msgs)
	expected := len("task")/4 + len("read_file")/4 + len(`{"path":"x"}`)/4
	if total != expected {
		t.Fatalf("expected %d, got %d", expected, total)
	}
}

func TestHeuristicTokens_Empty(t *testing.T) {
	if heuristicTokens(nil) != 0 {
		t.Fatal("expected 0 for nil")
	}
	if heuristicTokens([]Message{}) != 0 {
		t.Fatal("expected 0 for empty")
	}
}

// ---------------------------------------------------------------------------
// latestAccurateUsage tests
// ---------------------------------------------------------------------------

func TestLatestAccurateUsage_ReturnsLastUsage(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi", Usage: &Usage{PromptTokens: 100}},
		{Role: RoleUser, Content: "more"},
		{Role: RoleAssistant, Content: "done", Usage: &Usage{PromptTokens: 250}},
	}
	usage := latestAccurateUsage(rawMsgs)
	if usage != 250 {
		t.Fatalf("expected 250, got %d", usage)
	}
}

func TestLatestAccurateUsage_ResetsOnCompactify(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleAssistant, Content: "old", Usage: &Usage{PromptTokens: 500}},
		{Role: RoleTool, Content: "Noted.", Name: "context_compactify"},
		{Role: RoleAssistant, Content: "new", Usage: &Usage{PromptTokens: 60}},
	}
	usage := latestAccurateUsage(rawMsgs)
	if usage != 60 {
		t.Fatalf("expected 60 (post-compactify), got %d", usage)
	}
}

func TestLatestAccurateUsage_CompactifyClearsStaleUsage(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleAssistant, Content: "old", Usage: &Usage{PromptTokens: 500}},
		{Role: RoleTool, Content: "Noted.", Name: "context_compactify"},
		// No new usage after compactify — should return -1
		{Role: RoleUser, Content: "hello"},
	}
	usage := latestAccurateUsage(rawMsgs)
	if usage != -1 {
		t.Fatalf("expected -1 (stale after compactify), got %d", usage)
	}
}

func TestLatestAccurateUsage_NoUsageReturnsMinusOne(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	usage := latestAccurateUsage(rawMsgs)
	if usage != -1 {
		t.Fatalf("expected -1, got %d", usage)
	}
}

func TestLatestAccurateUsage_EmptyReturnsMinusOne(t *testing.T) {
	if latestAccurateUsage(nil) != -1 {
		t.Fatal("expected -1 for nil")
	}
	if latestAccurateUsage([]Message{}) != -1 {
		t.Fatal("expected -1 for empty")
	}
}

func TestLatestAccurateUsage_ZeroPromptTokensIgnored(t *testing.T) {
	rawMsgs := []Message{
		{Role: RoleAssistant, Content: "msg", Usage: &Usage{PromptTokens: 0, CompletionTokens: 100, TotalTokens: 100}},
	}
	usage := latestAccurateUsage(rawMsgs)
	if usage != -1 {
		t.Fatalf("expected -1 (0 PromptTokens ignored), got %d", usage)
	}
}

func TestMessageTokens_WithToolCallsAndNoUsage(t *testing.T) {
	m := Message{
		Role:    RoleAssistant,
		Content: "result",
		ToolCalls: []ToolCall{
			{Name: "read_file", Arguments: `{"path":"/foo"}`},
		},
	}
	total := messageTokens(m)
	expected := len("result")/4 + len("read_file")/4 + len(`{"path":"/foo"}`)/4
	if total != expected {
		t.Fatalf("expected %d (chars/4), got %d", expected, total)
	}
}

// ---------------------------------------------------------------------------
// Bug: Separate compaction ranges separated by compactify messages get merged
// into one marker instead of producing separate markers with distinct summaries.
// ---------------------------------------------------------------------------

func TestBuildCompactedView_SeparateRangesStaySeparateAcrossCompactifyMessages(t *testing.T) {
	// Scenario: Two separate tool-call rounds that have been compacted by two
	// different compactify calls. The compactify call/result messages sit
	// BETWEEN the two inRange spans in the raw message list.
	//
	// Raw messages:
	//   idx 0: User "task1"
	//   idx 1: Assistant (read_file)  ← inRange (compact range 1: [1,2])
	//   idx 2: Tool (result1)         ← inRange (compact range 1: [1,2])
	//   idx 3: Assistant (compactify)  ← first compactify call (isCompactifyMessage)
	//   idx 4: Tool (compactify result) ← first compactify result (isCompactifyMessage)
	//   idx 5: User "task2"
	//   idx 6: Assistant (shell)      ← inRange (compact range 2: [6,7])
	//   idx 7: Tool (result2)         ← inRange (compact range 2: [6,7])
	//   idx 8: Assistant (compactify)  ← second compactify call (isCompactifyMessage)
	//   idx 9: Tool (compactify result) ← second compactify result (isCompactifyMessage)
	//
	// Expected: TWO separate [Compacted ...] markers, one for each range,
	// with their respective summaries.

	rawMsgs := []Message{
		{Role: RoleUser, Content: "task1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "result1", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc1", Name: "context_compactify", Arguments: `{"range_start":1,"range_end":2,"summary":"Reading files"}`}}},
		{Role: RoleTool, Content: "Noted.", ToolCallID: "cc1", Name: "context_compactify"},
		{Role: RoleUser, Content: "task2"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "result2", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc2", Name: "context_compactify", Arguments: `{"range_start":6,"range_end":7,"summary":"Executing shell"}`}}},
		{Role: RoleTool, Content: "Noted.", ToolCallID: "cc2", Name: "context_compactify"},
	}

	// Extract and merge ranges, then build the view.
	ranges := extractCompactifyRanges(rawMsgs)
	inRange := computeEffectiveInRange(rawMsgs, ranges)
	summaryAt := buildSummaryLookup(len(rawMsgs), ranges)

	view, _ := buildCompactedView(rawMsgs, inRange, summaryAt)

	// We should see: user "task1", marker1, user "task2", marker2
	// = 4 messages in view (compactify calls/results are filtered).
	if len(view) != 4 {
		t.Fatalf("expected 4 view messages (2 users + 2 markers), got %d: %+v", len(view), view)
	}

	if view[0].Content != "task1" {
		t.Fatalf("view[0] = %q, want 'task1'", view[0].Content)
	}
	if view[2].Content != "task2" {
		t.Fatalf("view[2] = %q, want 'task2'", view[2].Content)
	}

	// First marker should have summary "Reading files"
	if !strings.Contains(view[1].Content, "Reading files") {
		t.Fatalf("view[1] (first marker) should contain 'Reading files', got %q", view[1].Content)
	}
	if strings.Contains(view[1].Content, "Executing shell") {
		t.Fatalf("view[1] (first marker) should NOT contain 'Executing shell', got %q", view[1].Content)
	}

	// Second marker should have summary "Executing shell"
	if !strings.Contains(view[3].Content, "Executing shell") {
		t.Fatalf("view[3] (second marker) should contain 'Executing shell', got %q", view[3].Content)
	}
	if strings.Contains(view[3].Content, "Reading files") {
		t.Fatalf("view[3] (second marker) should NOT contain 'Reading files', got %q", view[3].Content)
	}
}

func TestBuildCompactContext_MultipleSeparateCompactionsHaveDistinctSummaries(t *testing.T) {
	// Integration test: Two rounds of tool use, each compacted separately
	// with different summaries. The result should show two distinct markers.
	s := NewSession("testns", "")

	// Round 1
	s.Append(Message{Role: RoleUser, Content: "read files"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.go"}`}}})
	s.Append(Message{Role: RoleTool, Content: "content1", ToolCallID: "c1", Name: "read_file"})
	s.Append(Message{Role: RoleAssistant, Content: "done reading"})

	// First compactify call with summary "Reading project files"
	compactifyArgs1, _ := json.Marshal(struct {
		RangeStart int    `json:"range_start"`
		RangeEnd   int    `json:"range_end"`
		Summary    string `json:"summary,omitempty"`
	}{RangeStart: 1, RangeEnd: 3, Summary: "Reading project files"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc1", Name: "context_compactify", Arguments: string(compactifyArgs1)}}})
	s.Append(Message{Role: RoleTool, Content: "Noted.", ToolCallID: "cc1", Name: "context_compactify"})

	// Round 2
	s.Append(Message{Role: RoleUser, Content: "write code"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "write_file", Arguments: `{"path":"b.go"}`}}})
	s.Append(Message{Role: RoleTool, Content: "written", ToolCallID: "c2", Name: "write_file"})
	s.Append(Message{Role: RoleAssistant, Content: "done writing"})

	// Second compactify call with different summary
	compactifyArgs2, _ := json.Marshal(struct {
		RangeStart int    `json:"range_start"`
		RangeEnd   int    `json:"range_end"`
		Summary    string `json:"summary,omitempty"`
	}{RangeStart: 6, RangeEnd: 8, Summary: "Writing implementation"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc2", Name: "context_compactify", Arguments: string(compactifyArgs2)}}})
	s.Append(Message{Role: RoleTool, Content: "Noted.", ToolCallID: "cc2", Name: "context_compactify"})

	// Now build compact context
	result, needCompactify, _ := BuildCompactContext(s, 100000)
	if needCompactify {
		t.Fatal("expected needCompactify=false")
	}

	// Count compacted markers
	var markers []string
	for _, m := range result {
		if m.Role == RoleSystem && strings.Contains(m.Content, "Compacted") {
			markers = append(markers, m.Content)
		}
	}

	if len(markers) != 2 {
		t.Fatalf("expected 2 compacted markers, got %d: %v", len(markers), markers)
	}

	// First marker should mention "Reading project files"
	if !strings.Contains(markers[0], "Reading project files") {
		t.Fatalf("first marker should contain 'Reading project files', got %q", markers[0])
	}
	if strings.Contains(markers[0], "Writing implementation") {
		t.Fatalf("first marker should NOT contain 'Writing implementation', got %q", markers[0])
	}

	// Second marker should mention "Writing implementation"
	if !strings.Contains(markers[1], "Writing implementation") {
		t.Fatalf("second marker should contain 'Writing implementation', got %q", markers[1])
	}
	if strings.Contains(markers[1], "Reading project files") {
		t.Fatalf("second marker should NOT contain 'Reading project files', got %q", markers[1])
	}
}

// ---------------------------------------------------------------------------
// Bug: buildCompactedView inner loop skips compactify messages between two
// separate inRange spans, causing them to merge into ONE marker with combined
// summaries — instead of producing separate markers with distinct summaries.
// ---------------------------------------------------------------------------

func TestBuildCompactedView_CompactifyMessagesBetweenRangesDoNotMerge(t *testing.T) {
	// Scenario: Two separate inRange spans [1,2] and [5,6], separated ONLY
	// by compactify messages at indices 3-4 (no user/normal messages).
	//
	// This happens when the LLM calls compactify for the first tool round,
	// and then immediately (in the next assistant message) starts a new tool
	// round without user input.
	//
	// Raw messages:
	//   idx 0: User
	//   idx 1: Assistant (read_file)   ← inRange [1,2]
	//   idx 2: Tool (result)           ← inRange [1,2]
	//   idx 3: Assistant (compactify)  ← isCompactifyMessage (first compactify call)
	//   idx 4: Tool (compactify r.)    ← isCompactifyMessage
	//   idx 5: Assistant (shell)       ← inRange [5,6] (second tool round)
	//   idx 6: Tool (result)           ← inRange [5,6]
	//   idx 7: Assistant (compactify)  ← isCompactifyMessage (second compactify call)
	//   idx 8: Tool (compactify r.)    ← isCompactifyMessage
	//
	// BUG: The inner loop at i=1 skips indices 3-4 (compactify) and continues
	// into index 5 (inRange), merging [1,2] and [5,6] into ONE compacted marker.

	rawMsgs := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}},
		{Role: RoleTool, Content: "result1", ToolCallID: "c1", Name: "read_file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc1", Name: "context_compactify", Arguments: `{"range_start":1,"range_end":2,"summary":"Reading files"}`}}},
		{Role: RoleTool, Content: "Noted.", ToolCallID: "cc1", Name: "context_compactify"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "shell", Arguments: `{}`}}},
		{Role: RoleTool, Content: "result2", ToolCallID: "c2", Name: "shell"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc2", Name: "context_compactify", Arguments: `{"range_start":5,"range_end":6,"summary":"Executing shell"}`}}},
		{Role: RoleTool, Content: "Noted.", ToolCallID: "cc2", Name: "context_compactify"},
	}

	ranges := extractCompactifyRanges(rawMsgs)
	inRange := computeEffectiveInRange(rawMsgs, ranges)
	summaryAt := buildSummaryLookup(len(rawMsgs), ranges)

	view, _ := buildCompactedView(rawMsgs, inRange, summaryAt)

	// Should have 3 messages: user "task", marker1, marker2
	// (compactify messages filtered, two separate markers)
	if len(view) != 3 {
		t.Fatalf("expected 3 view messages (user + 2 markers), got %d: %+v", len(view), view)
	}

	if view[0].Content != "task" {
		t.Fatalf("view[0] = %q, want 'task'", view[0].Content)
	}

	// Count markers
	markerCount := 0
	for _, m := range view {
		if m.Role == RoleSystem && strings.Contains(m.Content, "Compacted") {
			markerCount++
		}
	}
	if markerCount != 2 {
		t.Fatalf("expected 2 markers, got %d. View: %+v", markerCount, view)
	}

	// First marker should have summary "Reading files"
	if !strings.Contains(view[1].Content, "Reading files") {
		t.Fatalf("view[1] (first marker) should contain 'Reading files', got %q", view[1].Content)
	}
	if strings.Contains(view[1].Content, "Executing shell") {
		t.Fatalf("view[1] (first marker) should NOT contain 'Executing shell', got %q", view[1].Content)
	}

	// Second marker should have summary "Executing shell"
	if !strings.Contains(view[2].Content, "Executing shell") {
		t.Fatalf("view[2] (second marker) should contain 'Executing shell', got %q", view[2].Content)
	}
	if strings.Contains(view[2].Content, "Reading files") {
		t.Fatalf("view[2] (second marker) should NOT contain 'Reading files', got %q", view[2].Content)
	}
}

// ---------------------------------------------------------------------------
// Same bug but at the BuildCompactContext integration level
// ---------------------------------------------------------------------------

func TestBuildCompactContext_TwoToolRoundsWithCompactifyBetween_NoUserMessage(t *testing.T) {
	s := NewSession("testns", "")

	// Round 1: user asks to read a file, assistant does it
	s.Append(Message{Role: RoleUser, Content: "read file"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.go"}`}}})
	s.Append(Message{Role: RoleTool, Content: "content", ToolCallID: "c1", Name: "read_file"})

	// Compactify round 1
	compactifyArgs1, _ := json.Marshal(struct {
		RangeStart int    `json:"range_start"`
		RangeEnd   int    `json:"range_end"`
		Summary    string `json:"summary,omitempty"`
	}{RangeStart: 1, RangeEnd: 2, Summary: "Reading files"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc1", Name: "context_compactify", Arguments: string(compactifyArgs1)}}})
	s.Append(Message{Role: RoleTool, Content: "Noted.", ToolCallID: "cc1", Name: "context_compactify"})

	// Round 2: assistant continues with shell (no user in between!)
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "shell", Arguments: `{"cmd":"ls"}`}}})
	s.Append(Message{Role: RoleTool, Content: "filelist", ToolCallID: "c2", Name: "shell"})

	// Compactify round 2
	compactifyArgs2, _ := json.Marshal(struct {
		RangeStart int    `json:"range_start"`
		RangeEnd   int    `json:"range_end"`
		Summary    string `json:"summary,omitempty"`
	}{RangeStart: 5, RangeEnd: 6, Summary: "Listing directory"})
	s.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "cc2", Name: "context_compactify", Arguments: string(compactifyArgs2)}}})
	s.Append(Message{Role: RoleTool, Content: "Noted.", ToolCallID: "cc2", Name: "context_compactify"})

	// Now build compact context
	result, needCompactify, _ := BuildCompactContext(s, 100000)
	if needCompactify {
		t.Fatal("expected needCompactify=false")
	}

	// Count compacted markers
	var markers []string
	for _, m := range result {
		if m.Role == RoleSystem && strings.Contains(m.Content, "Compacted") {
			markers = append(markers, m.Content)
		}
	}

	if len(markers) != 2 {
		t.Fatalf("expected 2 compacted markers, got %d: %v", len(markers), markers)
	}

	if !strings.Contains(markers[0], "Reading files") {
		t.Fatalf("marker[0] should contain 'Reading files', got %q", markers[0])
	}
	if strings.Contains(markers[0], "Listing directory") {
		t.Fatalf("marker[0] should NOT contain 'Listing directory', got %q", markers[0])
	}
	if !strings.Contains(markers[1], "Listing directory") {
		t.Fatalf("marker[1] should contain 'Listing directory', got %q", markers[1])
	}
	if strings.Contains(markers[1], "Reading files") {
		t.Fatalf("marker[1] should NOT contain 'Reading files', got %q", markers[1])
	}
}

package adapters

import (
	"encoding/json"
	"testing"

	"github.com/you/hakka/agent"
)

// ---------------------------------------------------------------------------
// Contract tests for toGemini() — converts hakka internal message history
// to Gemini API format (contents + system instruction).
//
// The Gemini API requires alternating user ↔ model roles. Consecutive
// messages of the same role must be merged into a single content entry
// with multiple parts.
// ---------------------------------------------------------------------------

func TestToGeminiConsecutiveToolResultsMerged(t *testing.T) {
	// Simulate: model issues two function calls, both return results.
	// Two consecutive RoleTool messages must merge into one user content
	// with two functionResponse parts.
	history := []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "get_weather", Arguments: `{"city":"Paris"}`},
			{ID: "2", Name: "get_time", Arguments: `{"tz":"UTC"}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "1", Name: "get_weather", Content: `{"temp":22}`},
		{Role: agent.RoleTool, ToolCallID: "2", Name: "get_time", Content: `{"time":"12:00"}`},
	}

	_, contents := toGemini(history)

	// Must produce exactly 2 contents: model + user (with merged results)
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents (model + user with 2 functionResponse parts), got %d", len(contents))
	}

	// Content 0: model with two functionCall parts
	if contents[0].Role != "model" {
		t.Errorf("contents[0].role = %q, want %q", contents[0].Role, "model")
	}
	if len(contents[0].Parts) != 2 {
		t.Fatalf("contents[0] (model) expected 2 parts, got %d", len(contents[0].Parts))
	}
	for i, name := range []string{"get_weather", "get_time"} {
		part := contents[0].Parts[i]
		if part.FunctionCall == nil {
			t.Fatalf("contents[0].parts[%d]: expected functionCall, got nil", i)
		}
		if part.FunctionCall.Name != name {
			t.Errorf("contents[0].parts[%d].functionCall.name = %q, want %q", i, part.FunctionCall.Name, name)
		}
	}

	// Content 1: user with two functionResponse parts
	if contents[1].Role != "user" {
		t.Errorf("contents[1].role = %q, want %q", contents[1].Role, "user")
	}
	if len(contents[1].Parts) != 2 {
		t.Fatalf("contents[1] (user) expected 2 parts, got %d", len(contents[1].Parts))
	}
	for i, name := range []string{"get_weather", "get_time"} {
		part := contents[1].Parts[i]
		if part.FunctionResponse == nil {
			t.Fatalf("contents[1].parts[%d]: expected functionResponse, got nil", i)
		}
		if part.FunctionResponse.Name != name {
			t.Errorf("contents[1].parts[%d].functionResponse.name = %q, want %q", i, part.FunctionResponse.Name, name)
		}
	}
}

func TestToGeminiToolResultAfterUserText(t *testing.T) {
	// A plain user text message followed by a tool result should produce
	// TWO separate user contents — they have different part types and
	// should not merge.
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "what's the weather?"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "get_weather", Arguments: `{}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "1", Name: "get_weather", Content: `{"temp":22}`},
	}

	_, contents := toGemini(history)

	// Expected structure:
	//   0: user (text part)
	//   1: model (functionCall part)
	//   2: user (functionResponse part)
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user-text, model, user-functionResponse), got %d", len(contents))
	}

	if contents[0].Role != "user" || contents[0].Parts[0].Text != "what's the weather?" {
		t.Error("contents[0] should be user with text part")
	}
	if contents[2].Role != "user" || contents[2].Parts[0].FunctionResponse == nil {
		t.Error("contents[2] should be user with functionResponse part")
	}
}

func TestToGeminiConsecutiveUserTextMerged(t *testing.T) {
	// Two consecutive user messages with text should merge into a single
	// user content with two text parts.
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "hello"},
		{Role: agent.RoleUser, Content: "world"},
	}

	_, contents := toGemini(history)

	if len(contents) != 1 {
		t.Fatalf("expected 1 merged user content, got %d", len(contents))
	}
	if contents[0].Role != "user" {
		t.Errorf("role = %q, want %q", contents[0].Role, "user")
	}
	if len(contents[0].Parts) != 2 {
		t.Fatalf("expected 2 text parts in merged content, got %d", len(contents[0].Parts))
	}
	for i, expected := range []string{"hello", "world"} {
		if contents[0].Parts[i].Text != expected {
			t.Errorf("contents[0].parts[%d].text = %q, want %q", i, contents[0].Parts[i].Text, expected)
		}
	}
}

func TestToGeminiConsecutiveModelMerged(t *testing.T) {
	// Two consecutive assistant messages (text + function calls) should
	// merge into a single model content with combined parts.
	history := []agent.Message{
		{Role: agent.RoleAssistant, Content: "let me check"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "search", Arguments: `{"q":"test"}`},
		}},
	}

	_, contents := toGemini(history)

	if len(contents) != 1 {
		t.Fatalf("expected 1 merged model content, got %d", len(contents))
	}
	if contents[0].Role != "model" {
		t.Errorf("role = %q, want %q", contents[0].Role, "model")
	}
	if len(contents[0].Parts) != 2 {
		t.Fatalf("expected 2 parts (text + functionCall), got %d", len(contents[0].Parts))
	}
	if contents[0].Parts[0].Text != "let me check" {
		t.Error("first part should be text")
	}
	if contents[0].Parts[1].FunctionCall == nil || contents[0].Parts[1].FunctionCall.Name != "search" {
		t.Error("second part should be functionCall named search")
	}
}

func TestToGeminiAlternatingRoles(t *testing.T) {
	// A full alternating conversation should be preserved as-is.
	history := []agent.Message{
		{Role: agent.RoleSystem, Content: "you are a bot"},
		{Role: agent.RoleUser, Content: "hi"},
		{Role: agent.RoleAssistant, Content: "hello"},
		{Role: agent.RoleUser, Content: "how are you?"},
		{Role: agent.RoleAssistant, Content: "i'm fine"},
	}

	sys, contents := toGemini(history)

	if sys == nil {
		t.Fatal("expected non-nil system instruction")
	}
	if len(sys.Parts) != 1 || sys.Parts[0].Text != "you are a bot" {
		t.Errorf("system instruction = %+v", sys)
	}
	if len(contents) != 4 {
		t.Fatalf("expected 4 contents, got %d", len(contents))
	}
	expectedRoles := []string{"user", "model", "user", "model"}
	for i, role := range expectedRoles {
		if contents[i].Role != role {
			t.Errorf("contents[%d].role = %q, want %q", i, contents[i].Role, role)
		}
	}
}

func TestToGeminiEmptyHistory(t *testing.T) {
	sys, contents := toGemini(nil)
	if sys != nil {
		t.Errorf("system = %+v, want nil", sys)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents, got %d", len(contents))
	}
}

func TestToGeminiMultipleSystemMessagesMerged(t *testing.T) {
	history := []agent.Message{
		{Role: agent.RoleSystem, Content: "first rule"},
		{Role: agent.RoleSystem, Content: "second rule"},
	}
	sys, contents := toGemini(history)
	if sys == nil || len(sys.Parts) != 2 {
		t.Fatalf("expected system instruction with 2 parts, got %+v", sys)
	}
	if sys.Parts[0].Text != "first rule" || sys.Parts[1].Text != "second rule" {
		t.Errorf("system parts: %+v", sys.Parts)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents (only system), got %d", len(contents))
	}
}

func TestToGeminiFunctionResponsePreservesNameAndData(t *testing.T) {
	// Verify that functionResponse parts carry the correct function name
	// and response data.
	history := []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "add", Arguments: `{"a":1,"b":2}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "1", Name: "add", Content: `{"sum":3}`},
	}

	_, contents := toGemini(history)

	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}
	// The functionResponse part
	fr := contents[1].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected functionResponse part, got nil")
	}
	if fr.Name != "add" {
		t.Errorf("functionResponse.name = %q, want %q", fr.Name, "add")
	}
	if fr.Response["sum"] != float64(3) {
		t.Errorf("functionResponse.response = %+v", fr.Response)
	}
}

func TestToGeminiNonJSONToolResultWrapped(t *testing.T) {
	// Non-JSON tool results should be wrapped in {"result": "..."}
	history := []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "search", Arguments: `{"q":"test"}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "1", Name: "search", Content: "found 5 results"},
	}

	_, contents := toGemini(history)

	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}
	fr := contents[1].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected functionResponse part, got nil")
	}
	if fr.Name != "search" {
		t.Errorf("functionResponse.name = %q, want %q", fr.Name, "search")
	}
	// Non-JSON result should be wrapped
	if fr.Response["result"] != "found 5 results" {
		t.Errorf("functionResponse.response = %+v", fr.Response)
	}
}

// TestToGeminiRoundTripJSON verifies that contents can marshal to valid JSON.
func TestToGeminiRoundTripJSON(t *testing.T) {
	history := []agent.Message{
		{Role: agent.RoleUser, Content: "compute"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "calc", Arguments: `{"expr":"1+1"}`},
		}},
		{Role: agent.RoleTool, ToolCallID: "1", Name: "calc", Content: `{"result":2}`},
	}

	_, contents := toGemini(history)
	raw, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	t.Logf("JSON output: %s", string(raw))

	// Unmarshal back and verify structural integrity
	var decoded []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text,omitempty"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal round-trip failed: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected 3 contents after round-trip, got %d", len(decoded))
	}
}

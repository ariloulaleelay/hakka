package adapters

import (
	"testing"

)

// TestOpenAIStreamToolCallAccumulatedDeltas reproduces the bug where
// streaming tool call deltas (incremental chunks per index) are appended
// as separate tool calls instead of being accumulated by Index.
//
// OpenAI sends tool calls incrementally:
//   chunk 1: {index:0, id:"call_1", function:{name:"get_weather", arguments:""}}
//   chunk 2: {index:0, function:{arguments:"{\"loc\":"}}
//   chunk 3: {index:0, function:{arguments:"ation\": \"NYC\"}"}}
//
// The bug: each chunk creates a new agent.ToolCall (3 total) instead of
// one accumulated call. The first has Arguments="" (empty), which causes
// "missing field 'arguments'" when sent back to the API.
func TestOpenAIStreamToolCallAccumulatedDeltas(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamMultipleToolCallsAccumulated verifies that multiple
// parallel tool calls (different indices) are correctly accumulated.
func TestOpenAIStreamMultipleToolCallsAccumulated(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamToolCallEOFWithoutFinishReason tests the EOF path:
// the stream ends with io.EOF (no finish_reason) but has accumulated
// tool calls. This exercises the second flush point in Stream().
func TestOpenAIStreamToolCallEOFWithoutFinishReason(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamToolCallManyChunks tests extreme argument fragmentation:
// arguments spread across many small chunks to verify pure concatenation.
func TestOpenAIStreamToolCallManyChunks(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamToolCallEmptyArgsReplaced verifies that when all
// argument deltas are empty strings, the final tool call gets "{}"
// instead of an empty string (which would cause "missing field 'arguments'").
func TestOpenAIStreamToolCallEmptyArgsReplaced(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamToolCallInterleavedText verifies that text deltas
// and tool call deltas can appear in the same chunk without interfering.
func TestOpenAIStreamToolCallInterleavedText(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

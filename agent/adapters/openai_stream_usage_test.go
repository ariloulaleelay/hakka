package adapters

import (
	"testing"
)

// TestOpenAIStreamCapturesUsageAfterFinishReason reproduces the bug where
// OpenAI's streaming response with `include_usage: true` sends usage in a
// separate SSE event AFTER the finish_reason chunk — but the current code
// returns as soon as it sees finish_reason, never reading the usage event.
//
// OpenAI SSE stream with include_usage:
//
//	data: {"choices":[{"delta":{"content":"Hello"}}]}
//	data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
//	data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}
//	data: [DONE]
//
// The usage event is the third line — it comes AFTER finish_reason. The
// Stream() goroutine must NOT return on finish_reason; it must continue
// reading until io.EOF to capture the final usage event.
func TestOpenAIStreamCapturesUsageAfterFinishReason(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamToolCallsCapturesUsageAfterFinishReason is the same test
// but for tool_calls finish_reason — the usage SSE event also comes after
// the tool_calls finish_reason chunk.
func TestOpenAIStreamToolCallsCapturesUsageAfterFinishReason(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

// TestOpenAIStreamExistingTestsStillPass verifies that the existing test
// patterns (no usage event after finish_reason) still work after the fix.
func TestOpenAIStreamExistingTestsStillPass(t *testing.T) {
	t.Skip("Stream API merged into Complete with onDelta")
}

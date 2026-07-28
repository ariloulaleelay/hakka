package adapters

import (
	"sort"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// Shared streaming helpers used by all LLM adapters.
//
// Each provider has a different wire protocol and event shape, but the
// core streaming loop is identical in structure:
//   1. Accumulate text deltas → emit Delta events
//   2. Accumulate tool calls (by index or by append)
//   3. Capture usage
//   4. On finish: emit ToolCalls or Done event
//
// The types below eliminate the duplicated accumulation logic that was
// previously copy-pasted across OpenAI, Anthropic, and Gemini.
// ---------------------------------------------------------------------------

// indexAccumulator merges streaming tool call deltas by index. This is used
// by providers (OpenAI, Anthropic) that stream tool calls incrementally —
// each chunk carries partial data (ID, name, arguments) for a specific
// index. We merge fragments by index rather than appending, so that a
// tool call split across N chunks produces exactly one agent.ToolCall.
type indexAccumulator struct {
	calls map[int]*agent.ToolCall
}

func newIndexAccumulator() *indexAccumulator {
	return &indexAccumulator{calls: make(map[int]*agent.ToolCall)}
}

// add merges a fragment into the tool call at the given index. If no entry
// exists yet at that index, one is created. Non-empty id, name, and args
// are merged: id and name overwrite (first non-empty wins in practice),
// arguments are concatenated.
func (a *indexAccumulator) add(index int, id, name, args string) {
	tc, exists := a.calls[index]
	if !exists {
		tc = &agent.ToolCall{}
		a.calls[index] = tc
	}
	if id != "" {
		tc.ID = id
	}
	if name != "" {
		tc.Name = name
	}
	tc.Arguments += args
}

// flush returns the accumulated tool calls sorted by index. Empty Arguments
// strings are normalised to "{}" so the LLM always receives valid JSON.
func (a *indexAccumulator) flush() []agent.ToolCall {
	if len(a.calls) == 0 {
		return nil
	}
	indices := make([]int, 0, len(a.calls))
	for i := range a.calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]agent.ToolCall, 0, len(indices))
	for _, i := range indices {
		tc := *a.calls[i]
		if tc.Arguments == "" {
			tc.Arguments = "{}"
		}
		out = append(out, tc)
	}
	return out
}

// appendAccumulator simply appends tool calls in the order they arrive.
// Used by providers (Gemini) that send complete tool calls per event
// rather than incremental deltas across multiple chunks.
type appendAccumulator struct {
	calls []agent.ToolCall
}

func newAppendAccumulator() *appendAccumulator {
	return &appendAccumulator{}
}

func (a *appendAccumulator) add(tc agent.ToolCall) {
	a.calls = append(a.calls, tc)
}

func (a *appendAccumulator) flush() []agent.ToolCall {
	for i := range a.calls {
		if a.calls[i].Arguments == "" {
			a.calls[i].Arguments = "{}"
		}
	}
	return a.calls
}

func sendStreamFinal(ch chan<- agent.StreamResult, toolCalls []agent.ToolCall, usage *agent.Usage, finishReason string) {
	if len(toolCalls) > 0 {
		ch <- agent.StreamResult{ToolCalls: toolCalls, Usage: usage, FinishReason: finishReason}
	} else {
		ch <- agent.StreamResult{Done: true, Usage: usage, FinishReason: finishReason}
	}
}

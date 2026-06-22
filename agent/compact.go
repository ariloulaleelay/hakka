package agent

import (
	"fmt"
	"sort"
	"strings"
)

// chainSpan describes a tool-call chain in terms of message indices.
// A chain is a sequence: assistant(tool_calls) → tool* → (assistant(tool_calls) → tool*)* → assistant(text).
// It starts at `start` (inclusive) and ends at `end` (exclusive).
// The last message in a complete chain is always an assistant text response.
type chainSpan struct {
	start     int
	end       int
	toolNames []string // sorted, deduplicated
}

// identifyChainSpans scans a message list and returns spans for every
// complete tool-call chain. Incomplete chains (those without a final
// assistant text) are NOT included.
func identifyChainSpans(messages []Message) []chainSpan {
	var spans []chainSpan
	i := 0
	for i < len(messages) {
		m := messages[i]
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			start := i
			toolSet := make(map[string]bool)
			for _, tc := range m.ToolCalls {
				toolSet[tc.Name] = true
			}
			i++

			// Walk through tool results and further tool-call rounds.
			complete := false
			for i < len(messages) {
				switch messages[i].Role {
				case RoleTool:
					i++
				case RoleAssistant:
					if len(messages[i].ToolCalls) > 0 {
						for _, tc := range messages[i].ToolCalls {
							toolSet[tc.Name] = true
						}
						i++
					} else {
						// Final assistant text — chain complete.
						i++
						complete = true
						// break out of inner loop
						goto chainFinished
					}
				default:
					// user/system message — chain incomplete, stop scanning
					goto chainFinished
				}
			}
		chainFinished:
			if complete {
				names := make([]string, 0, len(toolSet))
				for n := range toolSet {
					names = append(names, n)
				}
				sort.Strings(names)
				spans = append(spans, chainSpan{
					start:     start,
					end:       i,
					toolNames: names,
				})
			}
			// If !complete, i was already advanced past the chain boundary
			// by the goto — continue outer loop
		} else {
			i++
		}
	}
	return spans
}

// CompactMessages compacts old tool-call chains in the message list.
// It keeps the last `keep` complete chains intact and replaces older
// chains with a system message summarizing tool usage followed by
// the final assistant text of that chain.
//
// Non-chain messages (user, system, standalone assistant text) and
// incomplete chains are always preserved as-is.
//
// When keep >= number of chains, no compaction occurs.
func CompactMessages(messages []Message, keep int) []Message {
	spans := identifyChainSpans(messages)
	if len(spans) <= keep {
		return messages
	}

	compactCount := len(spans) - keep
	result := make([]Message, 0, len(messages))
	msgIdx := 0

	for si, sp := range spans {
		// Copy messages before this span.
		for msgIdx < sp.start {
			result = append(result, messages[msgIdx])
			msgIdx++
		}

		if si < compactCount {
			// Compact this chain: replace with system summary + final text.
			toolList := strings.Join(sp.toolNames, ", ")
			result = append(result, Message{
				Role:    RoleSystem,
				Content: fmt.Sprintf("[Called tools: %s]", toolList),
			})
			// The last message of a complete chain is the final assistant text.
			result = append(result, messages[sp.end-1])
			msgIdx = sp.end
		} else {
			// Keep this chain intact.
			for msgIdx < sp.end {
				result = append(result, messages[msgIdx])
				msgIdx++
			}
		}
	}

	// Copy any remaining messages after the last span.
	for msgIdx < len(messages) {
		result = append(result, messages[msgIdx])
		msgIdx++
	}

	return result
}

// CompactContext builds a context window with chain compaction.
// It combines the behavior of BuildContext (system prompt + CWD injection)
// with chain compaction on the conversation messages.
//
// keepChains controls how many complete chains to keep intact:
//   0 = compact all chains, keep none
//   N = keep the last N chains, compact older ones
//
// Callers that want the original uncompacted behavior should use
// BuildContext directly — CompactContext always compacts (even with
// keepChains=0, which compacts everything).
func CompactContext(session SessionHistory, keepChains int) []Message {
	// Compact the raw conversation messages.
	rawMsgs := session.AllMessages()
	compacted := CompactMessages(rawMsgs, keepChains)

	// Now prepend system prompt and CWD (same logic as BuildContext).
	history := session.History()
	// history[0] is the system prompt if one is set.
	result := make([]Message, 0, len(compacted)+2)
	if len(history) > 0 && history[0].Role == RoleSystem {
		result = append(result, history[0])
	}
	if cwdMsg := session.CWDMessage(); cwdMsg != nil {
		result = append(result, *cwdMsg)
	}
	result = append(result, compacted...)
	return result
}

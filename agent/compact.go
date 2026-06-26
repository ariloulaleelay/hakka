package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// compactRange describes an inclusive range of message indices to compact,
// along with an optional summary provided by the LLM.
type compactRange struct {
	from    int
	to      int
	summary string
}

// extractCompactifyRanges scans ALL raw messages for past context_compactify
// tool calls and returns the union of all compaction ranges requested by the LLM,
// including their optional summaries.
func extractCompactifyRanges(messages []Message) []compactRange {
	var ranges []compactRange
	for _, m := range messages {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.Name == "context_compactify" {
					var args struct {
						RangeStart int    `json:"range_start"`
						RangeEnd   int    `json:"range_end"`
						Summary    string `json:"summary,omitempty"`
					}
					if err := json.Unmarshal([]byte(tc.Arguments), &args); err == nil {
						// Filter out invalid ranges. The tool handler also validates,
						// but the call is still recorded even when the handler returns
						// an error — so we must guard against bogus ranges here too.
						if args.RangeStart >= 0 && args.RangeEnd >= args.RangeStart {
							ranges = append(ranges, compactRange{from: args.RangeStart, to: args.RangeEnd, summary: args.Summary})
						}
					}
				}
			}
		}
	}
	return ranges
}

// sortedKeys returns sorted keys from a string set.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isCompactifyMessage returns true if the message is a context_compactify
// tool call (assistant with only context_compactify tool calls) or a
// context_compactify tool result.
func isCompactifyMessage(m Message) bool {
	if m.Role == RoleTool && m.Name == "context_compactify" {
		return true
	}
	if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			if tc.Name != "context_compactify" {
				return false
			}
		}
		return true
	}
	return false
}

// stripCompactifyCalls removes context_compactify entries from a tool call
// list, returning a new slice. Used to clean mixed assistant messages where
// the LLM called compactify alongside regular tools.
func stripCompactifyCalls(calls []ToolCall) []ToolCall {
	filtered := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		if tc.Name != "context_compactify" {
			filtered = append(filtered, tc)
		}
	}
	return filtered
}

// computeEffectiveInRange marks messages that should be compacted, with
// the constraint that tool-call rounds must be compacted atomically:
// either all messages in a round are compacted, or none are.
// A round is: one assistant(tool_calls) + its matching tool results.
// User messages are NEVER compacted.
func computeEffectiveInRange(rawMsgs []Message, merged []compactRange) []bool {
	// Start with the raw ranges, excluding user messages.
	inRange := make([]bool, len(rawMsgs))
	for _, r := range merged {
		for i := r.from; i <= r.to && i < len(rawMsgs); i++ {
			if rawMsgs[i].Role != RoleUser {
				inRange[i] = true
			}
		}
	}

	// Walk through and enforce atomic rounds.
	// If any message in a round is in-range, the whole round must be in-range.
	// If any message in a round is out-of-range, the whole round must be out-of-range.
	// Tie-break: if a round is partially in-range (can't decide), keep it visible.
	for i := 0; i < len(rawMsgs); i++ {
		m := rawMsgs[i]
		if m.Role != RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		if isCompactifyMessage(m) {
			continue
		}

		// Find the end of this round: all consecutive tool messages
		// whose ToolCallID matches one of our tool calls.
		tcIDs := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				tcIDs[tc.ID] = true
			}
		}
		roundEnd := i + 1
		for roundEnd < len(rawMsgs) && rawMsgs[roundEnd].Role == RoleTool {
			if isCompactifyMessage(rawMsgs[roundEnd]) {
				roundEnd++
				continue
			}
			if tcIDs[rawMsgs[roundEnd].ToolCallID] {
				roundEnd++
			} else {
				break // tool result from a different round
			}
		}

		// Check if this round is partially in-range.
		hasIn := false
		hasOut := false
		for j := i; j < roundEnd; j++ {
			if isCompactifyMessage(rawMsgs[j]) {
				continue
			}
			if inRange[j] {
				hasIn = true
			} else {
				hasOut = true
			}
		}

		// If partially split, keep the whole round visible (no compaction).
		if hasIn && hasOut {
			for j := i; j < roundEnd; j++ {
				inRange[j] = false
			}
		}
		// If fully in-range or fully out-of-range, leave as-is.

		i = roundEnd - 1 // loop will increment
	}

	return inRange
}

// buildSummaryLookup creates a per-message summary lookup from merged ranges.
func buildSummaryLookup(numMsgs int, ranges []compactRange) []string {
	summaryAt := make([]string, numMsgs)
	for _, r := range ranges {
		for i := r.from; i <= r.to && i < numMsgs; i++ {
			if (summaryAt[i] == "") {
				summaryAt[i] = r.summary
			}
		}
	}
	return summaryAt
}

// buildCompactedView builds the compacted context view from raw messages.
// It filters out compactify messages, replaces compacted ranges
// with [Compacted N messages: ...] markers, and strips compactify calls from
// mixed assistant messages.
// Returns the view messages and a parallel slice mapping each view message
// to its original raw index (-1 for marker messages).
func buildCompactedView(rawMsgs []Message, inRange []bool, summaryAt []string) ([]Message, []int) {
	var view []Message
	var origIdx []int

	i := 0
	for i < len(rawMsgs) {
		if isCompactifyMessage(rawMsgs[i]) {
			i++
			continue
		}
		if inRange[i] {
			// Collect tool names and count messages in this compacted span.
			compactStart := i
			toolSet := make(map[string]bool)
			msgCount := 0
			for i < len(rawMsgs) && inRange[i] {
				if !isCompactifyMessage(rawMsgs[i]) {
					msgCount++
					for _, tc := range rawMsgs[i].ToolCalls {
						if tc.Name != "context_compactify" {
							toolSet[tc.Name] = true
						}
					}
					if rawMsgs[i].Role == RoleTool && rawMsgs[i].Name != "" && rawMsgs[i].Name != "context_compactify" {
						toolSet[rawMsgs[i].Name] = true
					}
				}
				i++
			}
			if msgCount > 0 {
				lastSummary := summaryAt[compactStart]
				markerText := ""
				if lastSummary != "" {
					markerText = fmt.Sprintf("[Compacted %d messages: %s]", msgCount, lastSummary)
				} else {
					toolList := sortedKeys(toolSet)
					markerText = fmt.Sprintf("[Compacted %d messages: %s]", msgCount, strings.Join(toolList, ", "))
				}
				view = append(view, Message{Role: RoleSystem, Content: markerText})
				origIdx = append(origIdx, -1)
			}
		} else {
			m := rawMsgs[i]
			// Strip context_compactify calls from mixed assistant messages.
			// Pure compactify messages were already filtered above, but
			// mixed messages (compactify + other tools) pass through and
			// must be cleaned so the LLM never sees context_compactify.
			if m.Role == RoleAssistant && !isCompactifyMessage(m) {
				filtered := stripCompactifyCalls(m.ToolCalls)
				if len(filtered) < len(m.ToolCalls) {
					m.ToolCalls = filtered
				}
			}
			view = append(view, m)
			origIdx = append(origIdx, i)
			i++
		}
	}
	return view, origIdx
}
// BuildCompactContext builds the compacted message list sent to the LLM.
//
// It scans ALL past context_compactify tool calls in the raw session,
// extracts and merges their compaction ranges, then builds a view where:
//   - context_compactify call/result messages are filtered out entirely
//   - compacted ranges are replaced with [Compacted N messages: ...] markers
//   - user messages and partial tool-call rounds are NEVER compacted
//
// After building the view, it estimates total tokens. If the estimate
// exceeds softLimit, it adds [N] index prefixes (raw session positions)
// to every message, appends a system warning prompting the LLM to
// call context_compactify, and persists the warning to session.
//
// Returns the context messages and a bool indicating whether
// context_compactify should be added to tool schemas for this turn.
func BuildCompactContext(session SessionHistory, softLimit int) ([]Message, bool, int) {
	rawMsgs := session.AllMessages()
	ranges := extractCompactifyRanges(rawMsgs)
	inRange := computeEffectiveInRange(rawMsgs, ranges)
	summaryAt := buildSummaryLookup(len(rawMsgs), ranges)
	view, origIdx := buildCompactedView(rawMsgs, inRange, summaryAt)

	estimatedTokens := estimateTokens(rawMsgs, inRange)
	needCompactify := estimatedTokens > softLimit

	if needCompactify {
		annotateViewWithIndices(view, origIdx)
		view = append(view, buildCompactionWarning(estimatedTokens, softLimit))
	}

	result := buildContextPrefix(session, needCompactify)
	result = append(result, view...)
	return result, needCompactify, estimatedTokens
}

// annotateViewWithIndices prepends [N] ~T tokens: index prefixes to
// each non-marker message in the view so the LLM can reference precise
// raw-message positions when calling context_compactify.
func annotateViewWithIndices(view []Message, origIdx []int) {
	for j := range view {
		if origIdx[j] >= 0 {
			tokens := messageTokens(view[j])
			view[j].Content = fmt.Sprintf("[%d] ~%d tokens:\n%s", origIdx[j], tokens, view[j].Content)
		}
	}
}

// buildCompactionWarning returns the system warning message that
// prompts the LLM to call context_compactify, showing the current
// estimated token usage vs the soft limit.
func buildCompactionWarning(estimatedTokens, softLimit int) Message {
	return Message{
		Role:    RoleUser,
		Content: fmt.Sprintf("STOP! Context exceeded ~%dK tokens (soft limit: %dK)! You MUST compact old history NOW using `context_compactify` before responding to the user. Range indices refer to [N] prefixes of each message. Decide what info you do not need anymore, and those ranges will be replaced with your summarization. Thus you can keep only importang info and drop boilerplate.", estimatedTokens/1000, softLimit/1000),
	}
}

// buildContextPrefix assembles the opening messages for every turn:
// system prompt (if present), optional compactify usage notice, and
// the working-directory message (if set).
func buildContextPrefix(session SessionHistory, needCompactify bool) []Message {
	history := session.History()
	result := make([]Message, 0, 4)

	if len(history) > 0 && history[0].Role == RoleSystem {
		result = append(result, history[0])
	}
	if needCompactify {
		result = append(result, Message{
			Role:    RoleSystem,
			Content: "You have access to `context_compactify` tool for compacting old conversation history when context gets too large. When warned about context quota, use it with range_start/range_end to compact unneeded tool-call rounds before continuing. Add meaningful summarization for compacted messages. You can call this tool multiple times to throw out obsolete and unneeded data.",
		})
	}
	if cwdMsg := session.CWDMessage(); cwdMsg != nil {
		result = append(result, *cwdMsg)
	}
	return result
}

// heuristicTokens returns a chars/4 token estimate for the given messages.
// Messages covered by any optional inRange mask are excluded (already
// compacted and not counted toward the current context size).
func heuristicTokens(msgs []Message, inRange ...[]bool) int {
	total := 0
	for i, m := range msgs {
		skip := false
		for _, rng := range inRange {
			if rng[i] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		total += messageTokens(m)
	}
	return total
}

// estimateTokens returns a token count for raw session messages using a
// text-based (chars/4) heuristic. Provider-reported Usage.PromptTokens
// is deliberately NOT used — it varies unpredictably across LLM backends
// and becomes stale after compaction, making the char-based heuristic
// more stable and provider-agnostic.
func estimateTokens(rawMsgs []Message, inRange ...[]bool) int {
	return heuristicTokens(rawMsgs, inRange...)
}

// messageTokens returns a rough token estimate for a single message
// using a chars/4 heuristic (content + tool call arguments).
// This is used only as a fallback when provider-reported usage is
// unavailable. Note: per-message Usage is NOT used here because
// Usage.PromptTokens describes the entire prompt, not one message.
func messageTokens(m Message) int {
	t := len(m.Content) / 4
	for _, tc := range m.ToolCalls {
		t += len(tc.Name) / 4
		t += len(tc.Arguments) / 4
	}
	return t
}

// contextCompactifySchema is the tool schema for the context_compactify
// meta-tool. It is conditionally added to schemas when the soft limit
// triggers, prompting the LLM to compact unneeded history.
var contextCompactifySchema = ToolSchema{
	Name:        "context_compactify",
	Description: "Compress message ranges to free context space. Use when the system warns about context quota. Ranges refer to [N] indices shown on each message.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"range_start": map[string]any{
				"type":        "integer",
				"description": "Start index of the message range to compact (inclusive, refers to [N] prefix)",
			},
			"range_end": map[string]any{
				"type":        "integer",
				"description": "End index of the message range to compact (inclusive, refers to [N] prefix)",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Optional one-line summary of what this range contains",
			},
		},
		"required": []any{"range_start", "range_end"},
	},
}

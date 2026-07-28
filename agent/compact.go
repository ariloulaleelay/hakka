package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ContextCompactifyToolName is the canonical name of the meta-tool used by
// the engine to request context compaction from the LLM. It is always
// registered in the tool registry but only added to schemas when the
// soft token limit is exceeded (see augmentSchemasWithCompactify).
const ContextCompactifyToolName = "context_compactify"


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
				if tc.Name == ContextCompactifyToolName {
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

// defaultShortUserMessageMaxLen is the character count threshold below
// which a user message is considered "trivial" and eligible for compaction.
// Short messages like "continue", "proceed", "ok" carry no substantive
// content and can be safely compressed alongside surrounding tool rounds,
// preventing context bloat from repeated compaction markers.
const defaultShortUserMessageMaxLen = 60

// isMessageCompressible reports whether an individual message can be
// compacted when it falls within a compaction range. This encapsulates
// all the per-message eligibility logic in one place.
//
//   - Non-user messages (assistant, tool, system) are always compressible.
//   - User messages are only compressible if they are short/trivial
//     (trimmed length ≤ defaultShortUserMessageMaxLen). Long user messages
//     carry substantive content that must survive compaction.
func isMessageCompressible(m Message) bool {
	if m.Role != RoleUser {
		return true
	}
	trimmed := strings.TrimSpace(m.Content)
	return len(trimmed) <= defaultShortUserMessageMaxLen
}

// isCompactifyMessage returns true if the message is a context_compactify
// tool call (assistant with only context_compactify tool calls) or a
// context_compactify tool result.
func isCompactifyMessage(m Message) bool {
	if m.Role == RoleTool && m.Name == ContextCompactifyToolName {
		return true
	}
	if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			if tc.Name != ContextCompactifyToolName {
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
		if tc.Name != ContextCompactifyToolName {
			filtered = append(filtered, tc)
		}
	}
	return filtered
}

// computeEffectiveInRange marks messages that should be compacted, with
// the constraint that tool-call rounds must be compacted atomically:
// either all messages in a round are compacted, or none are.
// A round is: one assistant(tool_calls) + its matching tool results.
//
// Message eligibility is delegated to isMessageCompressible:
//   - Long/substantive user messages are NEVER compacted.
//   - Short/trivial user messages (≤ defaultShortUserMessageMaxLen chars)
//     CAN be compacted alongside surrounding tool rounds, preventing
//     context bloat from repeated identical compaction markers.
//   - All non-user messages are always compressible.
func computeEffectiveInRange(rawMsgs []Message, merged []compactRange) []bool {
	// Start with the raw ranges, excluding messages that aren't compressible.
	inRange := make([]bool, len(rawMsgs))
	for _, r := range merged {
		for i := r.from; i <= r.to && i < len(rawMsgs); i++ {
			if isMessageCompressible(rawMsgs[i]) {
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
// It filters out compactify messages, replaces compacted ranges with
// [Compacted N messages: ...] markers, and strips compactify calls from
// mixed assistant messages.
//
// When compaction ranges are split by non-compressible messages (e.g. long
// user messages), only the FIRST marker carries the user-provided summary.
// Subsequent markers from the same split range fall back to listing tool
// names, preventing context bloat from repeated identical summary text.
//
// Returns the view messages and a parallel slice mapping each view message
// to its original raw index (-1 for marker messages).
//
// The three sub-operations — span collection, marker text, and message
// pass-through — are extracted into their own functions so each can be
// reasoned about and tested independently.
func buildCompactedView(rawMsgs []Message, inRange []bool, summaryAt []string) ([]Message, []int) {
	var view []Message
	var origIdx []int
	usedSummaries := make(map[string]bool)

	for i := 0; i < len(rawMsgs); {
		if isCompactifyMessage(rawMsgs[i]) {
			i++
			continue
		}
		if inRange[i] {
			marker, next := collectCompactedSpan(rawMsgs, inRange, summaryAt, i, usedSummaries)
			if marker.Role != "" {
				view = append(view, marker)
				origIdx = append(origIdx, -1)
			}
			i = next
		} else {
			view = append(view, passThroughMessage(rawMsgs[i]))
			origIdx = append(origIdx, i)
			i++
		}
	}
	return view, origIdx
}

// collectCompactedSpan reads consecutive inRange messages starting at i,
// counts real messages and gathers distinct tool names, then returns a
// compaction marker message and the index of the first message after the
// span. Returns a zero-value marker when the span contains no real messages.
//
// Compactify messages inside the span are skipped — this is what prevents
// separate compaction ranges (separated only by compactify call/results)
// from merging into a single marker.
//
// usedSummaries tracks which user-provided summaries have already appeared
// in previous markers. When a summary has already been emitted, the marker
// falls back to listing tool names — this prevents the same summary text
// from being repeated across split ranges (e.g. when a long user message
// forces a compaction range to split into multiple markers).
func collectCompactedSpan(rawMsgs []Message, inRange []bool, summaryAt []string, i int, usedSummaries map[string]bool) (Message, int) {
	start := i
	toolSet := make(map[string]bool)
	msgCount := 0

	for i < len(rawMsgs) && inRange[i] {
		if isCompactifyMessage(rawMsgs[i]) {
			i++
			continue
		}
		msgCount++
		for _, tc := range rawMsgs[i].ToolCalls {
			if tc.Name != ContextCompactifyToolName {
				toolSet[tc.Name] = true
			}
		}
		if rawMsgs[i].Role == RoleTool && rawMsgs[i].Name != "" && rawMsgs[i].Name != ContextCompactifyToolName {
			toolSet[rawMsgs[i].Name] = true
		}
		i++
	}
	if msgCount == 0 {
		return Message{}, i
	}
	summary := summaryAt[start]
	if summary != "" && usedSummaries[summary] {
		summary = "" // already used in a previous marker — fall back to tool names
	}
	if summary != "" {
		usedSummaries[summary] = true
	}
	return Message{Role: RoleSystem, Content: markerText(msgCount, toolSet, summary)}, i
}

// markerText formats a compaction marker. When summary is non-empty it is
// used verbatim; otherwise tool names are listed alphabetically.
func markerText(count int, toolSet map[string]bool, summary string) string {
	if summary != "" {
		return fmt.Sprintf("[Compacted %d messages: %s]", count, summary)
	}
	return fmt.Sprintf("[Compacted %d messages: %s]", count, strings.Join(sortedKeys(toolSet), ", "))
}

// passThroughMessage returns a copy of m suitable for the compacted view.
// Mixed assistant messages (compactify + regular tool calls) have their
// compactify calls stripped — the LLM must never see context_compactify.
func passThroughMessage(m Message) Message {
	if m.Role == RoleAssistant && !isCompactifyMessage(m) {
		if filtered := stripCompactifyCalls(m.ToolCalls); len(filtered) < len(m.ToolCalls) {
			m.ToolCalls = filtered
		}
	}
	return m
}
// BuildCompactContext builds the compacted message list sent to the LLM.
//
// It scans ALL past context_compactify tool calls in the raw session,
// extracts and merges their compaction ranges, then builds a view where:
//   - context_compactify call/result messages are filtered out entirely
//   - compacted ranges are replaced with [Compacted N messages: ...] markers
//   - long/substantive user messages (len > defaultShortUserMessageMaxLen)
//     and partial tool-call rounds are NEVER compacted
//   - short/trivial user messages (len ≤ defaultShortUserMessageMaxLen)
//     CAN be compacted when they fall within a compaction range
//
// After building the view, it estimates total tokens. If the estimate
// exceeds softLimit, it adds [N] index prefixes (raw session positions)
// to every message, appends a system warning prompting the LLM to
// call context_compactify, and persists the warning to session.
//
// Returns the context messages and a bool indicating whether
// context_compactify should be added to tool schemas for this turn.
func BuildCompactContext(session SessionHistory, softLimit int, skills *SkillRegistry) ([]Message, bool, int) {
	rawMsgs := session.Messages()
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

	result := buildContextPrefix(session, needCompactify, skills, session.GetCWD())
	result = append(result, view...)
	return result, needCompactify, estimatedTokens
}

// buildSkillMessages assembles system messages for loaded skills.
func buildSkillMessages(session SessionHistory, skills *SkillRegistry) []Message {
	if skills == nil {
		return nil
	}
	// Try to get active skills — SessionSkills interface
	skillSession, ok := session.(SessionSkills)
	if !ok {
		return nil
	}
	names := skillSession.ActiveSkills()
	if len(names) == 0 {
		return nil
	}
	var msgs []Message
	for _, name := range names {
		skill := skills.Get(name)
		if skill == nil {
			continue
		}
		content, err := skills.ReadContent(name)
		if err != nil {
			continue
		}
		msgs = append(msgs, Message{
			Role:    RoleSystem,
			Content: "## Skill: " + name + "\n\n" + content,
		})
	}
	return msgs
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
		Content: fmt.Sprintf("STOP! Context at ~%dK tokens (limit %dK). Archive finished tool rounds with `context_compactify`. Look for completed operations, old file reads, or resolved multi-step tasks that no longer inform the current goal. Each [N] is a message index — keep recent exchanges, compact the rest. You can also free context by unloading unnecessary skills with `unload_skill`.", estimatedTokens/1000, softLimit/1000),
	}
}

// buildContextPrefix assembles the opening messages for every turn:
// system prompt (if present), AGENTS.md from CWD (if found),
// optional compactify usage notice, loaded skills, and
// the working-directory message (if set).
func buildContextPrefix(session SessionHistory, needCompactify bool, skills *SkillRegistry, cwd string) []Message {
	result := make([]Message, 0, 8)

	if sp := session.SystemPrompt(); sp != "" {
		result = append(result, Message{Role: RoleSystem, Content: sp})
	}
	if agentsMsg := buildAgentMarkdownMessage(cwd); agentsMsg != nil {
		result = append(result, *agentsMsg)
	}
	if needCompactify {
		result = append(result, Message{
			Role:    RoleSystem,
			Content: "`context_compactify` frees context by replacing old [N..M] message ranges with a summary marker. Use when the warning appears. `unload_skill` removes loaded skills from your system prompt to free context.",
		})
	}
	// Inject loaded skills as system messages
	if skillMsgs := buildSkillMessages(session, skills); len(skillMsgs) > 0 {
		result = append(result, skillMsgs...)
	}
	if cwdMsg := session.CWDMessage(); cwdMsg != nil {
		result = append(result, *cwdMsg)
	}
	return result
}

// agentMarkdownCandidates lists the file paths to search for project
// instructions in the session's working directory, in priority order.
var agentMarkdownCandidates = []string{
	"AGENTS.md",
	"AGENT.md",
	".claude/AGENTS.md",
}

// buildAgentMarkdownMessage reads an AGENTS.md/AGENT.md file from the
// session's working directory if one exists. Returns nil when no file
// is found or cwd is empty — silent fallback.
func buildAgentMarkdownMessage(cwd string) *Message {
	if cwd == "" {
		return nil
	}
	for _, relPath := range agentMarkdownCandidates {
		path := filepath.Join(cwd, relPath)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return &Message{
			Role:    RoleSystem,
			Content: "## Project instructions (" + relPath + ")\n\n" + string(data),
		}
	}
	return nil
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
	Name:        ContextCompactifyToolName,
	Description: "Compress [range_start, range_end] message range using [N] indexes to free context. Provide a summary of what was compacted.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"range_start": map[string]any{
				"type":        "integer",
				"description": "Start of message range (inclusive, refers to [N] index)",
			},
			"range_end": map[string]any{
				"type":        "integer",
				"description": "End of message range (inclusive, refers to [N] index)",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Summary of what the compacted range contained",
			},
		},
		"required": []any{"range_start", "range_end"},
	},
}

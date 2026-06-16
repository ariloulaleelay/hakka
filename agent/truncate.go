package agent

import "unicode/utf8"

// Truncate returns a prefix of s up to maxLen bytes, appending "..." if
// truncated. The result is always valid UTF-8 and its byte length never
// exceeds maxLen.
//
// Invariants:
//   - utf8.ValidString(result)
//   - len(result) <= maxLen
//   - If len(s) <= maxLen: result == s (identity, no allocation)
//   - If len(s) > maxLen and maxLen > 3: result ends with "..."
//   - If len(s) > maxLen and maxLen <= 3: no suffix (too small to fit)
//
// Never panics: negative maxLen is treated as 0.
func Truncate(s string, maxLen int) string {
	if maxLen < 0 {
		maxLen = 0
	}
	if len(s) <= maxLen {
		return s
	}

	// Compute the byte limit for the prefix (reserve 3 bytes for "...").
	limit := maxLen
	if limit > 3 {
		limit = maxLen - 3
	}

	// Trim back to a valid UTF-8 boundary. We can't just check
	// utf8.RuneStart because a lead byte (e.g. 0xe6) is a rune start
	// but is NOT valid UTF-8 without its continuation bytes.
	for limit > 0 && !utf8.ValidString(s[:limit]) {
		limit--
	}

	if maxLen <= 3 {
		return s[:limit]
	}
	return s[:limit] + "..."
}

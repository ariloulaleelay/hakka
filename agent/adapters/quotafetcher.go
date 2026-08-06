package adapters

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// extractJSONPath navigates a nested JSON structure (map[string]any / []any)
// using a simple dot+bracket path expression. Returns the value and true if
// found, or nil and false if any segment is missing or out of bounds.
//
// Supported syntax:
//
//	.field
//	.field.subfield
//	.array[0].field        — numeric index
//	.array[currency=USD].field  — filter: find object where field==value
//
// Leading dot is required.
func extractJSONPath(root any, path string) (any, bool) {
	if path == "" || path == "." {
		return nil, false
	}
	// Strip leading dot.
	if path[0] == '.' {
		path = path[1:]
	}
	if path == "" {
		return nil, false
	}

	current := root
	for _, segment := range splitPath(path) {
		if segment == "" {
			return nil, false
		}
		field, idx, hasIdx, filterField, filterValue := parseSegment(segment)

		if field != "" {
			m, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[field]
			if !ok {
				return nil, false
			}
			current = v
		}

		if hasIdx {
			arr, ok := current.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			current = arr[idx]
		}

		if filterField != "" {
			arr, ok := current.([]any)
			if !ok {
				return nil, false
			}
			found := false
			for _, elem := range arr {
				m, ok := elem.(map[string]any)
				if !ok {
					continue
				}
				if v, ok := m[filterField]; ok && fmt.Sprintf("%v", v) == filterValue {
					current = elem
					found = true
					break
				}
			}
			if !found {
				return nil, false
			}
		}
	}
	return current, true
}

// splitPath splits a dot-separated path like "balance_infos[0].total_balance"
// into segments: ["balance_infos[0]", "total_balance"].
func splitPath(path string) []string {
	return strings.Split(path, ".")
}

// parseSegment parses a path segment that may include an array index or
// filter. Returns:
//
//	"balance_infos[0]"          → field="balance_infos", idx=0, hasIdx=true
//	"balance_infos[currency=USD]" → filterField="currency", filterValue="USD"
//	"total_balance"             → field="total_balance"
func parseSegment(segment string) (field string, idx int, hasIdx bool, filterField, filterValue string) {
	bracket := strings.IndexByte(segment, '[')
	if bracket < 0 {
		return segment, 0, false, "", ""
	}
	field = segment[:bracket]
	rest := segment[bracket:]
	if len(rest) < 3 || rest[len(rest)-1] != ']' {
		return segment, 0, false, "", ""
	}
	inner := rest[1 : len(rest)-1]

	// Try numeric index: [0], [5], etc.
	if n, err := strconv.Atoi(inner); err == nil {
		return field, n, true, "", ""
	}

	// Try filter: [field=value]
	if eq := strings.IndexByte(inner, '='); eq > 0 {
		return field, 0, false, inner[:eq], inner[eq+1:]
	}

	// Unrecognized bracket content — treat as field with no accessor.
	return segment, 0, false, "", ""
}

// extractJSONPathFloat is like extractJSONPath but converts the result to
// float64. Handles JSON numbers (float64), strings that look like numbers
// (e.g. "110.00"), and integers.
func extractJSONPathFloat(root any, path string) (float64, bool) {
	v, ok := extractJSONPath(root, path)
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case json.Number:
		f, err := val.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := parseNumeric(val)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// extractJSONPathString is like extractJSONPath but converts the result to
// string.
func extractJSONPathString(root any, path string) (string, bool) {
	v, ok := extractJSONPath(root, path)
	if !ok {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return val, true
	default:
		return fmt.Sprintf("%v", val), true
	}
}

// parseNumeric parses a numeric string like "110.00" into a float64.
func parseNumeric(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// isPathExpression returns true if s looks like a JSON path (contains dots
// or brackets), as opposed to a static literal like "CNY".
func isPathExpression(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == '[' {
			return true
		}
	}
	return false
}

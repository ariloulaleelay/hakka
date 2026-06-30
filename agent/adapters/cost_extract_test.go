package adapters

import (
	"encoding/json"
	"testing"
)

func TestExtractCostFromUsage(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected float64
	}{
		{
			name: "openai complete response with cost",
			json: `{
				"choices": [{"message": {"role": "assistant", "content": "hello"}}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30, "cost": 0.0000950625}
			}`,
			expected: 0.0000950625,
		},
		{
			name: "openai stream chunk with cost",
			json: `{
				"id": "gen-123",
				"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 7, "completion_tokens": 59, "total_tokens": 66, "cost": 0.0000950625}
			}`,
			expected: 0.0000950625,
		},
		{
			name: "no cost field",
			json: `{
				"choices": [{"message": {"role": "assistant", "content": "hi"}}],
				"usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}
			}`,
			expected: 0,
		},
		{
			name: "no usage at all",
			json: `{
				"choices": [{"message": {"role": "assistant", "content": "hi"}}]
			}`,
			expected: 0,
		},
		{
			name:     "empty body",
			json:     `{}`,
			expected: 0,
		},
		{
			name: "zero cost",
			json: `{
				"usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0, "cost": 0}
			}`,
			expected: 0,
		},
		{
			name: "large cost",
			json: `{
				"usage": {"prompt_tokens": 1000, "completion_tokens": 2000, "total_tokens": 3000, "cost": 1.5}
			}`,
			expected: 1.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCostFromUsage([]byte(tt.json))
			if got != tt.expected {
				t.Errorf("extractCostFromUsage() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtractCostFromUsage_UsedInUsageStruct(t *testing.T) {
	// Verify the helper works with Usage struct marshaling
	type usageWithCost struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		Cost             float64 `json:"cost"`
	}
	type response struct {
		Usage usageWithCost `json:"usage"`
	}

	raw := `{"usage":{"prompt_tokens":7,"completion_tokens":59,"total_tokens":66,"cost":0.0000950625}}`
	var resp response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := extractCostFromUsage([]byte(raw))
	if got != resp.Usage.Cost {
		t.Errorf("extractCostFromUsage() = %v, expected %v from Usage struct", got, resp.Usage.Cost)
	}
}

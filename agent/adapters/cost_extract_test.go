package adapters

import (
	"encoding/json"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
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

func TestExtractRawUsage_DeepSeekCacheTokens(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		wantCost   float64
		wantHit    int
		wantMiss   int
	}{
		{
			name: "deepseek with cache breakdown",
			json: `{
				"usage": {
					"prompt_tokens": 500,
					"completion_tokens": 100,
					"total_tokens": 600,
					"prompt_cache_hit_tokens": 300,
					"prompt_cache_miss_tokens": 200
				}
			}`,
			wantCost: 0,
			wantHit:  300,
			wantMiss: 200,
		},
		{
			name: "no cache fields",
			json: `{
				"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
			}`,
			wantCost: 0,
			wantHit:  0,
			wantMiss: 0,
		},
		{
			name: "cache only hit",
			json: `{
				"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "prompt_cache_hit_tokens": 100}
			}`,
			wantCost: 0,
			wantHit:  100,
			wantMiss: 0,
		},
		{
			name:     "empty body",
			json:     `{}`,
			wantCost: 0,
			wantHit:  0,
			wantMiss: 0,
		},
		{
			name: "with cost and cache",
			json: `{
				"usage": {
					"prompt_tokens": 500,
					"completion_tokens": 100,
					"total_tokens": 600,
					"cost": 0.0002,
					"prompt_cache_hit_tokens": 300,
					"prompt_cache_miss_tokens": 200
				}
			}`,
			wantCost: 0.0002,
			wantHit:  300,
			wantMiss: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRawUsage([]byte(tt.json))
			if got.cost != tt.wantCost {
				t.Errorf("cost = %v, want %v", got.cost, tt.wantCost)
			}
			if got.promptCacheHit != tt.wantHit {
				t.Errorf("promptCacheHit = %v, want %v", got.promptCacheHit, tt.wantHit)
			}
			if got.promptCacheMiss != tt.wantMiss {
				t.Errorf("promptCacheMiss = %v, want %v", got.promptCacheMiss, tt.wantMiss)
			}
		})
	}
}

func TestCalculateCostFromPricing(t *testing.T) {
	tests := []struct {
		name      string
		pricing   agent.Pricing
		usage     RawUsage
		wantCost  float64
	}{
		{
			name: "no cache breakdown - basic pricing",
			pricing: agent.Pricing{
				Input:  0.000003,
				Output: 0.000015,
			},
			usage: RawUsage{
				promptTokens:     1000,
				completionTokens: 500,
			},
			wantCost: 1000*0.000003 + 500*0.000015, // 0.003 + 0.0075 = 0.0105
		},
		{
			name: "with cache breakdown - deepseek style",
			pricing: agent.Pricing{
				Input:         0.00000027,
				Output:        0.00000110,
				CacheHitInput: 0.00000007,
			},
			usage: RawUsage{
				promptTokens:     500,
				completionTokens: 100,
				promptCacheHit:   300,
				promptCacheMiss:  200,
			},
			wantCost: 200*0.00000027 + 300*0.00000007 + 100*0.00000110,
			// 0.000054 + 0.000021 + 0.000110 = 0.000185
		},
		{
			name: "pricing not configured (zero)",
			pricing: agent.Pricing{},
			usage: RawUsage{
				promptTokens:     100,
				completionTokens: 50,
			},
			wantCost: 0,
		},
		{
			name: "only input and output - cache fields ignored",
			pricing: agent.Pricing{
				Input:  0.000001,
				Output: 0.000002,
			},
			usage: RawUsage{
				promptTokens:     1000,
				completionTokens: 2000,
			},
			wantCost: 1000*0.000001 + 2000*0.000002, // 0.001 + 0.004 = 0.005
		},
		{
			name: "zero tokens",
			pricing: agent.Pricing{
				Input:  0.000003,
				Output: 0.000015,
			},
			usage: RawUsage{
				promptTokens:     0,
				completionTokens: 0,
			},
			wantCost: 0,
		},
		{
			name: "only prompt tokens",
			pricing: agent.Pricing{
				Input:  0.000001,
				Output: 0.000002,
			},
			usage: RawUsage{
				promptTokens:     500,
				completionTokens: 0,
			},
			wantCost: 500 * 0.000001, // 0.0005
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateCostFromPricing(tt.pricing, tt.usage)
			if abs(got-tt.wantCost) > 1e-12 {
				t.Errorf("calculateCostFromPricing() = %v, want %v", got, tt.wantCost)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestPricingCalculator_DeepSeekExample(t *testing.T) {
	// Real-world example: DeepSeek pricing per million tokens (converted to per-token)
	// Input: $0.27/M, Cache Hit: $0.07/M, Output: $1.10/M
	pricing := agent.Pricing{
		Input:         0.00000027,
		Output:        0.00000110,
		CacheHitInput: 0.00000007,
	}

	usage := RawUsage{
		promptTokens:     500,
		completionTokens: 100,
		promptCacheHit:   300,
		promptCacheMiss:  200,
	}

	cost := calculateCostFromPricing(pricing, usage)

	expected := 200*0.00000027 + 300*0.00000007 + 100*0.00000110
	if abs(cost-expected) > 1e-12 {
		t.Errorf("DeepSeek cost = %v (%.10f), want %v (%.10f)", cost, cost, expected, expected)
	}
}

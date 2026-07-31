package adapters

import (
	"encoding/json"
	"log/slog"

	"github.com/ariloulaleelay/hakka/agent"
)

// RawUsage carries raw token and cost fields extracted from a provider response.
type RawUsage struct {
	cost             float64
	promptTokens     int
	completionTokens int
	totalTokens      int
	promptCacheHit   int
	promptCacheMiss  int
}

func extractCostFromUsage(body []byte) float64 {
	return extractRawUsage(body).cost
}

func extractRawUsage(body []byte) RawUsage {
	var resp struct {
		Usage struct {
			Cost             float64 `json:"cost"`
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			TotalTokens      int     `json:"total_tokens"`
			PromptCacheHit   int     `json:"prompt_cache_hit_tokens"`
			PromptCacheMiss  int     `json:"prompt_cache_miss_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return RawUsage{}
	}
	return RawUsage{
		cost:             resp.Usage.Cost,
		promptTokens:     resp.Usage.PromptTokens,
		completionTokens: resp.Usage.CompletionTokens,
		totalTokens:      resp.Usage.TotalTokens,
		promptCacheHit:   resp.Usage.PromptCacheHit,
		promptCacheMiss:  resp.Usage.PromptCacheMiss,
	}
}

// calculateCostFromPricing computes monetary cost from token usage.
// When cache breakdown is available, cached tokens are priced at CacheHitInput,
// uncached at Input. Otherwise all prompt tokens use Input.
func calculateCostFromPricing(pricing agent.Pricing, raw RawUsage) float64 {
	if pricing.Input == 0 && pricing.Output == 0 {
		return 0
	}
	var inputCost float64
	if raw.promptCacheHit+raw.promptCacheMiss == raw.promptTokens && raw.promptCacheMiss > 0 {
		inputCost = float64(raw.promptCacheMiss)*pricing.Input + float64(raw.promptCacheHit)*pricing.CacheHitInput
	} else {
		inputCost = float64(raw.promptTokens) * pricing.Input
	}
	outputCost := float64(raw.completionTokens) * pricing.Output
	total := inputCost + outputCost
	slog.Debug("pricing_calculate: total",
		"input_cost", inputCost, "output_cost", outputCost, "total", total)
	return total
}

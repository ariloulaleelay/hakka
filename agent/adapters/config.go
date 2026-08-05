package adapters

import (
	"net/http"

	"github.com/ariloulaleelay/hakka/agent"
)

// AdapterConfig is the common surface every adapter config must expose.
type AdapterConfig interface {
	DebugDir() string
	RetryPolicy() agent.RetryConfig
	ResolveCost(raw RawUsage) float64
}

// OpenAIConfig holds configuration for the OpenAI adapter.
type OpenAIConfig struct {
	debugDir   string
	retryCfg   agent.RetryConfig
	pricing    agent.Pricing
	Extra      map[string]any
	Quota      *agent.QuotaConfig
	HTTPClient *http.Client // for quota fetching
}

func NewOpenAIConfig(debugDir string, retryCfg agent.RetryConfig, pricing agent.Pricing, extra map[string]any) OpenAIConfig {
	return OpenAIConfig{debugDir: debugDir, retryCfg: retryCfg, pricing: pricing, Extra: extra}
}

func (c OpenAIConfig) DebugDir() string               { return c.debugDir }
func (c OpenAIConfig) RetryPolicy() agent.RetryConfig { return c.retryCfg }
func (c OpenAIConfig) ResolveCost(raw RawUsage) float64 {
	if raw.cost > 0 {
		return raw.cost
	}
	if c.pricing.Input != 0 || c.pricing.Output != 0 {
		return calculateCostFromPricing(c.pricing, raw)
	}
	return 0
}

// AnthropicConfig holds configuration for the Anthropic adapter.
type AnthropicConfig struct {
	debugDir  string
	retryCfg  agent.RetryConfig
	pricing   agent.Pricing
	Version   string
	MaxTokens int
	Quota     *agent.QuotaConfig
}

func NewAnthropicConfig(debugDir string, retryCfg agent.RetryConfig, pricing agent.Pricing, version string, maxTokens int) AnthropicConfig {
	return AnthropicConfig{debugDir: debugDir, retryCfg: retryCfg, pricing: pricing, Version: version, MaxTokens: maxTokens}
}

func (c AnthropicConfig) DebugDir() string               { return c.debugDir }
func (c AnthropicConfig) RetryPolicy() agent.RetryConfig { return c.retryCfg }
func (c AnthropicConfig) ResolveCost(raw RawUsage) float64 {
	if raw.cost > 0 {
		return raw.cost
	}
	if c.pricing.Input != 0 || c.pricing.Output != 0 {
		return calculateCostFromPricing(c.pricing, raw)
	}
	return 0
}

// GeminiConfig holds configuration for the Gemini adapter.
type GeminiConfig struct {
	debugDir string
	retryCfg agent.RetryConfig
	pricing  agent.Pricing
	Quota    *agent.QuotaConfig
}

func NewGeminiConfig(debugDir string, retryCfg agent.RetryConfig, pricing agent.Pricing) GeminiConfig {
	return GeminiConfig{debugDir: debugDir, retryCfg: retryCfg, pricing: pricing}
}

func (c GeminiConfig) DebugDir() string               { return c.debugDir }
func (c GeminiConfig) RetryPolicy() agent.RetryConfig { return c.retryCfg }
func (c GeminiConfig) ResolveCost(raw RawUsage) float64 {
	if raw.cost > 0 {
		return raw.cost
	}
	if c.pricing.Input != 0 || c.pricing.Output != 0 {
		return calculateCostFromPricing(c.pricing, raw)
	}
	return 0
}

// DeepSeekConfig holds configuration for the DeepSeek adapter.
type DeepSeekConfig struct {
	debugDir string
	retryCfg agent.RetryConfig
	pricing  agent.Pricing
	Extra    map[string]any
	Quota    *agent.QuotaConfig
}

func NewDeepSeekConfig(debugDir string, retryCfg agent.RetryConfig, pricing agent.Pricing, extra ...map[string]any) DeepSeekConfig {
	var e map[string]any
	if len(extra) > 0 {
		e = extra[0]
	}
	return DeepSeekConfig{debugDir: debugDir, retryCfg: retryCfg, pricing: pricing, Extra: e}
}

func (c DeepSeekConfig) DebugDir() string               { return c.debugDir }
func (c DeepSeekConfig) RetryPolicy() agent.RetryConfig { return c.retryCfg }
func (c DeepSeekConfig) ResolveCost(raw RawUsage) float64 {
	if raw.cost > 0 {
		return raw.cost
	}
	if c.pricing.Input != 0 || c.pricing.Output != 0 {
		return calculateCostFromPricing(c.pricing, raw)
	}
	return 0
}

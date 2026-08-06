package agent

import (
	"context"
	"time"
)

// CompleteOptions carries per-request knobs. Adapters may ignore fields
// they don't support.
type CompleteOptions struct {
	Temperature *float32
	MaxTokens   *int
	// Extra carries additional body fields to inject into the LLM request.
	// Adapters that support it merge these into the outgoing JSON payload.
	Extra map[string]any
	// SessionID is the current hakka session ID. Adapters can use it to
	// resolve placeholders like "$session_id" in their Extra configuration.
	SessionID string
}

// Usage represents the token consumption of an LLM generation.
// Duration is the wall-clock time of the LLM call in nanoseconds,
// set by the engine as an informational metric.
// Cost is the monetary cost of the LLM call in USD, extracted from
// the provider's response (usage.cost field) when available or
// calculated from pricing config when the provider doesn't return it.
type Usage struct {
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	PromptCacheHitTokens  int           `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int           `json:"prompt_cache_miss_tokens,omitempty"`
	Duration              time.Duration `json:"duration_ns,omitempty"`
	Cost                  float64       `json:"cost,omitempty"`
}

// RetryConfig configures the retry policy for LLM provider HTTP calls.
// Zero values are replaced with sensible defaults at use-site.
type RetryConfig struct {
	// MaxAttempts is the total number of HTTP attempts (including the initial
	// call). 0 means use the default (20).
	MaxAttempts int `json:"max_attempts,omitempty"`
	// BaseDelay is the initial delay before the first retry. 0 means use the
	// default (250ms).
	BaseDelay time.Duration `json:"base_delay,omitempty"`
	// MaxDelay is the maximum delay cap. 0 means use the default (10s).
	MaxDelay time.Duration `json:"max_delay,omitempty"`
	// BackoffFactor is the multiplier applied to the delay after each retry
	// attempt. 0 means use the default (1.5).
	BackoffFactor float64 `json:"backoff_factor,omitempty"`
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:   20,
		BaseDelay:     250 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 1.5,
	}
}

// IsZero returns true when all fields are at their zero value (no explicit
// retry configuration). Adapters that historically had no retry should only
// apply retry when IsZero is false, preserving backward compatibility.
func (r RetryConfig) IsZero() bool {
	return r.MaxAttempts == 0 && r.BaseDelay == 0 && r.MaxDelay == 0 && r.BackoffFactor == 0
}

// Pricing defines per-token costs for LLM providers that do not return
// cost in their response. When set on a model config, the adapter
// calculates the monetary cost from the actual token usage after each
// LLM call.
//
// All values are per-token in USD. For providers like DeepSeek that
// charge differently for cached vs uncached prompt tokens, set
// CacheHitInput to the reduced rate.
type Pricing struct {
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	CacheHitInput float64 `json:"cache_hit_input,omitempty"`
}

// QuotaInfo holds the fetched quota/balance information for a provider.
type QuotaInfo struct {
	Balance  *float64 `json:"balance,omitempty"`  // money left (computed)
	Currency string   `json:"currency,omitempty"` // e.g. "CNY", "USD"
}

// QuotaSource describes a single HTTP endpoint + path expression to fetch
// a numeric value. Set Value to a static number to skip the HTTP call.
type QuotaSource struct {
	URL   string   `json:"url,omitempty"`
	Path  string   `json:"path,omitempty"`
	Value *float64 `json:"value,omitempty"`
}

// QuotaConfig describes how to fetch quota/balance info from one or more
// provider API endpoints.
//
// Three sources are supported:
//   - balance: directly gives money-left (e.g. DeepSeek)
//   - spent:   money consumed this period (defaults to 0)
//   - limit:   spending cap for the period (defaults to 0)
//
// Resolution order:
//  1. If balance is configured → use it directly.
//  2. Otherwise → compute balance = limit - spent.
//     Missing sources default to 0, so spent-only yields a negative balance.
//
// Currency is always a static literal (e.g. "USD").
type QuotaConfig struct {
	Balance  *QuotaSource `json:"balance,omitempty"`
	Spent    *QuotaSource `json:"spent,omitempty"`
	Limit    *QuotaSource `json:"limit,omitempty"`
	Currency string       `json:"currency,omitempty"`
}

// QuotaFetcher is an optional interface that adapters can implement to
// provide quota/balance information from the provider's API. The
// engine calls FetchQuota after each turn and on model switch.
type QuotaFetcher interface {
	FetchQuota(ctx context.Context) (*QuotaInfo, error)
}

// LLMResponse is the normalized result of a single completion call.
type LLMResponse struct {
	Message      Message
	FinishReason string
	Usage        *Usage
}

// LLMAdapter abstracts an LLM provider (OpenAI, Anthropic, Ollama, ...).
// Implementations should be safe for concurrent use.
//
// The engine always passes a non-nil onDelta callback. Adapters that
// support streaming may call onDelta for each text token as it arrives,
// giving the client incremental progress feedback. Adapters that do not
// support streaming simply ignore onDelta and return the full response in
// LLMResponse.Message.Content — that content is the source of truth
// regardless of whether onDelta was used.
//
// The returned LLMResponse always contains the full accumulated text and
// any tool calls.
type LLMAdapter interface {
	Complete(ctx context.Context, msgs []Message, tools []ToolSchema, opts CompleteOptions, onDelta func(string)) (*LLMResponse, error)
}

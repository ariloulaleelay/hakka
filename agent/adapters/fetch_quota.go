package adapters

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// FetchQuota implementations for each adapter.
// These use the shared FetchQuotaFromURL helper from quotafetcher.go.
// ---------------------------------------------------------------------------

// --- DeepSeek ---

func (ad *DeepSeekAdapter) FetchQuota(ctx context.Context) (*agent.QuotaInfo, error) {
	return fetchQuota(ctx, ad.HTTPClient, ad.Config.Quota)
}

// --- Anthropic ---

func (ad *AnthropicAdapter) FetchQuota(ctx context.Context) (*agent.QuotaInfo, error) {
	return fetchQuota(ctx, ad.HTTPClient, ad.Config.Quota)
}

// --- Gemini ---

func (ad *GeminiAdapter) FetchQuota(ctx context.Context) (*agent.QuotaInfo, error) {
	return fetchQuota(ctx, ad.HTTPClient, ad.Config.Quota)
}

// --- OpenAI ---

func (ad *OpenAIAdapter) FetchQuota(ctx context.Context) (*agent.QuotaInfo, error) {
	return fetchQuota(ctx, ad.Config.HTTPClient, ad.Config.Quota)
}

// --- OpenAI Responses ---

func (ad *OpenAIResponsesAdapter) FetchQuota(ctx context.Context) (*agent.QuotaInfo, error) {
	return fetchQuota(ctx, ad.Client, ad.Config.Quota)
}

// ---------------------------------------------------------------------------
// Shared helper
// ---------------------------------------------------------------------------

func fetchQuota(ctx context.Context, client *http.Client, quotaCfg *agent.QuotaConfig) (*agent.QuotaInfo, error) {
	if quotaCfg == nil || quotaCfg.URL == "" {
		slog.Debug("quota: no config or URL", "quotaCfg", quotaCfg)
		return nil, nil
	}
	// If Currency is set but has no dots/brackets, treat it as a static literal.
	// Otherwise it's a path expression to extract.
	staticCurrency := ""
	currencyPath := quotaCfg.Currency
	if currencyPath != "" && !isPathExpression(currencyPath) {
		staticCurrency = currencyPath
		currencyPath = ""
	}

	slog.Debug("quota: fetching from URL", "url", quotaCfg.URL)
	return FetchQuotaFromURL(ctx, client,
		quotaCfg.URL,
		quotaCfg.Balance,
		currencyPath,
		staticCurrency,
	)
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

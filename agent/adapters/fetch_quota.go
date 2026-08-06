package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// ---------------------------------------------------------------------------
// FetchQuota implementations for each adapter.
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
	if quotaCfg == nil {
		slog.Debug("quota: no config")
		return nil, nil
	}

	info := &agent.QuotaInfo{Currency: quotaCfg.Currency}

	// 1. Direct balance source.
	if quotaCfg.Balance != nil && quotaCfg.Balance.URL != "" {
		slog.Debug("quota: fetching balance source", "url", quotaCfg.Balance.URL)
		v, err := fetchSource(ctx, client, quotaCfg.Balance.URL, quotaCfg.Balance.Path)
		if err != nil {
			return nil, err
		}
		info.Balance = v
		return info, nil
	}

	// 2. Spent and/or limit → compute balance = limit - spent.
	//    Missing sources default to 0 (spent-only gives negative balance).
	hasURL := false
	var spent, limit float64

	if s := quotaCfg.Spent; s != nil {
		if s.Value != nil {
			spent = *s.Value
		} else if s.URL != "" {
			hasURL = true
			slog.Debug("quota: fetching spent source", "url", s.URL)
			if v, err := fetchSource(ctx, client, s.URL, s.Path); err != nil {
				return nil, err
			} else if v != nil {
				spent = *v
			}
		}
	}
	if l := quotaCfg.Limit; l != nil {
		if l.Value != nil {
			limit = *l.Value
		} else if l.URL != "" {
			hasURL = true
			slog.Debug("quota: fetching limit source", "url", l.URL)
			if v, err := fetchSource(ctx, client, l.URL, l.Path); err != nil {
				return nil, err
			} else if v != nil {
				limit = *v
			}
		}
	}

	if hasURL {
		b := limit - spent
		info.Balance = &b
		return info, nil
	}

	slog.Debug("quota: no usable source configured")
	return nil, nil
}

// fetchSource fetches a single quota source URL and extracts a numeric value
// from the given JSON path.
func fetchSource(ctx context.Context, client *http.Client, url, path string) (*float64, error) {
	body, err := doQuotaGET(ctx, client, url)
	if err != nil {
		return nil, err
	}

	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("quota: parse JSON: %w", err)
	}

	if path != "" {
		if v, ok := extractJSONPathFloat(root, path); ok {
			return &v, nil
		}
	}
	return nil, nil
}

// doQuotaGET makes a GET request and returns the response body.
func doQuotaGET(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	slog.Debug("quota: GET", "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("quota: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quota: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("quota: HTTP error", "status", resp.Status, "body", string(body))
		return nil, fmt.Errorf("quota: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("quota: read body: %w", err)
	}

	slog.Debug("quota: response", "body", string(body))
	return body, nil
}

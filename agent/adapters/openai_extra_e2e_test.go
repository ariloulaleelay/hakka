package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

// TestOpenAIExtraThroughBuildRegistry simulates the exact flow that
// BuildRegistry uses: transport chain with extra injection.
func TestOpenAIExtraThroughBuildRegistry(t *testing.T) {
	extra := map[string]any{"session_id": "$session_id", "provider": "openrouter"}
	var captured map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		t.Logf("request body: %s", string(body))
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	// Build transport chain exactly like config.go does:
	// Wrap default transport with extra injection.
	wrapped := WrapTransport(http.DefaultTransport, extra, "")
	httpClient := &http.Client{Transport: wrapped}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig("", agent.RetryConfig{}, agent.Pricing{}, extra))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{
			SessionID: "my-test-session-uuid",
		}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if captured["provider"] != "openrouter" {
		t.Errorf("expected provider=openrouter in body, got %v", captured["provider"])
	}
	if captured["session_id"] != "my-test-session-uuid" {
		t.Errorf("expected session_id='my-test-session-uuid' in body, got %v", captured["session_id"])
	}
	if captured["model"] != "test-model" {
		t.Errorf("expected model='test-model' in body, got %v", captured["model"])
	}
	if captured["messages"] == nil {
		t.Errorf("expected messages in body, got %v", captured)
	}
}

// TestOpenAIExtraWithoutSessionID simulates what happens when opts.SessionID
// is empty but extras are configured — the placeholder should remain as-is.
func TestOpenAIExtraWithoutSessionID(t *testing.T) {
	extra := map[string]any{"provider": "openrouter", "session_id": "$session_id"}
	var captured map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		t.Logf("request body: %s", string(body))
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	// Wrap transport with instrumentedTransport
	httpClient := &http.Client{Transport: WrapTransport(http.DefaultTransport, extra, "")}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig("", agent.RetryConfig{}, agent.Pricing{}, extra))

	// No SessionID in opts — placeholder is replaced with empty string
	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if captured["provider"] != "openrouter" {
		t.Errorf("expected provider=openrouter in body, got %v", captured["provider"])
	}
	// Without SessionID, $session_id placeholder is replaced with empty string
	if captured["session_id"] != "" {
		t.Errorf("expected session_id='' (empty, unresolved) in body, got %v", captured["session_id"])
	}
}

// TestInstrumentedTransportGETNoBody verifies that GET requests (which have nil
// body) do not panic in instrumentedTransport.RoundTrip. This reproduces a
// panic caused by io.ReadAll(nil) when the quota fetcher makes a GET request
// through the wrapped transport.
func TestInstrumentedTransportGETNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	// Wrap transport with instrumentedTransport (exactly like the quota fetch path).
	client := &http.Client{Transport: WrapTransport(http.DefaultTransport, nil, "")}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

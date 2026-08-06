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

// newOpenAITestAdapterWithExtra creates a test OpenAI adapter with extras configured.
func newOpenAITestAdapterWithExtra(t *testing.T, handler http.HandlerFunc, extra map[string]any) (*OpenAIAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	transport := WrapTransport(http.DefaultTransport, extra, "")
	httpClient := &http.Client{Transport: transport}

	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig("", agent.RetryConfig{}, agent.Pricing{}, extra))
	return adapter, srv
}

func TestOpenAIExtraInRequestBody(t *testing.T) {
	extra := map[string]any{"provider": "openrouter", "cache_prompt": true}
	var captured map[string]any

	adapter, _ := newOpenAITestAdapterWithExtra(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}, extra)

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
	if captured["cache_prompt"] != true {
		t.Errorf("expected cache_prompt=true in body, got %v", captured["cache_prompt"])
	}
	if _, ok := captured["model"]; !ok {
		t.Errorf("expected 'model' field in body, got %v", captured)
	}
}

func TestOpenAISessionIDPlaceholder(t *testing.T) {
	extra := map[string]any{"session_id": "$session_id"}
	var captured map[string]any

	adapter, _ := newOpenAITestAdapterWithExtra(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}, extra)

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{
			SessionID: "my-session-uuid",
		}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if captured["session_id"] != "my-session-uuid" {
		t.Errorf("expected session_id='my-session-uuid' in body, got %v", captured["session_id"])
	}
}

func TestOpenAIPerRequestExtra(t *testing.T) {
	extra := map[string]any{"provider": "openrouter"}
	var captured map[string]any

	adapter, _ := newOpenAITestAdapterWithExtra(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}, extra)

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{
			Extra: map[string]any{"route": "sticky"},
		}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if captured["provider"] != "openrouter" {
		t.Errorf("expected provider=openrouter, got %v", captured["provider"])
	}
	if captured["route"] != "sticky" {
		t.Errorf("expected route=sticky, got %v", captured["route"])
	}
}

func TestOpenAIExtraEmpty(t *testing.T) {
	// When no extras are configured, the body should be unchanged.
	var captured map[string]any

	adapter, _ := newOpenAITestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	})

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Should contain standard fields but not extras
	if _, ok := captured["model"]; !ok {
		t.Errorf("expected model field, got %v", captured)
	}
	if _, ok := captured["session_id"]; ok {
		t.Errorf("unexpected session_id in body: %v", captured["session_id"])
	}
}

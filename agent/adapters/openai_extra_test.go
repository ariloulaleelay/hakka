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

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model")
	adapter.Extra = extra
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
		nil, agent.CompleteOptions{},
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
		},
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
		},
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

func TestOpenAIExtraStream(t *testing.T) {
	extra := map[string]any{"session_id": "$session_id", "provider": "openrouter"}
	var captured map[string]any
	capturedCh := make(chan map[string]any, 1)

	adapter, _ := newOpenAITestAdapterWithExtra(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err == nil {
			capturedCh <- captured
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		payload := map[string]any{
			"choices": []any{
				map[string]any{
					"delta":        map[string]any{"content": "hello"},
					"finish_reason": "stop",
				},
			},
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}, extra)

	resultCh, err := adapter.Stream(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{
			SessionID: "stream-session-id",
		},
	)
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	for range resultCh {
		// consume
	}

	select {
	case cap := <-capturedCh:
		if cap["provider"] != "openrouter" {
			t.Errorf("expected provider=openrouter, got %v", cap["provider"])
		}
		if cap["session_id"] != "stream-session-id" {
			t.Errorf("expected session_id='stream-session-id', got %v", cap["session_id"])
		}
	default:
		t.Error("did not capture request body")
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
	adapter.Extra = nil

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
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

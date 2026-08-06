package adapters

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
)

func debugDumpFileCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	return count
}

func TestDebugDumpOpenAIWithoutExtra(t *testing.T) {
	debugDir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"test-model"`) {
			t.Errorf("model not in request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	transport := WrapTransport(http.DefaultTransport, nil, debugDir)
	httpClient := &http.Client{Transport: transport}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig(debugDir, agent.RetryConfig{}, agent.Pricing{}, nil))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	count := debugDumpFileCount(t, debugDir)
	if count < 2 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 2 debug files (body + resp), got %d", count)
	}
}

func TestDebugDumpAnthropic(t *testing.T) {
	debugDir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"max_tokens"`) {
			t.Errorf("expected max_tokens in request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1",
			"content": [{"type": "text", "text": "hello from claude"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 5, "output_tokens": 10}
		}`))
	}))
	t.Cleanup(srv.Close)

	adapter := NewAnthropicAdapter(srv.Client(), srv.URL, "claude-opus-4", NewAnthropicConfig(debugDir, agent.RetryConfig{}, agent.Pricing{}, "2023-06-01", 1024))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	count := debugDumpFileCount(t, debugDir)
	if count < 2 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 2 debug files for Anthropic Complete, got %d", count)
	}
}

func TestDebugDumpGemini(t *testing.T) {
	debugDir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{"content": {"parts": [{"text": "hello from gemini"}], "role": "model"}, "finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
		}`))
	}))
	t.Cleanup(srv.Close)

	adapter := NewGeminiAdapter(srv.Client(), srv.URL, "gemini-2.0-flash", NewGeminiConfig(debugDir, agent.RetryConfig{}, agent.Pricing{}))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	count := debugDumpFileCount(t, debugDir)
	if count < 2 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 2 debug files for Gemini Complete, got %d", count)
	}
}

func TestDebugDumpDirCreation(t *testing.T) {
	debugDir := filepath.Join(t.TempDir(), "nested", "debug")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("empty request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	transport := WrapTransport(http.DefaultTransport, nil, debugDir)
	httpClient := &http.Client{Transport: transport}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model", NewOpenAIConfig(debugDir, agent.RetryConfig{}, agent.Pricing{}, nil))

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{}, nil,
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	count := debugDumpFileCount(t, debugDir)
	if count < 1 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 1 debug file in nested dir, got %d", count)
	}
}

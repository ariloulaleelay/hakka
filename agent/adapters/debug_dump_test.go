package adapters

import (
	"context"
	"encoding/json"
	"fmt"
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

// debugDumpFileCount returns the number of JSON files in the given directory.
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

// TestDebugDumpOpenAIWithoutExtra verifies that request body is dumped to the
// debug directory for OpenAI models even when no extra fields are configured.
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

	// Build with debugDir but NO extras — this should still dump
	transport := WrapTransport(http.DefaultTransport, nil, debugDir)
	httpClient := &http.Client{Transport: transport}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model")
	adapter.LLMDebugDir = debugDir

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Should have at least one dump file (body + resp)
	count := debugDumpFileCount(t, debugDir)
	if count < 2 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 2 debug files (body + resp), got %d", count)
	}
}

// TestDebugDumpOpenAIWithoutExtraStream verifies that streaming also dumps
// to debug directory even without extras.
func TestDebugDumpOpenAIWithoutExtraStream(t *testing.T) {
	debugDir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	t.Cleanup(srv.Close)

	transport := WrapTransport(http.DefaultTransport, nil, debugDir)
	httpClient := &http.Client{Transport: transport}

	cfg := openai.DefaultConfig("dummy-key")
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = httpClient

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model")
	adapter.LLMDebugDir = debugDir

	resultCh, err := adapter.Stream(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
	)
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	for range resultCh {
		// consume
	}

	// Should have at least one dump file (the wire body)
	count := debugDumpFileCount(t, debugDir)
	if count < 1 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 1 debug file (wire body) for stream, got %d", count)
	}
}

// TestDebugDumpAnthropic verifies that doJSONPost dumps request/response
// bodies when debugDir is set on the adapter.
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

	adapter := NewAnthropicAdapter(srv.Client(), srv.URL, "claude-opus-4")
	adapter.Version = "2023-06-01"
	adapter.MaxTokens = 1024
	adapter.LLMDebugDir = debugDir

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
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

// TestDebugDumpAnthropicStream verifies that doStreamPost dumps request body
// when debugDir is set.
func TestDebugDumpAnthropicStream(t *testing.T) {
	debugDir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Simplified SSE responses
		events := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"message_delta","usage":{"input_tokens":1,"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	adapter := NewAnthropicAdapter(srv.Client(), srv.URL, "claude-opus-4")
	adapter.Version = "2023-06-01"
	adapter.MaxTokens = 1024
	adapter.LLMDebugDir = debugDir

	resultCh, err := adapter.Stream(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
	)
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	for range resultCh {
		// consume
	}

	count := debugDumpFileCount(t, debugDir)
	if count < 1 {
		entries, _ := os.ReadDir(debugDir)
		for _, e := range entries {
			t.Logf("debug dir entry: %s", e.Name())
		}
		t.Fatalf("expected at least 1 debug file for Anthropic Stream, got %d", count)
	}
}

// TestDebugDumpGemini verifies that doJSONPost dumps bodies for Gemini adapter.
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

	adapter := NewGeminiAdapter(srv.Client(), srv.URL, "gemini-2.0-flash")
	adapter.LLMDebugDir = debugDir

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
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

// TestDebugDumpDirCreation verifies that the debug directory is created
// automatically if it doesn't exist (dumpDebugJSON creates it).
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

	adapter := NewOpenAIAdapter(openai.NewClientWithConfig(cfg), "test-model")
	adapter.LLMDebugDir = debugDir

	_, err := adapter.Complete(context.Background(),
		[]agent.Message{{Role: agent.RoleUser, Content: "hi"}},
		nil, agent.CompleteOptions{},
	)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if _, err := os.Stat(debugDir); os.IsNotExist(err) {
		t.Fatal("debug directory was not created")
	}
	count := debugDumpFileCount(t, debugDir)
	if count < 2 {
		t.Fatalf("expected debug files in created directory, got %d", count)
	}
}

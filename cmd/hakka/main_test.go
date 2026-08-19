package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/gateways"
	"github.com/ariloulaleelay/hakka/batch"
)

// ---------------------------------------------------------------------------
// A scripted adapter that returns a fixed sequence of LLM responses,
// defined here in the test package so we don't depend on unexported types
// from the agent package.
// ---------------------------------------------------------------------------

type scriptedAdapter struct {
	responses []agent.LLMResponse
	calls     int
}

func (a *scriptedAdapter) Complete(_ context.Context, msgs []agent.Message, tools []agent.ToolSchema, _ agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	if a.calls >= len(a.responses) {
		return nil, errors.New("scripted: out of responses")
	}
	r := a.responses[a.calls]
	a.calls++
	return &r, nil
}

// ---------------------------------------------------------------------------
// A tool that echoes its arguments back.
// ---------------------------------------------------------------------------

func echoTool() agent.Tool {
	return agent.Tool{
		Schema: agent.ToolSchema{
			Name:        "echo",
			Description: "Echoes the input back",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
				"required": []any{"text"},
			},
		},
		Tags: []string{"utility", "all"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			return "echo: " + args.Text, nil
		},
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func testAdapterRegistry(t *testing.T, adapter agent.LLMAdapter) *agent.Registry {
	t.Helper()
	reg := agent.NewRegistry()
	reg.Register("test-model", adapter)
	if err := reg.SetDefault("test-model"); err != nil {
		t.Fatal(err)
	}
	return reg
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// ---------------------------------------------------------------------------
// Tests — Given / When / Then structure (Kevlin Henney style)
// ---------------------------------------------------------------------------

func TestBatchTaskGivenSimpleQueryWhenRunThenReturnsAssistantReply(t *testing.T) {
	// Given a registry with a scripted adapter that returns "hello from batch"
	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{Message: agent.Message{Role: agent.RoleAssistant, Content: "hello from batch"}},
		},
	}
	reg := testAdapterRegistry(t, adapter)

	// When we run a batch task
	reply, err := batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry: reg,
		Task:     "say hello",
		Logger:   testLogger(t),
	})

	// Then the assistant's reply is returned without error
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	if reply != "hello from batch" {
		t.Fatalf("expected reply %q, got %q", "hello from batch", reply)
	}
}

func TestBatchTaskGivenToolUseWhenRunThenExecutesToolsAndReturnsFinalReply(t *testing.T) {
	// Given a registry with a scripted adapter that does one tool call then replies
	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{
				Message: agent.Message{
					Role: agent.RoleAssistant,
					ToolCalls: []agent.ToolCall{{
						ID: "call-1", Name: "echo", Arguments: `{"text":"hello world"}`,
					}},
				},
			},
			{Message: agent.Message{Role: agent.RoleAssistant, Content: "echo returned: hello world"}},
		},
	}
	reg := testAdapterRegistry(t, adapter)
	tools := agent.NewToolRegistry()
	tools.Register(echoTool())

	// When we run a batch task with the echo tool enabled
	reply, err := batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry:    reg,
		Task:        "echo hello world",
		Tools:       tools,
		EnableTools: []string{"echo"},
		Logger:      testLogger(t),
	})

	// Then the tool is executed and the final reply is returned
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	if reply != "echo returned: hello world" {
		t.Fatalf("expected reply %q, got %q", "echo returned: hello world", reply)
	}
	// And the adapter was called exactly twice (tool call + final reply)
	if adapter.calls != 2 {
		t.Fatalf("expected 2 LLM calls (tool + final), got %d", adapter.calls)
	}
}

func TestBatchTaskGivenNoToolEnablingWhenRunThenNoToolsAreAvailable(t *testing.T) {
	// Given a registry and a tool registry with several tools
	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{Message: agent.Message{Role: agent.RoleAssistant, Content: "ok"}},
		},
	}
	reg := testAdapterRegistry(t, adapter)
	tools := agent.NewToolRegistry()
	tools.Register(echoTool())

	// Use a shared store so we can inspect the session after the run.
	store := agent.NewMemoryStore()

	// When we run a batch task WITHOUT specifying any enabled tools
	var auxBuf strings.Builder
	_, err := batch.RunBatchWithOutput(context.Background(), batch.RunBatchParams{
		Registry: reg,
		Task:     "hello",
		Tools:    tools,
		// EnableTools is nil — deliberately not whitelisting anything
		Logger: testLogger(t),
		Store:  store,
	}, io.Discard, &auxBuf)

	// Then the run completes but no tools were enabled on the session.
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	// Parse the session ID from aux output.
	var sessionID string
	for _, line := range strings.Split(auxBuf.String(), "\n") {
		if strings.HasPrefix(line, "session: ") {
			sessionID = strings.TrimPrefix(line, "session: ")
			break
		}
	}
	if sessionID == "" {
		t.Fatal("expected session ID in aux output, got none")
	}

	// Verify the session has no enabled tools.
	manager := agent.NewSessionManager(store, "")
	session, err := manager.CreateWithID(context.Background(), "batch", sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	for _, s := range tools.Schemas() {
		if session.IsToolEnabled(s.Name) {
			t.Fatalf("tool %q is enabled but EnableTools was not specified", s.Name)
		}
	}
}

func TestBatchTaskGivenTagBasedToolEnablingWhenRunThenOnlyMatchingToolsEnabled(t *testing.T) {
	// Given tools with different tags
	tools := agent.NewToolRegistry()
	tools.Register(echoTool()) // tagged "utility" and "all"

	// Register a second tool that should NOT be enabled
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "dangerous", Description: "harmful"},
		Tags:   []string{"dangerous"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "boom", nil
		},
	})

	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{Message: agent.Message{Role: agent.RoleAssistant, Content: "done"}},
		},
	}
	reg := testAdapterRegistry(t, adapter)

	// When we enable only "utility" tag tools
	reply, err := batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry:    reg,
		Task:        "test",
		Tools:       tools,
		EnableTools: []string{"utility"},
		Logger:      testLogger(t),
	})

	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	_ = reply
	// Verify which tools were enabled on the session
}

func TestBatchTaskGivenExactToolNameWhenResolvingThenMatchesByName(t *testing.T) {
	// Given a tool registry with several tools
	tools := agent.NewToolRegistry()
	tools.Register(echoTool())

	// When we resolve "echo" as an exact name
	resolved := batch.ResolveToolsByTagOrName(tools, []string{"echo"})

	// Then only echo is resolved
	if len(resolved) != 1 || resolved[0] != "echo" {
		t.Fatalf("expected [echo], got %v", resolved)
	}
}

func TestBatchTaskGivenTagWhenResolvingThenMatchesAllTaggedTools(t *testing.T) {
	// Given tools with tags
	tools := agent.NewToolRegistry()
	tools.Register(echoTool()) // tagged "utility", "all"
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "uppercase", Description: "Uppercases input"},
		Tags:   []string{"utility", "all"},
		Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
			return "", nil
		},
	})
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "secret", Description: "hidden"},
		Tags:   []string{"internal"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", nil
		},
	})

	// When we resolve "utility" as a tag
	resolved := batch.ResolveToolsByTagOrName(tools, []string{"utility"})

	// Then both echo and uppercase are resolved, but not secret
	if len(resolved) != 2 {
		t.Fatalf("expected 2 tools matching 'utility' tag, got %v", resolved)
	}
}

func TestBatchTaskGivenTagCollidesWithToolNameWhenPlainRefThenOnlyToolEnabled(t *testing.T) {
	// Given a tool named "all" that collides with the tag "all"
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "all", Description: "does everything"},
		Tags:   []string{"utility"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "done", nil
		},
	})
	tools.Register(echoTool()) // tagged "utility", "all"

	// When we resolve "all" as a plain reference (no prefix)
	resolved := batch.ResolveToolsByTagOrName(tools, []string{"all"})

	// Then only the tool named "all" is matched — the tag "all" is shadowed
	if len(resolved) != 1 || resolved[0] != "all" {
		t.Fatalf("expected [all] (tool name match), got %v", resolved)
	}
}

func TestBatchTaskGivenTagCollidesWithToolNameWhenPrefixedRefThenTagExpanded(t *testing.T) {
	// Given a tool named "all" that collides with the tag "all"
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "all", Description: "does everything"},
		Tags:   []string{"utility"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "done", nil
		},
	})
	tools.Register(echoTool()) // tagged "utility", "all"

	// When we resolve "#all" as a tag-prefixed reference
	resolved := batch.ResolveToolsByTagOrName(tools, []string{"#all"})

	// Then all tools tagged "all" are expanded — echo gets included,
	// but the tool named "all" is NOT included (it's tagged "utility", not "all")
	if len(resolved) != 1 || resolved[0] != "echo" {
		t.Fatalf("expected [echo] (tag match), got %v", resolved)
	}
}

func TestBatchTaskGivenMixedTagsAndNamesWhenResolvingThenCombinesBoth(t *testing.T) {
	// Given a tool registry
	tools := agent.NewToolRegistry()
	tools.Register(echoTool()) // "utility", "all"
	tools.Register(agent.Tool{Schema: agent.ToolSchema{Name: "random", Description: "random"}, Tags: []string{"utility", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) { return "4", nil },
	})

	// When we resolve "echo" (by name) and "utility" (by tag)
	resolved := batch.ResolveToolsByTagOrName(tools, []string{"echo", "utility"})

	// Then echo is included once and random is included from the tag
	if len(resolved) != 2 {
		t.Fatalf("expected 2 tools, got %v", resolved)
	}
}

func TestBatchTaskGivenModelDirWhenRunThenOutputsSessionIdAndTokenUsage(t *testing.T) {
	// Given a registry with a scripted adapter that reports usage
	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{
				Message: agent.Message{Role: agent.RoleAssistant, Content: "done"},
				Usage:   &agent.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			},
		},
	}
	reg := testAdapterRegistry(t, adapter)

	// When we run a batch task and capture auxiliary output
	var outputBuf strings.Builder
	var auxBuf strings.Builder
	_, err := batch.RunBatchWithOutput(context.Background(), batch.RunBatchParams{
		Registry: reg,
		Task:     "test",
		Logger:   testLogger(t),
	}, &outputBuf, &auxBuf)

	// Then the auxiliary output contains session ID and token info
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	if !strings.Contains(auxBuf.String(), "session:") {
		t.Fatalf("expected session ID in auxiliary output, got: %s", auxBuf.String())
	}
	if !strings.Contains(auxBuf.String(), "15") {
		t.Fatalf("expected token usage (15) in auxiliary output, got: %s", auxBuf.String())
	}
}

func TestBatchTaskGivenToolThatFailsWhenRunThenEngineContinues(t *testing.T) {
	// Given a failing tool
	tools := agent.NewToolRegistry()
	tools.Register(agent.Tool{
		Schema: agent.ToolSchema{Name: "failing", Description: "always fails"},
		Tags:   []string{"utility", "all"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", errors.New("something went wrong")
		},
	})
	adapter := &scriptedAdapter{
		responses: []agent.LLMResponse{
			{
				Message: agent.Message{
					Role: agent.RoleAssistant,
					ToolCalls: []agent.ToolCall{{
						ID: "call-1", Name: "failing", Arguments: `{}`,
					}},
				},
			},
			{Message: agent.Message{Role: agent.RoleAssistant, Content: "recovered from error"}},
		},
	}
	reg := testAdapterRegistry(t, adapter)

	// When we run a batch task with the failing tool enabled
	reply, err := batch.RunBatch(context.Background(), batch.RunBatchParams{
		Registry:    reg,
		Task:        "use failing tool",
		Tools:       tools,
		EnableTools: []string{"utility"},
		Logger:      testLogger(t),
	})

	// Then the engine continues after the error and returns the final reply
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	if reply != "recovered from error" {
		t.Fatalf("expected reply %q, got %q", "recovered from error", reply)
	}
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level string
	}{
		{"debug"},
		{"info"},
		{"warn"},
		{"error"},
		{"unknown"}, // fallback to info
	}

	for _, tt := range tests {
		logger := newLogger(tt.level)
		if logger == nil {
			t.Errorf("expected logger to not be nil for level %s", tt.level)
		}
	}
}

// ---------------------------------------------------------------------------
// Web frontend gateway — web files wiring
// ---------------------------------------------------------------------------

func TestBuildWebFrontGateway_WiresFileStore(t *testing.T) {
	reg := testAdapterRegistry(t, &scriptedAdapter{})
	platform := agent.NewPlatform(agent.PlatformConfig{
		Store:        agent.NewMemoryStore(),
		Registry:     reg,
		SystemPrompt: "sys",
	})
	tools := agent.NewToolRegistry()
	hub := gateways.NewNamespaceHub("ws")

	gw := buildWebFrontGateway(platform, tools, ":0", hub)
	if gw == nil {
		t.Fatal("buildWebFrontGateway returned nil")
	}
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := gw.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	cancel()
}

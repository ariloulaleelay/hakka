package agent

import (
	"context"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// stubLLM implements LLMAdapter for tests that need a valid
// Registry entry but never call the adapter.
type stubLLM struct{}

func (stubLLM) Complete(_ context.Context, _ []Message, _ []ToolSchema, _ CompleteOptions, _ func(string)) (*LLMResponse, error) {
	return nil, nil
}

func TestNewPlatformMinimal(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "You are a test assistant.",
	})

	if p == nil {
		t.Fatal("NewPlatform returned nil")
	}
	if p.Sessions() == nil {
		t.Error("Sessions() returned nil")
	}
	if p.Router() == nil {
		t.Error("Router() returned nil")
	}
	if p.SystemPrompt() != "You are a test assistant." {
		t.Errorf("SystemPrompt() = %q, want %q", p.SystemPrompt(), "You are a test assistant.")
	}
}

func TestNewPlatformWithFullConfig(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	cfg := DefaultEngineConfig()
	cfg.MaxToolIterations = 42

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "Hello",
		EngineCfg:    cfg,
	})

	if p.EngineConfig().MaxToolIterations != 42 {
		t.Errorf("MaxToolIterations = %d, want 42", p.EngineConfig().MaxToolIterations)
	}
}

func TestPlatformForNamespace(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "Test prompt.",
	})

	tools := NewToolRegistry()
	ns := p.ForNamespace("ws", tools, nil)

	if ns.Conversation == nil {
		t.Error("NamespaceComponents.Conversation is nil")
	}
	if ns.Tools == nil {
		t.Error("NamespaceComponents.Tools is nil")
	}
	if ns.Tools != tools {
		t.Error("NamespaceComponents.Tools != provided tools")
	}
}

func TestPlatformForNamespaceIsolation(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "Test.",
	})

	wsTools := NewToolRegistry()
	tgTools := NewToolRegistry()

	wsNs := p.ForNamespace("ws", wsTools, nil)
	tgNs := p.ForNamespace("tg", tgTools, nil)

	// Each namespace gets its own Conversation
	if wsNs.Conversation == tgNs.Conversation {
		t.Error("Conversations are shared across namespaces — should be isolated")
	}

	// Check namespaces are set correctly
	if wsNs.Conversation.Namespace() != "ws" {
		t.Errorf("WS Conversation namespace = %q, want %q", wsNs.Conversation.Namespace(), "ws")
	}
	if tgNs.Conversation.Namespace() != "tg" {
		t.Errorf("TG Conversation namespace = %q, want %q", tgNs.Conversation.Namespace(), "tg")
	}
}

func TestPlatformForNamespaceWithDecorator(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "Test.",
	})

	tools := NewToolRegistry()

	ns := p.ForNamespace("ws", tools,
		ToolContextDecoratorFunc(func(ctx context.Context, sessionID string, events chan<- event.EngineEvent) context.Context {
			return ctx
		}),
	)

	// The decorator is installed on the Conversation via SetToolContext.
	// It is NOT invoked during ForNamespace — only later during tool
	// execution. We just verify components are non-nil.
	if ns.Conversation == nil {
		t.Error("Conversation is nil after ForNamespace with decorator")
	}
}

func TestPlatformForNamespaceNoExtraConfig(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("test-model", stubLLM{})

	// Platform without any optional extras — skills are session-bound and
	// need no platform-level configuration.
	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "Test.",
	})

	tools := NewToolRegistry()
	ns := p.ForNamespace("ws", tools, nil)

	if ns.Conversation == nil {
		t.Error("Conversation is nil")
	}
}

func TestPlatformSessionsAccessor(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("m", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "X",
	})

	s, err := p.Sessions().CreateWithID(t.Context(), "test-ns", "")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	if s == nil {
		t.Fatal("session is nil")
	}
}

func TestPlatformSkillsAreSessionBound(t *testing.T) {
	// Skills are per-session: sessions created via the platform must not
	// share any registry state.
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("m", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "X",
	})

	s1, err := p.Sessions().CreateWithID(t.Context(), "ns-1", "")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	s2, err := p.Sessions().CreateWithID(t.Context(), "ns-2", "")
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}

	if s1.SkillRegistry() == s2.SkillRegistry() {
		t.Error("sessions must not share a skill registry")
	}
}

func TestPlatformEngineConfigDefaults(t *testing.T) {
	store := NewMemoryStore()
	reg := NewRegistry()
	reg.Register("m", stubLLM{})

	p := NewPlatform(PlatformConfig{
		Store:        store,
		Registry:     reg,
		SystemPrompt: "X",
	})

	cfg := p.EngineConfig()
	def := DefaultEngineConfig()

	if cfg.MaxToolIterations != def.MaxToolIterations {
		t.Errorf("MaxToolIterations = %d, want %d (default)", cfg.MaxToolIterations, def.MaxToolIterations)
	}
	if cfg.CompactSoftLimit != def.CompactSoftLimit {
		t.Errorf("CompactSoftLimit = %d, want %d (default)", cfg.CompactSoftLimit, def.CompactSoftLimit)
	}
}

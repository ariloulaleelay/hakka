package tools

import (
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// TestRegisterTelegramTools_OnlySafeTools verifies that RegisterTelegramTools
// only registers safe tools (http_get) and none of the dangerous ones.
func TestRegisterTelegramTools_OnlySafeTools(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterTelegramTools(reg)

	schemas := reg.Schemas()

	// Build a set of registered names
	names := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		names[s.Name] = true
	}

	// Dangerous tools that MUST NOT be available to Telegram users
	dangerous := []string{
		"read_file",
		"write_file",
		"edit_file",
		"list_dir",
		"shell",
		"vim_run_command",
		"search",
	}

	for _, name := range dangerous {
		if names[name] {
			t.Errorf("dangerous tool %q registered by RegisterTelegramTools — should not be available to Telegram users", name)
		}
	}

	// Safe tools that SHOULD be available
	if !names["http_get"] {
		t.Error("safe tool http_get should be registered by RegisterTelegramTools")
	}
	if !names["random"] {
		t.Error("safe tool random should be registered by RegisterTelegramTools")
	}

	t.Logf("Telegram tools registered: %v", names)
}

// TestRegisterTelegramTools_OnlySafeTools verifies that
// RegisterTelegramTools only registers safe tools (http_get, random)
// and none of the dangerous ones.
func TestRegisterTelegramTools_SafeToolList(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterTelegramTools(reg)

	schemas := reg.Schemas()

	// Should have exactly 2 tools: http_get, random
	if len(schemas) != 2 {
		t.Fatalf("expected exactly 2 tools (http_get, random), got %d: %+v", len(schemas), schemas)
	}

	names := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		names[s.Name] = true
	}

	if !names["http_get"] {
		t.Fatalf("expected http_get to be registered, got: %+v", names)
	}
	if !names["random"] {
		t.Fatalf("expected random to be registered, got: %+v", names)
	}
}

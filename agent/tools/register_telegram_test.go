package tools

import (
	"testing"

	"github.com/you/hakka/agent"
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

	// Safe tool that SHOULD be available
	if !names["http_get"] {
		t.Error("safe tool http_get should be registered by RegisterTelegramTools")
	}

	t.Logf("Telegram tools registered: %v", names)
}

// TestRegisterTelegramTools_DoesNotAffectFullRegistry verifies that
// RegisterTelegramTools creates a separate toolset and does not leak
// dangerous tools into it.
func TestRegisterTelegramTools_OnlyHTTPGet(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterTelegramTools(reg)

	schemas := reg.Schemas()

	// Should have exactly 1 tool: http_get
	if len(schemas) != 1 {
		t.Fatalf("expected exactly 1 tool (http_get), got %d: %+v", len(schemas), schemas)
	}

	if schemas[0].Name != "http_get" {
		t.Fatalf("expected the only tool to be http_get, got %q", schemas[0].Name)
	}
}

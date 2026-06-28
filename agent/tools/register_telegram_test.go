package tools

import (
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
)

// TestRegisterTelegramTools_OnlySafeTools verifies that RegisterTelegramTools
// only registers safe tools (http_get, random, feedback) and none of the
// dangerous ones (filesystem, shell, neovim).
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
	if !names["feedback"] {
		t.Error("safe tool feedback should be registered by RegisterTelegramTools")
	}

	t.Logf("Telegram tools registered: %v", names)
}

// TestRegisterTelegramTools_SafeToolCount verifies that RegisterTelegramTools
// registers the expected set of safe tools (http_get, random, feedback).
func TestRegisterTelegramTools_SafeToolCount(t *testing.T) {
	reg := agent.NewToolRegistry()
	RegisterTelegramTools(reg)

	schemas := reg.Schemas()

	if len(schemas) != 3 {
		t.Fatalf("expected exactly 3 tools (http_get, random, feedback), got %d: %+v", len(schemas), schemas)
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
	if !names["feedback"] {
		t.Fatalf("expected feedback to be registered, got: %+v", names)
	}
}

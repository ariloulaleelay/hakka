package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func newTestSkillRegistry(t *testing.T) *agent.SkillRegistry {
	t.Helper()

	// Create temp skill files
	dir := t.TempDir()

	skills := map[string]string{
		"go-testing.md": `---
name: go-testing
description: How to write and run tests in Go
tags: go, testing
---
# Go Testing
Run tests with: go test ./...`,
		"deploy-app.md": `---
name: deploy-app
description: Deploy the application to production
tags: deploy, production
---
# Deploy
Steps to deploy the application.`,
	}

	for name, content := range skills {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := agent.NewSkillRegistry()
	if _, err := reg.AddDir(dir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	return reg
}

func runSkillTool(t *testing.T, tool agent.Tool, args any) string {
	t.Helper()

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(args)
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("tool %q: %v", tool.Schema.Name, err)
	}
	return res
}

// ---------------------------------------------------------------------------
// search_skills
// ---------------------------------------------------------------------------

func TestSearchSkills_Found(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := SearchSkills(reg)

	res := runSkillTool(t, tool, map[string]any{
		"query": "testing",
	})

	if !strings.Contains(res, "go-testing") {
		t.Fatalf("expected result to mention 'go-testing', got: %s", res)
	}
}

func TestSearchSkills_NotFound(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := SearchSkills(reg)

	res := runSkillTool(t, tool, map[string]any{
		"query": "nonexistent",
	})

	if !strings.Contains(res, "No skills found") {
		t.Fatalf("expected 'No skills found', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// inspect_skill
// ---------------------------------------------------------------------------

func TestInspectSkill_Registered(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := InspectSkill(reg)

	res := runSkillTool(t, tool, map[string]any{
		"name": "go-testing",
	})

	if !strings.Contains(res, "Skill: go-testing") {
		t.Fatalf("expected 'Skill: go-testing', got: %s", res)
	}
	if !strings.Contains(res, "Description: How to write and run tests in Go") {
		t.Fatalf("expected description, got: %s", res)
	}
	if !strings.Contains(res, "go test ./...") {
		t.Fatalf("expected content, got: %s", res)
	}
}

func TestInspectSkill_NotFound(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := InspectSkill(reg)

	res := runSkillTool(t, tool, map[string]any{
		"name": "nonexistent",
	})

	if !strings.Contains(res, "not found") {
		t.Fatalf("expected 'not found', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// load_skill
// ---------------------------------------------------------------------------

func TestLoadSkill_LoadsAndTracks(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := LoadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"name": "go-testing"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}

	if !strings.Contains(res, "Loaded skill") {
		t.Fatalf("expected 'Loaded skill', got: %s", res)
	}

	// Check it's tracked in the session
	skills := session.ActiveSkills()
	if len(skills) != 1 || skills[0] != "go-testing" {
		t.Fatalf("expected [go-testing], got %v", skills)
	}
}

func TestLoadSkill_AlreadyLoaded(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := LoadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	session.AddActiveSkill("go-testing")

	raw, _ := json.Marshal(map[string]any{"name": "go-testing"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}

	if !strings.Contains(res, "already loaded") {
		t.Fatalf("expected 'already loaded', got: %s", res)
	}
}

func TestLoadSkill_NotFound(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := LoadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"name": "nonexistent"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}

	if !strings.Contains(res, "not found") {
		t.Fatalf("expected 'not found', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// unload_skill
// ---------------------------------------------------------------------------

func TestUnloadSkill_RemovesTracking(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := UnloadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	session.AddActiveSkill("go-testing")

	raw, _ := json.Marshal(map[string]any{"name": "go-testing"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("unload_skill: %v", err)
	}

	if !strings.Contains(res, "Unloaded skill") {
		t.Fatalf("expected 'Unloaded skill', got: %s", res)
	}

	skills := session.ActiveSkills()
	if len(skills) != 0 {
		t.Fatalf("expected empty skills, got %v", skills)
	}
}

func TestUnloadSkill_NotLoaded(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := UnloadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"name": "go-testing"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("unload_skill: %v", err)
	}

	if !strings.Contains(res, "not currently loaded") {
		t.Fatalf("expected 'not currently loaded', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// import_skill — loads a skill from a file path
// ---------------------------------------------------------------------------

func TestImportSkill_FromFile(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	// Create a skill file
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "my-custom-skill.md")
	content := `---
name: custom-skill
description: A custom skill
---
Custom instructions go here.`
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"path": skillPath})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("import_skill: %v", err)
	}

	if !strings.Contains(res, "Imported") {
		t.Fatalf("expected 'Imported', got: %s", res)
	}

	// Should be loaded in session
	skills := session.ActiveSkills()
	if len(skills) != 1 || skills[0] != "custom-skill" {
		t.Fatalf("expected [custom-skill], got %v", skills)
	}
}

func TestImportSkill_FileNotFound(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"path": "/nonexistent/path/skill.md"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("import_skill: %v", err)
	}

	if !strings.Contains(res, "does not exist") {
		t.Fatalf("expected 'does not exist', got: %s", res)
	}
}

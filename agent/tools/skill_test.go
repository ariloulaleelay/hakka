package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestSkillRegistry(t *testing.T) *agent.SkillRegistry {
	t.Helper()
	regDir := t.TempDir()

	skills := map[string]string{
		"go-testing": `---
name: go-testing
description: How to write and run tests in Go
tags: go, testing
---
# Go Testing
Run tests with: go test ./...`,
		"deploy-app": `---
name: deploy-app
description: Deploy the application to production
tags: deploy, production
---
# Deploy
Steps to deploy the application.`,
	}

	for name, content := range skills {
		dir := filepath.Join(regDir, name)
		os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := agent.NewSkillRegistry()
	if _, err := reg.AddDir(regDir); err != nil {
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

	res := runSkillTool(t, tool, map[string]any{"query": "testing"})

	if !strings.Contains(res, "go-testing") {
		t.Fatalf("expected 'go-testing', got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}
}

func TestSearchSkills_NotFound(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := SearchSkills(reg)

	res := runSkillTool(t, tool, map[string]any{"query": "nonexistent"})

	if !strings.Contains(res, "No skills found") {
		t.Fatalf("expected 'No skills found', got: %s", res)
	}
}

func TestSearchSkills_EmptyReturnsAll(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := SearchSkills(reg)

	res := runSkillTool(t, tool, map[string]any{"query": ""})

	if !strings.Contains(res, "go-testing") || !strings.Contains(res, "deploy-app") {
		t.Fatalf("expected both skills, got: %s", res)
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

	skills := session.ActiveSkills()
	if len(skills) != 1 || skills[0] != "go-testing" {
		t.Fatalf("expected [go-testing], got %v", skills)
	}
}

func TestLoadSkill_AlreadyLoaded(t *testing.T) {
	reg := newTestSkillRegistry(t)
	tool := LoadSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	session.AddActiveSkill(context.Background(), "go-testing")

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
	session.AddActiveSkill(context.Background(), "go-testing")

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
// import_skill — registers only, never loads
// ---------------------------------------------------------------------------

func TestImportSkill_FromFile(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "custom-skill")
	os.MkdirAll(skillDir, 0o755)
	skillPath := filepath.Join(skillDir, "SKILL.md")
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

	if !strings.Contains(res, "Registered") {
		t.Fatalf("expected 'Registered', got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}

	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if reg.Get("custom-skill") == nil {
		t.Fatal("expected custom-skill in registry")
	}
}

func TestImportSkill_FileNotFound(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"path": "/nonexistent/SKILL.md"})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("import_skill: %v", err)
	}

	if !strings.Contains(res, "does not exist") {
		t.Fatalf("expected 'does not exist', got: %s", res)
	}
}

func TestImportSkill_FromSkillDirectory(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	os.MkdirAll(skillDir, 0o755)
	content := `---
name: my-skill
description: A skill from a directory
---
Skill instructions.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"path": skillDir})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("import_skill: %v", err)
	}

	if !strings.Contains(res, "Registered") {
		t.Fatalf("expected 'Registered', got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}

	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if reg.Get("my-skill") == nil {
		t.Fatal("expected my-skill in registry")
	}
}

func TestImportSkill_FromRegistryDir(t *testing.T) {
	reg := agent.NewSkillRegistry()
	tool := ImportSkill(reg)

	tmpDir := t.TempDir()

	for _, name := range []string{"skill-a", "skill-b"} {
		dir := filepath.Join(tmpDir, name)
		os.MkdirAll(dir, 0o755)
		content := fmt.Sprintf(`---
name: %s
description: Skill %s
---
Content for %s.`, name, name, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(tmpDir, "not-a-skill"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte("not a skill"), 0o644)

	session := agent.NewSession("test", "test-prompt")
	raw, _ := json.Marshal(map[string]any{"path": tmpDir})
	ctx := event.ContextWithSessionView(context.Background(), session)
	res, err := tool.Handler(ctx, raw)
	if err != nil {
		t.Fatalf("import_skill: %v", err)
	}

	if !strings.Contains(res, "Registered 2 skill") {
		t.Fatalf("expected 'Registered 2 skill', got: %s", res)
	}
	if !strings.Contains(res, "skill-a") || !strings.Contains(res, "skill-b") {
		t.Fatalf("expected skill-a and skill-b, got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}

	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if reg.Get("skill-a") == nil || reg.Get("skill-b") == nil {
		t.Fatal("expected both skills in registry")
	}
}

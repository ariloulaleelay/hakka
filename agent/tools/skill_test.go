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

// newTestSkillSession creates a session whose ${cwd}/skills contains
// go-testing and deploy-app, so the session registry auto-imports them.
func newTestSkillSession(t *testing.T) *agent.Session {
	t.Helper()
	cwd := t.TempDir()

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
		dir := filepath.Join(cwd, "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	session := agent.NewSession("test", "test-prompt")
	if err := session.SetClientCWD(context.Background(), cwd); err != nil {
		t.Fatal(err)
	}
	return session
}

// newEmptySkillSession creates a session with an empty cwd (no skills).
func newEmptySkillSession(t *testing.T) *agent.Session {
	t.Helper()
	session := agent.NewSession("test", "test-prompt")
	if err := session.SetClientCWD(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return session
}

func runSkillToolOn(t *testing.T, session *agent.Session, tool agent.Tool, args any) string {
	t.Helper()
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
	tool := SearchSkills()
	res := runSkillToolOn(t, newTestSkillSession(t), tool, map[string]any{"query": "testing"})

	if !strings.Contains(res, "go-testing") {
		t.Fatalf("expected 'go-testing', got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}
}

func TestSearchSkills_NotFound(t *testing.T) {
	tool := SearchSkills()
	res := runSkillToolOn(t, newTestSkillSession(t), tool, map[string]any{"query": "nonexistent"})

	if !strings.Contains(res, "No skills found") {
		t.Fatalf("expected 'No skills found', got: %s", res)
	}
}

func TestSearchSkills_EmptyReturnsAll(t *testing.T) {
	tool := SearchSkills()
	res := runSkillToolOn(t, newTestSkillSession(t), tool, map[string]any{"query": ""})

	if !strings.Contains(res, "go-testing") || !strings.Contains(res, "deploy-app") {
		t.Fatalf("expected both skills, got: %s", res)
	}
}

func TestSearchSkills_NoSessionInContext(t *testing.T) {
	tool := SearchSkills()
	raw, _ := json.Marshal(map[string]any{"query": ""})
	if _, err := tool.Handler(context.Background(), raw); err == nil {
		t.Fatal("expected error when no session is available in context")
	}
}

// ---------------------------------------------------------------------------
// load_skill
// ---------------------------------------------------------------------------

func TestLoadSkill_LoadsAndTracks(t *testing.T) {
	tool := LoadSkill()

	session := newTestSkillSession(t)
	res := runSkillToolOn(t, session, tool, map[string]any{"name": "go-testing"})

	if !strings.Contains(res, "Loaded skill") {
		t.Fatalf("expected 'Loaded skill', got: %s", res)
	}

	skills := session.ActiveSkills()
	if len(skills) != 1 || skills[0] != "go-testing" {
		t.Fatalf("expected [go-testing], got %v", skills)
	}
}

func TestLoadSkill_AlreadyLoaded(t *testing.T) {
	tool := LoadSkill()

	session := newTestSkillSession(t)
	session.AddActiveSkill(context.Background(), "go-testing")

	res := runSkillToolOn(t, session, tool, map[string]any{"name": "go-testing"})
	if !strings.Contains(res, "already loaded") {
		t.Fatalf("expected 'already loaded', got: %s", res)
	}
}

func TestLoadSkill_NotFound(t *testing.T) {
	tool := LoadSkill()

	res := runSkillToolOn(t, newTestSkillSession(t), tool, map[string]any{"name": "nonexistent"})
	if !strings.Contains(res, "not found") {
		t.Fatalf("expected 'not found', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// unload_skill
// ---------------------------------------------------------------------------

func TestUnloadSkill_RemovesTracking(t *testing.T) {
	tool := UnloadSkill()

	session := newTestSkillSession(t)
	session.AddActiveSkill(context.Background(), "go-testing")

	res := runSkillToolOn(t, session, tool, map[string]any{"name": "go-testing"})
	if !strings.Contains(res, "Unloaded skill") {
		t.Fatalf("expected 'Unloaded skill', got: %s", res)
	}

	skills := session.ActiveSkills()
	if len(skills) != 0 {
		t.Fatalf("expected empty skills, got %v", skills)
	}
}

func TestUnloadSkill_NotLoaded(t *testing.T) {
	tool := UnloadSkill()

	res := runSkillToolOn(t, newTestSkillSession(t), tool, map[string]any{"name": "go-testing"})
	if !strings.Contains(res, "not currently loaded") {
		t.Fatalf("expected 'not currently loaded', got: %s", res)
	}
}

// ---------------------------------------------------------------------------
// import_skill — registers into the CURRENT session only, persists paths
// ---------------------------------------------------------------------------

func makeImportSkillDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`---
name: %s
description: A custom skill
---
Custom instructions for %s.`, name, name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImportSkill_FromFile(t *testing.T) {
	tool := ImportSkill()

	skillDir := makeImportSkillDir(t, t.TempDir(), "custom-skill")
	skillPath := filepath.Join(skillDir, "SKILL.md")

	session := newEmptySkillSession(t)
	res := runSkillToolOn(t, session, tool, map[string]any{"path": skillPath})

	if !strings.Contains(res, "Registered") {
		t.Fatalf("expected 'Registered', got: %s", res)
	}
	if !strings.Contains(res, "Use load_skill") {
		t.Fatalf("expected 'Use load_skill', got: %s", res)
	}

	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if session.SkillRegistry().Get("custom-skill") == nil {
		t.Fatal("expected custom-skill in the session registry")
	}
	if len(session.Read().SkillPaths) != 1 {
		t.Fatalf("expected 1 persisted skill path, got %v", session.Read().SkillPaths)
	}
}

func TestImportSkill_FileNotFound(t *testing.T) {
	tool := ImportSkill()

	res := runSkillToolOn(t, newEmptySkillSession(t), tool, map[string]any{"path": "/nonexistent/SKILL.md"})
	if !strings.Contains(res, "does not exist") {
		t.Fatalf("expected 'does not exist', got: %s", res)
	}
}

func TestImportSkill_FromSkillDirectory(t *testing.T) {
	tool := ImportSkill()

	skillDir := makeImportSkillDir(t, t.TempDir(), "my-skill")

	session := newEmptySkillSession(t)
	res := runSkillToolOn(t, session, tool, map[string]any{"path": skillDir})

	if !strings.Contains(res, "Registered") {
		t.Fatalf("expected 'Registered', got: %s", res)
	}
	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if session.SkillRegistry().Get("my-skill") == nil {
		t.Fatal("expected my-skill in the session registry")
	}
}

func TestImportSkill_FromRegistryDir(t *testing.T) {
	tool := ImportSkill()

	regDir := t.TempDir()
	makeImportSkillDir(t, regDir, "skill-a")
	makeImportSkillDir(t, regDir, "skill-b")
	os.MkdirAll(filepath.Join(regDir, "not-a-skill"), 0o755)
	os.WriteFile(filepath.Join(regDir, "notes.txt"), []byte("not a skill"), 0o644)

	session := newEmptySkillSession(t)
	res := runSkillToolOn(t, session, tool, map[string]any{"path": regDir})

	if !strings.Contains(res, "Registered 2 skill") {
		t.Fatalf("expected 'Registered 2 skill', got: %s", res)
	}
	if !strings.Contains(res, "skill-a") || !strings.Contains(res, "skill-b") {
		t.Fatalf("expected skill-a and skill-b, got: %s", res)
	}
	if len(session.ActiveSkills()) != 0 {
		t.Fatalf("expected 0 active skills, got %v", session.ActiveSkills())
	}
	if session.SkillRegistry().Get("skill-a") == nil || session.SkillRegistry().Get("skill-b") == nil {
		t.Fatal("expected both skills in the session registry")
	}
	if len(session.Read().SkillPaths) != 2 {
		t.Fatalf("expected 2 persisted skill paths, got %v", session.Read().SkillPaths)
	}
}

func TestImportSkill_SessionScoped(t *testing.T) {
	tool := ImportSkill()

	skillDir := makeImportSkillDir(t, t.TempDir(), "secret-skill")
	sessionA := newEmptySkillSession(t)
	sessionB := newEmptySkillSession(t)

	res := runSkillToolOn(t, sessionA, tool, map[string]any{"path": filepath.Join(skillDir, "SKILL.md")})
	if !strings.Contains(res, "Registered") {
		t.Fatalf("expected 'Registered', got: %s", res)
	}

	if sessionA.SkillRegistry().Get("secret-skill") == nil {
		t.Fatal("session A must see the imported skill")
	}
	if sessionB.SkillRegistry().Get("secret-skill") != nil {
		t.Fatal("session B must NOT see skills imported into session A")
	}
	if len(sessionB.Read().SkillPaths) != 0 {
		t.Fatalf("session B must have empty SkillPaths, got %v", sessionB.Read().SkillPaths)
	}
}

func TestImportSkill_NoSessionInContext(t *testing.T) {
	tool := ImportSkill()
	raw, _ := json.Marshal(map[string]any{"path": "/tmp"})
	if _, err := tool.Handler(context.Background(), raw); err == nil {
		t.Fatal("expected error when no session is available in context")
	}
}

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillRegistry_Add(t *testing.T) {
	// Create a temp skill file
	tmpDir := t.TempDir()
	skillPath := filepath.Join(tmpDir, "go-testing.md")
	content := `---
name: go-testing
description: How to write and run tests in Go
tags: go, testing, conventions
---

# Go Testing Skills

Run tests with:
  go test ./...
`
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewSkillRegistry()
	if err := reg.Add(skillPath); err != nil {
		t.Fatalf("Add: %v", err)
	}

	skill := reg.Get("go-testing")
	if skill == nil {
		t.Fatal("expected skill 'go-testing' to be registered")
	}
	if skill.Description != "How to write and run tests in Go" {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
	if len(skill.Tags) != 3 || skill.Tags[0] != "go" {
		t.Fatalf("unexpected tags: %v", skill.Tags)
	}
}

func TestSkillRegistry_Add_FilenameFallback(t *testing.T) {
	tmpDir := t.TempDir()
	skillPath := filepath.Join(tmpDir, "deploy-app.md")
	content := `# Deploy App

Steps to deploy the application.
`
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewSkillRegistry()
	if err := reg.Add(skillPath); err != nil {
		t.Fatalf("Add: %v", err)
	}

	skill := reg.Get("deploy-app")
	if skill == nil {
		t.Fatal("expected skill 'deploy-app' to be registered")
	}
}

func TestSkillRegistry_AddDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a couple of skill files
	skills := map[string]string{
		"go-testing.md": `---
name: go-testing
description: Go testing skills
tags: go
---
Content`,
		"python-packaging.md": `---
name: python-packaging
description: Python packaging skills
tags: python
---
Content`,
		"notes.txt": `not a markdown file, should be skipped`,
	}

	for name, content := range skills {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := NewSkillRegistry()
	count, err := reg.AddDir(tmpDir)
	if err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 skills, got %d", count)
	}

	if reg.Get("go-testing") == nil {
		t.Fatal("expected go-testing skill")
	}
	if reg.Get("python-packaging") == nil {
		t.Fatal("expected python-packaging skill")
	}
}

func TestSkillRegistry_Search(t *testing.T) {
	reg := NewSkillRegistry()

	// Manually add skills (simulating registration)
	reg.skills["go-testing"] = &Skill{
		Name:        "go-testing",
		Description: "How to write and run tests in Go",
		Tags:        []string{"go", "testing"},
	}
	reg.skills["go-deploy"] = &Skill{
		Name:        "go-deploy",
		Description: "Deploy Go services to production",
		Tags:        []string{"go", "deploy", "production"},
	}
	reg.skills["python-deploy"] = &Skill{
		Name:        "python-deploy",
		Description: "Deploy Python services",
		Tags:        []string{"python", "deploy"},
	}

	// Search by name
	results := reg.Search("go-testing")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'go-testing', got %d", len(results))
	}

	// Search by description
	results = reg.Search("deploy")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'deploy', got %d: %v", len(results), results)
	}

	// Search by tag
	results = reg.Search("python")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'python', got %d", len(results))
	}

	// Empty query returns all
	results = reg.Search("")
	if len(results) != 3 {
		t.Fatalf("expected 3 results for empty query, got %d", len(results))
	}
}

func TestSkillRegistry_ReadContent(t *testing.T) {
	tmpDir := t.TempDir()
	skillPath := filepath.Join(tmpDir, "my-skill.md")
	bodyContent := "Actual skill content here.\n\nWith multiple lines."
	content := `---
name: my-skill
description: Test skill
tags: test
---
` + bodyContent

	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewSkillRegistry()
	if err := reg.Add(skillPath); err != nil {
		t.Fatalf("Add: %v", err)
	}

	body, err := reg.ReadContent("my-skill")
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if body != bodyContent {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSkillRegistry_DuplicateName(t *testing.T) {
	tmpDir := t.TempDir()
	skill1 := filepath.Join(tmpDir, "same-name.md")
	skill2 := filepath.Join(tmpDir, "subdir", "same-name.md")

	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0o755)

	for _, p := range []string{skill1, skill2} {
		content := `---
name: same-name
description: ` + p + `
---
Content`
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := NewSkillRegistry()
	if err := reg.Add(skill1); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	// Second add with same name should fail
	if err := reg.Add(skill2); err == nil {
		t.Fatal("expected error for duplicate skill name")
	} else {
		if !strings.Contains(err.Error(), "duplicate name") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestParseSkillFile_NoFrontmatter(t *testing.T) {
	content := "# Just a heading\n\nSome content"
	name, desc, tags := ParseSkillFile("/fake/path/test-skill.md", content)
	if name != "test-skill" {
		t.Fatalf("expected name 'test-skill', got %q", name)
	}
	if desc != "" {
		t.Fatalf("expected empty description, got %q", desc)
	}
	if len(tags) != 0 {
		t.Fatalf("expected empty tags, got %v", tags)
	}
}

func TestParseSkillFile_WithFrontmatter(t *testing.T) {
	content := `---
name: my-cool-skill
description: Does something cool
tags: go, testing, automations
---
# Actual skill content`
	name, desc, tags := ParseSkillFile("/fake/path/file.md", content)
	if name != "my-cool-skill" {
		t.Fatalf("expected name 'my-cool-skill', got %q", name)
	}
	if desc != "Does something cool" {
		t.Fatalf("expected description 'Does something cool', got %q", desc)
	}
	if len(tags) != 3 || tags[0] != "go" || tags[1] != "testing" {
		t.Fatalf("unexpected tags: %v", tags)
	}
}

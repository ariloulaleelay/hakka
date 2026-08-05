package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeSkillDir creates a skill directory with SKILL.md and returns (dirPath, skillPath).
func makeSkillDir(t *testing.T, parent, name, content string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// Add — directory-based skill via SKILL.md path
// ---------------------------------------------------------------------------

func TestSkillRegistry_Add(t *testing.T) {
	tmpDir := t.TempDir()

	dir := makeSkillDir(t, tmpDir, "go-testing", `---
name: go-testing
description: How to write and run tests in Go
tags: go, testing, conventions
---

# Go Testing Skills

Run tests with:
  go test ./...
`)

	reg := NewSkillRegistry()
	if err := reg.Add(filepath.Join(dir, "SKILL.md")); err != nil {
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
	if skill.DirPath != dir {
		t.Fatalf("expected DirPath %q, got %q", dir, skill.DirPath)
	}
}

func TestSkillRegistry_Add_NameFromDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// No name in frontmatter — should derive from directory name
	dir := makeSkillDir(t, tmpDir, "deploy-app", `# Deploy App

Steps to deploy the application.
`)

	reg := NewSkillRegistry()
	if err := reg.Add(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	skill := reg.Get("deploy-app")
	if skill == nil {
		t.Fatal("expected skill 'deploy-app' to be registered")
	}
}

func TestSkillRegistry_Add_NameMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	// Frontmatter name doesn't match directory name — should fail
	dir := makeSkillDir(t, tmpDir, "real-dir", `---
name: wrong-name
description: Test
---
Content
`)

	reg := NewSkillRegistry()
	if err := reg.Add(filepath.Join(dir, "SKILL.md")); err == nil {
		t.Fatal("expected error for name mismatch")
	} else {
		if !strings.Contains(err.Error(), "does not match directory name") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestSkillRegistry_Add_NotSKILLmd(t *testing.T) {
	tmpDir := t.TempDir()

	// Not a SKILL.md file — should fail
	os.MkdirAll(filepath.Join(tmpDir, "myskill"), 0o755)
	fpath := filepath.Join(tmpDir, "myskill", "README.md")
	if err := os.WriteFile(fpath, []byte("# Not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewSkillRegistry()
	if err := reg.Add(fpath); err == nil {
		t.Fatal("expected error for non-SKILL.md file")
	} else {
		if !strings.Contains(err.Error(), "must be a SKILL.md file") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// AddDir — scans a skill registry directory
// ---------------------------------------------------------------------------

func TestSkillRegistry_AddDir(t *testing.T) {
	tmpDir := t.TempDir()

	makeSkillDir(t, tmpDir, "go-testing", `---
name: go-testing
description: Go testing skills
tags: go
---
Content`)
	makeSkillDir(t, tmpDir, "python-packaging", `---
name: python-packaging
description: Python packaging skills
tags: python
---
Content`)
	// A subdirectory without SKILL.md — should be skipped
	os.MkdirAll(filepath.Join(tmpDir, "not-a-skill"), 0o755)
	// A regular file — should be skipped
	os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte("not a skill"), 0o644)

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

func TestSkillRegistry_AddDir_AllFields(t *testing.T) {
	tmpDir := t.TempDir()

	makeSkillDir(t, tmpDir, "pdf-processing", `---
name: pdf-processing
description: Extract PDF text, fill forms, merge files. Use when handling PDFs.
license: Apache-2.0
compatibility: Requires Python 3.10+
metadata:
  author: example-org
  version: "1.0"
allowed-tools: Bash(git:*) Read
---

# PDF Processing

Instructions for processing PDF files.

See [reference](references/REFERENCE.md) for details.
`)

	// Create a references subdirectory
	refsDir := filepath.Join(tmpDir, "pdf-processing", "references")
	os.MkdirAll(refsDir, 0o755)
	os.WriteFile(filepath.Join(refsDir, "REFERENCE.md"), []byte("# Reference\n\nDetailed info."), 0o644)

	reg := NewSkillRegistry()
	count, err := reg.AddDir(tmpDir)
	if err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 skill, got %d", count)
	}

	skill := reg.Get("pdf-processing")
	if skill == nil {
		t.Fatal("expected skill 'pdf-processing' to be registered")
	}
	if skill.Description != "Extract PDF text, fill forms, merge files. Use when handling PDFs." {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
	if skill.License != "Apache-2.0" {
		t.Fatalf("unexpected license: %q", skill.License)
	}
	if skill.Compatibility != "Requires Python 3.10+" {
		t.Fatalf("unexpected compatibility: %q", skill.Compatibility)
	}
	if skill.AllowedTools != "Bash(git:*) Read" {
		t.Fatalf("unexpected allowed-tools: %q", skill.AllowedTools)
	}
	if skill.Metadata == nil || skill.Metadata["author"] != "example-org" || skill.Metadata["version"] != "1.0" {
		t.Fatalf("unexpected metadata: %v", skill.Metadata)
	}
}

// ---------------------------------------------------------------------------
// ReadSkillFile — read referenced files from within skill dir
// ---------------------------------------------------------------------------

func TestSkillRegistry_ReadSkillFile(t *testing.T) {
	tmpDir := t.TempDir()

	makeSkillDir(t, tmpDir, "my-skill", `---
name: my-skill
description: Test skill
---
# Skill content
`)
	os.MkdirAll(filepath.Join(tmpDir, "my-skill", "references"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "my-skill", "references", "guide.md"), []byte("# Guide\n\nHelpful guide content."), 0o644)

	reg := NewSkillRegistry()
	if _, err := reg.AddDir(tmpDir); err != nil {
		t.Fatalf("AddDir: %v", err)
	}

	content, err := reg.ReadSkillFile("my-skill", "references/guide.md")
	if err != nil {
		t.Fatalf("ReadSkillFile: %v", err)
	}
	if content != "# Guide\n\nHelpful guide content." {
		t.Fatalf("unexpected content: %q", content)
	}

	_, err = reg.ReadSkillFile("my-skill", "nonexistent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// Name validation
// ---------------------------------------------------------------------------

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"pdf-processing", false},
		{"data-analysis", false},
		{"code-review", false},
		{"a", false},
		{"abc", false},
		{"a1-b2-c3", false},
		{"", true},
		{"PDF-Processing", true},
		{"-pdf", true},
		{"pdf-", true},
		{"pdf--processing", true},
		{"pdf_processing", true},
		{"pdf processing", true},
		{strings.Repeat("a", 65), true},
		{strings.Repeat("a", 64), false},
	}

	for _, tt := range tests {
		err := ValidateSkillName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateSkillName(%q) error=%v, wantErr=%v", tt.name, err, tt.wantErr)
		}
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestSkillRegistry_Search(t *testing.T) {
	reg := NewSkillRegistry()

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

	results := reg.Search("go-testing")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'go-testing', got %d", len(results))
	}

	results = reg.Search("deploy")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'deploy', got %d: %v", len(results), results)
	}

	results = reg.Search("python")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'python', got %d", len(results))
	}

	results = reg.Search("")
	if len(results) != 3 {
		t.Fatalf("expected 3 results for empty query, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// ReadContent
// ---------------------------------------------------------------------------

func TestSkillRegistry_ReadContent(t *testing.T) {
	tmpDir := t.TempDir()
	bodyContent := "Actual skill content here.\n\nWith multiple lines."

	makeSkillDir(t, tmpDir, "my-skill", `---
name: my-skill
description: Test skill
tags: test
---
`+bodyContent)

	reg := NewSkillRegistry()
	if err := reg.Add(filepath.Join(tmpDir, "my-skill", "SKILL.md")); err != nil {
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

// ---------------------------------------------------------------------------
// Duplicate name
// ---------------------------------------------------------------------------

func TestSkillRegistry_DuplicateName(t *testing.T) {
	tmpDir := t.TempDir()

	makeSkillDir(t, tmpDir, "same-name", `---
name: same-name
description: First
---
Content`)
	makeSkillDir(t, filepath.Join(tmpDir, "nested"), "same-name", `---
name: same-name
description: Second
---
Content`)

	reg := NewSkillRegistry()
	if err := reg.Add(filepath.Join(tmpDir, "same-name", "SKILL.md")); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if err := reg.Add(filepath.Join(tmpDir, "nested", "same-name", "SKILL.md")); err == nil {
		t.Fatal("expected error for duplicate skill name")
	} else {
		if !strings.Contains(err.Error(), "duplicate name") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// ParseSkillFile — no frontmatter
// ---------------------------------------------------------------------------

func TestParseSkillFile_NoFrontmatter(t *testing.T) {
	content := "# Just a heading\n\nSome content"
	fm := ParseSkillFile("/fake/path/test-skill/SKILL.md", content)
	if fm.Name != "" {
		t.Fatalf("expected empty name, got %q", fm.Name)
	}
	if fm.Description != "" {
		t.Fatalf("expected empty description, got %q", fm.Description)
	}
	if len(fm.Tags) != 0 {
		t.Fatalf("expected empty tags, got %v", fm.Tags)
	}
}

// ---------------------------------------------------------------------------
// ParseSkillFile — with frontmatter fields
// ---------------------------------------------------------------------------

func TestParseSkillFile_WithFrontmatter(t *testing.T) {
	content := `---
name: my-cool-skill
description: Does something cool
tags: go, testing, automations
---
# Actual skill content`
	fm := ParseSkillFile("/fake/path/my-cool-skill/SKILL.md", content)
	if fm.Name != "my-cool-skill" {
		t.Fatalf("expected name 'my-cool-skill', got %q", fm.Name)
	}
	if fm.Description != "Does something cool" {
		t.Fatalf("expected description 'Does something cool', got %q", fm.Description)
	}
	if len(fm.Tags) != 3 || fm.Tags[0] != "go" || fm.Tags[1] != "testing" {
		t.Fatalf("unexpected tags: %v", fm.Tags)
	}
}

func TestParseSkillFile_AllFields(t *testing.T) {
	content := `---
name: pdf-tool
description: Process PDF files
license: MIT
compatibility: Requires python 3.10+
metadata:
  author: my-org
  version: "2.1"
allowed-tools: Bash(git:*) Read
tags: pdf, conversion
---
# PDF Tool

Full instructions here.
`
	fm := ParseSkillFile("/fake/pdf-tool/SKILL.md", content)
	if fm.Name != "pdf-tool" {
		t.Fatalf("expected name 'pdf-tool', got %q", fm.Name)
	}
	if fm.Description != "Process PDF files" {
		t.Fatalf("expected description, got %q", fm.Description)
	}
	if fm.License != "MIT" {
		t.Fatalf("expected license 'MIT', got %q", fm.License)
	}
	if fm.Compatibility != "Requires python 3.10+" {
		t.Fatalf("expected compatibility, got %q", fm.Compatibility)
	}
	if fm.AllowedTools != "Bash(git:*) Read" {
		t.Fatalf("expected allowed-tools, got %q", fm.AllowedTools)
	}
	if fm.Metadata == nil || fm.Metadata["author"] != "my-org" || fm.Metadata["version"] != "2.1" {
		t.Fatalf("unexpected metadata: %v", fm.Metadata)
	}
	if len(fm.Tags) != 2 || fm.Tags[0] != "pdf" || fm.Tags[1] != "conversion" {
		t.Fatalf("unexpected tags: %v", fm.Tags)
	}
}

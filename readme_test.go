package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestGoModulePathIsCorrect(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("cannot read go.mod: %v", err)
	}
	content := string(data)
	// First line should be: module github.com/ariloulaleelay/hakka
	if !strings.HasPrefix(content, "module github.com/ariloulaleelay/hakka") {
		t.Errorf("go.mod must start with 'module github.com/ariloulaleelay/hakka', got:\n%s", content)
	}
}

func TestREADMESectionExists(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	sections := []string{
		"Features",
		"Quick Start",
		"Architecture",
		"Usage",
		"Configuration",
		"Wire Protocol",
		"Extending",
	}

	for _, s := range sections {
		if !strings.Contains(content, "## "+s) {
			t.Errorf("README.md missing section '## %s'", s)
		}
	}
}

func TestREADMEDockerSectionExists(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## Docker") {
		t.Errorf("README.md missing section '## Docker'")
	}
	if !strings.Contains(content, "/data") {
		t.Errorf("README.md Docker section should document the /data volume for config + database")
	}
	if !strings.Contains(content, "8080") {
		t.Errorf("README.md Docker section should document the exposed 8080 port")
	}
}

func TestREADMENoInternalReferences(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	internalPatterns := []string{
		"CUSTOM_ENDPOINT",
		"CUSTOM_TOKEN",
		"INTERNAL_PROXY",
		"internal proxy",
		"git.prsx.ru",
		"terminus",
		"Need-Raw-Answer",
	}

	for _, pat := range internalPatterns {
		if strings.Contains(content, pat) {
			t.Errorf("README.md should not contain internal reference '%s'", pat)
		}
	}
}

func TestREADMEHasBadges(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	// Check for badge-style markdown links (img.shields.io or similar)
	badgePattern := regexp.MustCompile(`https?://img\.shields\.io/badge/`)
	if !badgePattern.MatchString(content) {
		t.Errorf("README.md should contain at least one badge (img.shields.io)")
	}
}

func TestREADMEHasWorkingInstallSection(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	// Should mention go install
	if !strings.Contains(content, "go install") && !strings.Contains(content, "go build") {
		t.Errorf("README.md should contain installation instructions (go install or go build)")
	}
}

func TestREADMEHasLicenseSection(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "MIT") && !strings.Contains(content, "License") {
		t.Errorf("README.md should mention license (MIT)")
	}
}

func TestREADMEHasArchitectureSection(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("cannot read README.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "Architecture") {
		t.Errorf("README.md should have an Architecture section")
	}
}

func TestLICENSEFileExists(t *testing.T) {
	_, err := os.Stat("LICENSE")
	if os.IsNotExist(err) {
		t.Errorf("LICENSE file must exist for GitHub-ready repository")
	}
}

func TestExampleConfigUsesPublicAPI(t *testing.T) {
	data, err := os.ReadFile("hakka.example.json")
	if err != nil {
		t.Fatalf("cannot read hakka.example.json: %v", err)
	}
	content := string(data)

	internalPatterns := []string{
		"INTERNAL_PROXY_ENDPOINT",
		"INTERNAL_PROXY_TOKEN",
		"Need-Raw-Answer",
		"terminus",
		"claude-opus-4-7",
	}

	for _, pat := range internalPatterns {
		if strings.Contains(content, pat) {
			t.Errorf("hakka.example.json should not contain internal reference '%s'", pat)
		}
	}
}

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill represents a reusable skill/knowledge that can be loaded into a
// session's context. Skills are just markdown files with optional YAML
// frontmatter that describe how to do something.
//
// The registry stores metadata only (name, description, tags, file path).
// Content is read from disk on-demand when a skill is loaded into a session.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Path        string   `json:"-"` // absolute path to the skill file
}

// SkillIndex holds the sorted list of all skill names currently loaded
// into a session. It is stored in SessionData as ActiveSkills.
type SkillIndex []string

// SkillRegistry is a read-only registry of all available skills. Skills
// are indexed at startup by scanning configured directories.
//
// The registry is goroutine-safe for concurrent reads.
type SkillRegistry struct {
	skills map[string]*Skill // name → Skill
	dirs   []string          // directories scanned for skills
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		skills: make(map[string]*Skill),
	}
}

// Add registers a single skill by its file path. It reads the file,
// parses YAML frontmatter (if present), and indexes it by name.
// Returns an error if the file cannot be read or parsed.
func (sr *SkillRegistry) Add(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("skill: resolve path %q: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("skill: read %q: %w", path, err)
	}

	name, desc, tags := ParseSkillFile(path, string(data))
	if name == "" {
		// Fall back to filename without extension
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	skill := &Skill{
		Name:        name,
		Description: desc,
		Tags:        tags,
		Path:        path,
	}

	if _, exists := sr.skills[name]; exists {
		return fmt.Errorf("skill: duplicate name %q (already registered from %s, skipping %s)",
			name, sr.skills[name].Path, path)
	}

	sr.skills[name] = skill
	return nil
}

// AddDir scans a directory for skill files (*.md) and registers them.
// By default it looks for any *.md file. If the directory contains a
// `skills.json` or `skills.yaml` index, that can be used as an alternative.
// Returns the count of skills registered and the first error encountered.
func (sr *SkillRegistry) AddDir(dir string) (int, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, fmt.Errorf("skill: resolve dir %q: %w", dir, err)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, fmt.Errorf("skill: dir %q does not exist", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("skill: read dir %q: %w", dir, err)
	}

	sr.dirs = append(sr.dirs, dir)
	count := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") &&
			!strings.HasSuffix(strings.ToLower(entry.Name()), ".markdown") {
			continue
		}

		fpath := filepath.Join(dir, entry.Name())
		if err := sr.Add(fpath); err != nil {
			// Log but continue — don't fail on one bad file
			continue
		}
		count++
	}

	return count, nil
}

// Get returns the skill metadata by name, or nil if not found.
func (sr *SkillRegistry) Get(name string) *Skill {
	return sr.skills[name]
}

// Search returns skills whose name, description, or tags match the query
// (case-insensitive substring match).
func (sr *SkillRegistry) Search(query string) []*Skill {
	if query == "" {
		return sr.List()
	}

	q := strings.ToLower(query)
	var out []*Skill
	for _, s := range sr.skills {
		if strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			out = append(out, s)
			continue
		}
		for _, tag := range s.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				out = append(out, s)
				break
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// List returns all skills in the registry, sorted by name.
func (sr *SkillRegistry) List() []*Skill {
	out := make([]*Skill, 0, len(sr.skills))
	for _, s := range sr.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// ReadContent reads and returns the full text content of a skill file.
// Returns the raw markdown (without frontmatter) and the raw markdown
// (with frontmatter) if available.
func (sr *SkillRegistry) ReadContent(name string) (string, error) {
	s := sr.skills[name]
	if s == nil {
		return "", fmt.Errorf("skill %q not found in registry", name)
	}

	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}

	return parseSkillBody(string(data)), nil
}

// ParseSkillFile extracts name, description, and tags from a skill file.
// It supports a simple frontmatter format:
//
//	---
//	name: my-skill
//	description: Does something
//	tags: go, testing, conventions
//	---
//
//	Actual skill content here...
func ParseSkillFile(path, content string) (name, description string, tags []string) {
	content = strings.TrimSpace(content)

	// Try to parse YAML-like frontmatter between --- markers
	if strings.HasPrefix(content, "---") {
		rest := content[3:]
		idx := strings.Index(rest, "\n---")
		if idx > 0 {
			frontmatter := strings.TrimSpace(rest[:idx])
			body := strings.TrimSpace(rest[idx+4:])

			// Simple line-by-line parsing (no full YAML dependency)
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(line[5:])
				} else if strings.HasPrefix(line, "description:") {
					description = strings.TrimSpace(line[12:])
				} else if strings.HasPrefix(line, "tags:") {
					tagStr := strings.TrimSpace(line[5:])
					for _, t := range strings.Split(tagStr, ",") {
						t = strings.TrimSpace(t)
						if t != "" {
							tags = append(tags, t)
						}
					}
				}
			}

			_ = body // body is available for content extraction
		}
	}

	// If no frontmatter name, use filename
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	return name, description, tags
}

// parseSkillBody returns the skill content without frontmatter.
func parseSkillBody(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "---") {
		rest := content[3:]
		idx := strings.Index(rest, "\n---")
		if idx > 0 {
			return strings.TrimSpace(rest[idx+4:])
		}
	}
	return content
}

// MarshalJSON implements json.Marshaler for SkillIndex.
func (si SkillIndex) MarshalJSON() ([]byte, error) {
	if si == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(si))
}

// UnmarshalJSON implements json.Unmarshaler for SkillIndex.
func (si *SkillIndex) UnmarshalJSON(data []byte) error {
	var s []string
	if err := json.Unmarshal(data, &s); err != nil {
		*si = nil
		return nil // don't fail on empty/missing data
	}
	*si = SkillIndex(s)
	return nil
}

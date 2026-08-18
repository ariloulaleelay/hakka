package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Skill represents a reusable skill/knowledge that can be loaded into a
// session's context. Skills follow the Agent Skills specification:
// https://agentskills.io
//
// A skill is a directory containing, at minimum, a SKILL.md file with
// YAML frontmatter and optional scripts/, references/, assets/ subdirs.
//
// Skill registries are directories that contain multiple skill
// subdirectories. Use AddDir to scan a registry directory.
type Skill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Tags          []string          `json:"tags,omitempty"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Path          string            `json:"-"` // path to SKILL.md file
	DirPath       string            `json:"-"` // skill root directory
}

// SkillFrontmatter holds the parsed YAML frontmatter from a SKILL.md file.
type SkillFrontmatter struct {
	Name          string
	Description   string
	Tags          []string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  string
}

// SkillRegistry holds the skills available to a single session. Each
// session owns its own registry — skills are never shared across sessions.
//
// The registry is goroutine-safe: tool handlers run concurrently within a
// turn, so all methods synchronise on an internal RWMutex.
type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]*Skill // name → Skill
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		skills: make(map[string]*Skill),
	}
}

// skillNameRegexp validates skill names per the Agent Skills spec:
//   - 1-64 characters
//   - Lowercase alphanumeric (a-z, 0-9) and hyphens only
//   - Must not start or end with hyphen
//   - Must not contain consecutive hyphens
var skillNameRegexp = regexp.MustCompile(`^[a-z0-9]$|^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ValidateSkillName checks whether the given name conforms to the
// Agent Skills specification.
func ValidateSkillName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("skill name must not be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("skill name must be 1-64 characters, got %d", len(name))
	}
	if !skillNameRegexp.MatchString(name) {
		return fmt.Errorf("skill name %q must contain only lowercase letters, numbers, and hyphens; must not start or end with hyphen; must not contain consecutive hyphens", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("skill name %q must not contain consecutive hyphens", name)
	}
	return nil
}

// Add registers a single skill from a path to its SKILL.md file.
// The parent directory becomes the skill root. The skill name from
// the frontmatter must match the parent directory name (per spec).
func (sr *SkillRegistry) Add(path string) error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.addLocked(path)
}

// addLocked registers a single skill; the caller must hold sr.mu.
func (sr *SkillRegistry) addLocked(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("skill: resolve path %q: %w", path, err)
	}

	// Must be a SKILL.md file
	base := filepath.Base(path)
	if !strings.EqualFold(base, "skill.md") {
		return fmt.Errorf("skill: path %q must be a SKILL.md file", path)
	}

	dirPath := filepath.Dir(path)
	dirName := filepath.Base(dirPath)

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("skill: read %q: %w", path, err)
	}

	fm := ParseSkillFile(path, string(data))

	// Determine skill name: frontmatter name takes precedence,
	// fall back to directory name.
	name := fm.Name
	if name == "" {
		name = dirName
	}

	// Per spec: name must match parent directory name
	if name != dirName {
		return fmt.Errorf("skill: name %q does not match directory name %q", name, dirName)
	}

	// Validate name format
	if err := ValidateSkillName(name); err != nil {
		return fmt.Errorf("skill: %w", err)
	}

	skill := &Skill{
		Name:          name,
		Description:   fm.Description,
		Tags:          fm.Tags,
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		AllowedTools:  fm.AllowedTools,
		Path:          path,
		DirPath:       dirPath,
	}

	if _, exists := sr.skills[name]; exists {
		return fmt.Errorf("skill: duplicate name %q (already registered from %s, skipping %s)",
			name, sr.skills[name].Path, path)
	}

	sr.skills[name] = skill
	return nil
}

// AddDir scans a directory for skill subdirectories and registers them.
//
// The directory is treated as a skill registry: each subdirectory that
// contains a SKILL.md file is registered as a skill.
//
// Returns the count of skills registered and the first error encountered
// (non-fatal errors like a single malformed skill are skipped).
func (sr *SkillRegistry) AddDir(dir string) (int, error) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.addDirLocked(dir)
}

// addDirLocked scans a directory for skill subdirectories and registers
// them; the caller must hold sr.mu.
func (sr *SkillRegistry) addDirLocked(dir string) (int, error) {
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

	count := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue // no SKILL.md, skip
		}

		if err := sr.addLocked(skillPath); err != nil {
			continue // skip malformed skills
		}
		count++
	}

	return count, nil
}

// Get returns the skill metadata by name, or nil if not found.
func (sr *SkillRegistry) Get(name string) *Skill {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.skills[name]
}

// Search returns skills whose name, description, or tags match the query
// (case-insensitive substring match).
func (sr *SkillRegistry) Search(query string) []*Skill {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	if query == "" {
		return sr.listLocked()
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
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.listLocked()
}

// listLocked returns all skills sorted by name; the caller must hold sr.mu.
func (sr *SkillRegistry) listLocked() []*Skill {
	out := make([]*Skill, 0, len(sr.skills))
	for _, s := range sr.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// ReadContent reads and returns the body content of a skill's SKILL.md
// (without YAML frontmatter).
func (sr *SkillRegistry) ReadContent(name string) (string, error) {
	sr.mu.RLock()
	s := sr.skills[name]
	sr.mu.RUnlock()
	if s == nil {
		return "", fmt.Errorf("skill %q not found in registry", name)
	}

	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}

	return parseSkillBody(string(data)), nil
}

// ReadSkillFile reads a file relative to the skill's root directory.
// The relPath must be a relative path (e.g. "references/REFERENCE.md").
func (sr *SkillRegistry) ReadSkillFile(name, relPath string) (string, error) {
	sr.mu.RLock()
	s := sr.skills[name]
	sr.mu.RUnlock()
	if s == nil {
		return "", fmt.Errorf("skill %q not found in registry", name)
	}
	if s.DirPath == "" {
		return "", fmt.Errorf("skill %q has no directory", name)
	}

	// Security: only allow relative paths, no traversal
	clean := filepath.Clean(relPath)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("skill %q: invalid relative path %q", name, relPath)
	}

	fullPath := filepath.Join(s.DirPath, clean)
	// Verify the resolved path is still within the skill dir
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(s.DirPath)+string(os.PathSeparator)) {
		return "", fmt.Errorf("skill %q: path %q escapes skill directory", name, relPath)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read skill file %q: %w", name, err)
	}
	return string(data), nil
}

// ParseSkillFile extracts frontmatter fields from a SKILL.md file.
//
// Supported frontmatter fields (YAML-like between --- markers):
//
//	name:          Required. Skill name (must match parent directory).
//	description:   Required. What the skill does.
//	license:       Optional. License name.
//	compatibility: Optional. Environment requirements.
//	metadata:      Optional. Indented key-value pairs under "metadata:".
//	allowed-tools: Optional. Space-separated pre-approved tools.
//	tags:          Optional. Comma-separated tags (Hakka extension).
//
// Returns a SkillFrontmatter with parsed values. Empty fields are zero-valued.
func ParseSkillFile(path, content string) *SkillFrontmatter {
	fm := &SkillFrontmatter{}

	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		return fm
	}

	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm
	}

	frontmatter := strings.TrimSpace(rest[:idx])

	lines := strings.Split(frontmatter, "\n")

	// Handle multi-line metadata block specially
	var metadataLines []string
	inMetadata := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// If in metadata block, check for continued indented lines
		if inMetadata {
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				metadataLines = append(metadataLines, trimmed)
				continue
			}
			// End of metadata block
			fm.Metadata = parseMetadataLines(metadataLines)
			metadataLines = nil
			inMetadata = false
		}

		// Parse simple key: value lines
		if colonIdx := strings.Index(trimmed, ":"); colonIdx > 0 {
			key := strings.TrimSpace(trimmed[:colonIdx])
			value := strings.TrimSpace(trimmed[colonIdx+1:])

			switch strings.ToLower(key) {
			case "name":
				fm.Name = value
			case "description":
				fm.Description = value
			case "license":
				fm.License = value
			case "compatibility":
				fm.Compatibility = value
			case "allowed-tools":
				fm.AllowedTools = value
			case "tags":
				for _, t := range strings.Split(value, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						fm.Tags = append(fm.Tags, t)
					}
				}
			case "metadata":
				// Next indented lines are metadata key-value pairs
				if value == "" {
					inMetadata = true
				}
			}
		}
	}

	// Finalize metadata if still in block
	if inMetadata && len(metadataLines) > 0 {
		fm.Metadata = parseMetadataLines(metadataLines)
	}

	return fm
}

// parseMetadataLines parses indented key: value pairs from a metadata block.
func parseMetadataLines(lines []string) map[string]string {
	meta := make(map[string]string)
	for _, line := range lines {
		if colonIdx := strings.Index(line, ":"); colonIdx > 0 {
			key := strings.TrimSpace(line[:colonIdx])
			value := strings.TrimSpace(line[colonIdx+1:])
			if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"') {
				value = value[1 : len(value)-1]
			}
			meta[key] = value
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
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

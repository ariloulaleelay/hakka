package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// search_skills — search the skill registry
// ---------------------------------------------------------------------------

type searchSkillsArgs struct {
	Query string `json:"query"`
}

// SearchSkills returns a tool that searches the skill registry by name,
// description, or tags. Results include all metadata needed to decide
// whether to load a skill — no separate inspect step needed.
func SearchSkills(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("search_skills", "Search registered skills by name, description, or tags. Use this to discover what skills are available.").
		StringParam("query", "Search query — matches against skill name, description, and tags. Empty returns all.", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchSkillsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("search_skills: %w", err)
			}

			results := sr.Search(args.Query)
			if len(results) == 0 {
				return fmt.Sprintf("No skills found matching %q.", args.Query), nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Found %d skill(s) matching %q:\n", len(results), args.Query))
			for _, s := range results {
				b.WriteString(fmt.Sprintf("  %s — %s\n", s.Name, s.Description))
				if len(s.Tags) > 0 {
					b.WriteString(fmt.Sprintf("    Tags: %s\n", strings.Join(s.Tags, ", ")))
				}
				if s.License != "" {
					b.WriteString(fmt.Sprintf("    License: %s\n", s.License))
				}
				if s.Compatibility != "" {
					b.WriteString(fmt.Sprintf("    Compatibility: %s\n", s.Compatibility))
				}
				if len(s.Metadata) > 0 {
					for k, v := range s.Metadata {
						b.WriteString(fmt.Sprintf("    %s: %s\n", k, v))
					}
				}
				if s.AllowedTools != "" {
					b.WriteString(fmt.Sprintf("    Allowed tools: %s\n", s.AllowedTools))
				}
				b.WriteString("\n")
			}
			b.WriteString("Use load_skill <name> to activate a skill.")
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// load_skill — load a skill into the session
// ---------------------------------------------------------------------------

type loadSkillArgs struct {
	Name string `json:"name"`
}

// LoadSkill returns a tool that loads a registered skill into the current
// session. The skill's content becomes part of the system prompt on every
// turn until unloaded.
func LoadSkill(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("load_skill", "Load a registered skill into the current session. The skill's instructions become part of your system prompt on every turn. Use unload_skill to remove it when no longer needed.").
		StringParam("name", "Name of the skill to load", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args loadSkillArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("load_skill: %w", err)
			}
			if args.Name == "" {
				return "Error: name is required", nil
			}

			skill := sr.Get(args.Name)
			if skill == nil {
				return fmt.Sprintf("Error: skill %q not found. Use search_skills to discover available skills.", args.Name), nil
			}

			session, ok := event.SessionViewFromContext(ctx).(agent.SessionView)
			if !ok || session == nil {
				return "", fmt.Errorf("load_skill: session not available in context")
			}

			// Check if already loaded
			for _, loaded := range session.ActiveSkills() {
				if loaded == args.Name {
					return fmt.Sprintf("Skill %q is already loaded.", args.Name), nil
				}
			}

			// Verify content is readable before committing
			if _, err := sr.ReadContent(args.Name); err != nil {
				return "", fmt.Errorf("load_skill: %w", err)
			}

			if err := session.AddActiveSkill(ctx, args.Name); err != nil {
				return "", fmt.Errorf("load_skill: %w", err)
			}
			return fmt.Sprintf("Loaded skill %q. It is now part of your system prompt on every turn. Use unload_skill %q to remove it when no longer needed.", args.Name, args.Name), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// unload_skill — unload a skill from the session
// ---------------------------------------------------------------------------

type unloadSkillArgs struct {
	Name string `json:"name"`
}

// UnloadSkill returns a tool that removes a loaded skill from the current
// session, freeing context.
func UnloadSkill(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("unload_skill", "Remove a loaded skill from the current session to free context. The skill's instructions will no longer appear in your system prompt.").
		StringParam("name", "Name of the skill to unload", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args unloadSkillArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("unload_skill: %w", err)
			}
			if args.Name == "" {
				return "Error: name is required", nil
			}

			session, ok := event.SessionViewFromContext(ctx).(agent.SessionView)
			if !ok || session == nil {
				return "", fmt.Errorf("unload_skill: session not available in context")
			}

			// Check if loaded
			found := false
			for _, loaded := range session.ActiveSkills() {
				if loaded == args.Name {
					found = true
					break
				}
			}
			if !found {
				return fmt.Sprintf("Skill %q is not currently loaded.", args.Name), nil
			}

			if err := session.RemoveActiveSkill(ctx, args.Name); err != nil {
				return "", fmt.Errorf("unload_skill: %w", err)
			}
			return fmt.Sprintf("Unloaded skill %q. It will no longer appear in your system prompt.", args.Name), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// import_skill — load skill(s) from a path
// ---------------------------------------------------------------------------

type importSkillArgs struct {
	Path string `json:"path"`
}

// ImportSkill returns a tool that registers skills from a path into the
// registry. It never loads skills into the session — use load_skill for that.
//
// Three forms are accepted:
//  1. A SKILL.md file → registers that single skill.
//  2. A skill directory (containing SKILL.md) → registers that single skill.
//  3. A registry directory (subdirs each with SKILL.md) → registers all skills.
func ImportSkill(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("import_skill", "Register skill(s) from a path into the registry. Does NOT load into session — use load_skill to activate. Accepts: a SKILL.md file, a single skill directory, or a registry directory (multiple skills).").
		StringParam("path", "Path to a SKILL.md file, a skill directory, or a registry directory", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args importSkillArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("import_skill: %w", err)
			}
			if args.Path == "" {
				return "Error: path is required", nil
			}

			// Resolve the path relative to CWD
			absPath := args.Path
			if !filepath.IsAbs(absPath) {
				cwd := event.CWDFromContext(ctx)
				if cwd != "" {
					absPath = filepath.Join(cwd, absPath)
				}
			}
			absPath, err := filepath.Abs(absPath)
			if err != nil {
				return "", fmt.Errorf("import_skill: %w", err)
			}

			info, err := os.Stat(absPath)
			if os.IsNotExist(err) {
				return fmt.Sprintf("Error: path %q does not exist.", args.Path), nil
			}
			if err != nil {
				return "", fmt.Errorf("import_skill: %w", err)
			}

			// Case 1: path is a file → must be SKILL.md → register one
			if !info.IsDir() {
				return registerOneSkill(sr, absPath)
			}

			// Case 2: directory contains SKILL.md → single skill dir → register one
			skillMDPath := filepath.Join(absPath, "SKILL.md")
			if _, err := os.Stat(skillMDPath); err == nil {
				return registerOneSkill(sr, skillMDPath)
			}

			// Case 3: registry directory → scan for subdirs with SKILL.md → register all
			entries, err := os.ReadDir(absPath)
			if err != nil {
				return "", fmt.Errorf("import_skill: read dir: %w", err)
			}

			var registered []string
			var errors []string
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				sp := filepath.Join(absPath, entry.Name(), "SKILL.md")
				if _, err := os.Stat(sp); err != nil {
					continue
				}
				if err := sr.Add(sp); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", entry.Name(), err))
					continue
				}
				skill := sr.Get(entry.Name())
				name := entry.Name()
				if skill != nil {
					name = skill.Name
				}
				registered = append(registered, name)
			}

			if len(registered) == 0 {
				if len(errors) > 0 {
					return fmt.Sprintf("Error: no skills registered. Errors: %s", strings.Join(errors, "; ")), nil
				}
				return fmt.Sprintf("Error: no skill subdirectories with SKILL.md found in %q.", args.Path), nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Registered %d skill(s) from %s:\n", len(registered), absPath))
			for _, name := range registered {
				b.WriteString(fmt.Sprintf("  %s\n", name))
			}
			b.WriteString("Use load_skill to activate the ones you need.")
			if len(errors) > 0 {
				b.WriteString(fmt.Sprintf("\nErrors: %s", strings.Join(errors, "; ")))
			}
			return b.String(), nil
		}).
		Build()
}

// registerOneSkill registers a single skill from a SKILL.md path.
func registerOneSkill(sr *agent.SkillRegistry, skillPath string) (string, error) {
	if err := sr.Add(skillPath); err != nil {
		if strings.Contains(err.Error(), "duplicate name") {
			return fmt.Sprintf("Skill at %q is already registered.", skillPath), nil
		}
		return "", fmt.Errorf("import_skill: %w", err)
	}

	// Resolve the registered name
	dirName := filepath.Base(filepath.Dir(skillPath))
	skill := sr.Get(dirName)
	name := dirName
	if skill != nil {
		name = skill.Name
	}
	return fmt.Sprintf("Registered skill %q from %s. Use load_skill %q to activate it.", name, skillPath, name), nil
}

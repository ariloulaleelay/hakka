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

// skillSessionFrom returns the current session's skill state from the
// context. Skill tools are always session-bound — they operate on the
// session's own registry, never on a server-global one.
func skillSessionFrom(ctx context.Context) (agent.SessionSkills, error) {
	v := event.SessionViewFromContext(ctx)
	s, ok := v.(agent.SessionSkills)
	if !ok || s == nil {
		return nil, fmt.Errorf("skill tool: session not available in context")
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// search_skills — search the session's skill registry
// ---------------------------------------------------------------------------

type searchSkillsArgs struct {
	Query string `json:"query"`
}

// SearchSkills returns a tool that searches the CURRENT session's skill
// registry by name, description, or tags. Results include all metadata
// needed to decide whether to load a skill — no separate inspect step
// needed.
func SearchSkills() agent.Tool {
	return NewTool("search_skills", "Search this session's registered skills by name, description, or tags. Use this to discover what skills are available.").
		StringParam("query", "Search query — matches against skill name, description, and tags. Empty returns all.", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchSkillsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("search_skills: %w", err)
			}

			session, err := skillSessionFrom(ctx)
			if err != nil {
				return "", err
			}

			results := session.SkillRegistry().Search(args.Query)
			if len(results) == 0 {
				return fmt.Sprintf("No skills found matching %q. Use import_skill to register skills from a path into this session.", args.Query), nil
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
func LoadSkill() agent.Tool {
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

			session, err := skillSessionFrom(ctx)
			if err != nil {
				return "", err
			}

			skill := session.SkillRegistry().Get(args.Name)
			if skill == nil {
				return fmt.Sprintf("Error: skill %q not found. Use search_skills to discover available skills.", args.Name), nil
			}

			// Check if already loaded
			for _, loaded := range session.ActiveSkills() {
				if loaded == args.Name {
					return fmt.Sprintf("Skill %q is already loaded.", args.Name), nil
				}
			}

			// Verify content is readable before committing
			if _, err := session.SkillRegistry().ReadContent(args.Name); err != nil {
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
func UnloadSkill() agent.Tool {
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

			session, err := skillSessionFrom(ctx)
			if err != nil {
				return "", err
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
// import_skill — register skill(s) from a path into the CURRENT session
// ---------------------------------------------------------------------------

type importSkillArgs struct {
	Path string `json:"path"`
}

// ImportSkill returns a tool that registers skills from a path into the
// CURRENT session's registry. Imported skills are persisted with the
// session and never leak into other sessions. It never loads skills into
// the session — use load_skill for that.
//
// Three forms are accepted:
//  1. A SKILL.md file → registers that single skill.
//  2. A skill directory (containing SKILL.md) → registers that single skill.
//  3. A registry directory (subdirs each with SKILL.md) → registers all skills.
func ImportSkill() agent.Tool {
	return NewTool("import_skill", "Register skill(s) from a path into THIS session. Does NOT load into the session — use load_skill to activate. Accepts: a SKILL.md file, a single skill directory, or a registry directory (multiple skills). Skills imported here are visible only in this session.").
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

			session, err := skillSessionFrom(ctx)
			if err != nil {
				return "", err
			}

			// Resolve the path relative to CWD
			absPath := args.Path
			if !filepath.IsAbs(absPath) {
				cwd := event.CWDFromContext(ctx)
				if cwd != "" {
					absPath = filepath.Join(cwd, absPath)
				}
			}
			absPath, err = filepath.Abs(absPath)
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

			// Resolve the user path to concrete SKILL.md files.
			skillPaths, err := resolveSkillPaths(absPath, info)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), nil
			}

			added, problems, err := session.ImportSkills(ctx, skillPaths)
			if err != nil {
				return "", fmt.Errorf("import_skill: %w", err)
			}
			// Names of skills from this import that are now in the session
			// registry (newly added or already present).
			var registered []string
			for _, p := range skillPaths {
				name := filepath.Base(filepath.Dir(p))
				if session.SkillRegistry().Get(name) != nil && !slicesContainsString(registered, name) {
					registered = append(registered, name)
				}
			}

			if added == 0 {
				if len(problems) > 0 {
					msgs := make([]string, 0, len(problems))
					for _, p := range problems {
						msgs = append(msgs, p.Error())
					}
					return fmt.Sprintf("Error: no skills registered. Errors: %s", strings.Join(msgs, "; ")), nil
				}
				return fmt.Sprintf("Skill(s) at %q are already registered in this session.", args.Path), nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Registered %d skill(s) from %s:\n", added, absPath))
			for _, name := range registered {
				b.WriteString(fmt.Sprintf("  %s\n", name))
			}
			b.WriteString("Use load_skill to activate the ones you need.")
			if len(problems) > 0 {
				msgs := make([]string, 0, len(problems))
				for _, p := range problems {
					msgs = append(msgs, p.Error())
				}
				b.WriteString(fmt.Sprintf("\nErrors: %s", strings.Join(msgs, "; ")))
			}
			return b.String(), nil
		}).
		Build()
}

// resolveSkillPaths resolves a user-supplied path (file, skill dir, or
// registry dir) into a list of concrete SKILL.md file paths.
func resolveSkillPaths(absPath string, info os.FileInfo) ([]string, error) {
	// Case 1: path is a file → must be a SKILL.md file
	if !info.IsDir() {
		return []string{absPath}, nil
	}

	// Case 2: directory contains SKILL.md → single skill dir
	skillMDPath := filepath.Join(absPath, "SKILL.md")
	if _, err := os.Stat(skillMDPath); err == nil {
		return []string{skillMDPath}, nil
	}

	// Case 3: registry directory → scan for subdirs with SKILL.md
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sp := filepath.Join(absPath, entry.Name(), "SKILL.md")
		if _, err := os.Stat(sp); err != nil {
			continue
		}
		paths = append(paths, sp)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no skill subdirectories with SKILL.md found in %q", absPath)
	}
	return paths, nil
}

func slicesContainsString(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

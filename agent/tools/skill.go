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
// description, or tags.
func SearchSkills(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("search_skills", "Search registered skills by name, description, or tags. Use this to discover what skills are available.").
		StringParam("query", "Search query — matches against skill name, description, and tags", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchSkillsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("search_skills: %w", err)
			}
			if args.Query == "" {
				return "Error: query is required", nil
			}

			results := sr.Search(args.Query)
			if len(results) == 0 {
				return fmt.Sprintf("No skills found matching %q.", args.Query), nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Found %d skill(s) matching %q:\n", len(results), args.Query))
			for _, s := range results {
				tagStr := ""
				if len(s.Tags) > 0 {
					tagStr = " [" + strings.Join(s.Tags, ", ") + "]"
				}
				b.WriteString(fmt.Sprintf("  %s — %s%s\n", s.Name, s.Description, tagStr))
			}
			return b.String(), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// inspect_skill — show full content preview of a skill
// ---------------------------------------------------------------------------

type inspectSkillArgs struct {
	Name string `json:"name"`
}

// InspectSkill returns a tool that shows the full content of a registered
// skill without loading it into the session.
func InspectSkill(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("inspect_skill", "Show full content of a registered skill without loading it. Read before loading to decide if you need it.").
		StringParam("name", "Name of the skill to inspect", true).
		Tags("skill", "all").
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args inspectSkillArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("inspect_skill: %w", err)
			}
			if args.Name == "" {
				return "Error: name is required", nil
			}

			skill := sr.Get(args.Name)
			if skill == nil {
				return fmt.Sprintf("Error: skill %q not found. Use search_skills to discover available skills.", args.Name), nil
			}

			content, err := sr.ReadContent(args.Name)
			if err != nil {
				return "", fmt.Errorf("inspect_skill: %w", err)
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Skill: %s\n", skill.Name))
			b.WriteString(fmt.Sprintf("Description: %s\n", skill.Description))
			if len(skill.Tags) > 0 {
				b.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(skill.Tags, ", ")))
			}
			b.WriteString(fmt.Sprintf("Path: %s\n", skill.Path))
			b.WriteString("\n--- Content ---\n")
			b.WriteString(content)
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

			session.AddActiveSkill(args.Name)
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

			session.RemoveActiveSkill(args.Name)
			return fmt.Sprintf("Unloaded skill %q. It will no longer appear in your system prompt.", args.Name), nil
		}).
		Build()
}

// ---------------------------------------------------------------------------
// import_skill — load an ad-hoc skill from any file path
// ---------------------------------------------------------------------------

type importSkillArgs struct {
	Path string `json:"path"`
}

// ImportSkill returns a tool that loads a skill from any file path,
// registering it on-the-fly and loading it into the session.
func ImportSkill(sr *agent.SkillRegistry) agent.Tool {
	return NewTool("import_skill", "Load a skill from any file path. The file is registered and loaded into the current session. Use this for one-off skills not in the registry.").
		StringParam("path", "Absolute or relative path to the skill file", true).
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

			if _, err := os.Stat(absPath); os.IsNotExist(err) {
				return fmt.Sprintf("Error: file %q does not exist.", absPath), nil
			}

			// Read the skill name from the file
			data, err := os.ReadFile(absPath)
			if err != nil {
				return "", fmt.Errorf("import_skill: %w", err)
			}

			name, _, _ := agent.ParseSkillFile(absPath, string(data))
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
			}

			// Register the skill (allow overwriting existing — the import is a one-off)
			_ = sr.Add(absPath) // ignore duplicate errors

			// Load into session
			session, ok := event.SessionViewFromContext(ctx).(agent.SessionView)
			if !ok || session == nil {
				return "", fmt.Errorf("import_skill: session not available in context")
			}

			session.AddActiveSkill(name)
			return fmt.Sprintf("Imported and loaded skill %q from %s. It is now part of your system prompt. Use unload_skill %q to remove it.", name, absPath, name), nil
		}).
		Build()
}

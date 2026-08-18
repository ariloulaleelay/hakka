package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeSkillTree creates a skills registry directory at <root>/skills/<name>
// with a valid SKILL.md and returns root.
func makeSkillTree(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	makeSkillInDir(t, filepath.Join(root, "skills", name), name, "Cwd skill "+name)
	return root
}

// makeSkillInDir creates a skill directory (with SKILL.md) at dir.
func makeSkillInDir(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n\nBody for " + name + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeStandaloneSkill creates a skill dir NOT under a skills/ registry dir
// and returns the path to its SKILL.md (used for import tests).
func makeStandaloneSkill(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	makeSkillInDir(t, dir, name, "Imported skill "+name)
	return filepath.Join(dir, "SKILL.md")
}

// newSessionWithCWD creates a session with an explicit working directory.
func newSessionWithCWD(t *testing.T, cwd string) *Session {
	t.Helper()
	sess := NewSession("testns", "prompt")
	if err := sess.SetClientCWD(context.Background(), cwd); err != nil {
		t.Fatal(err)
	}
	return sess
}

// ---------------------------------------------------------------------------
// Auto-import of ${cwd}/skills
// ---------------------------------------------------------------------------

func TestSessionSkillRegistry_AutoImportsCwdSkills(t *testing.T) {
	root := makeSkillTree(t, "go-testing")
	sess := newSessionWithCWD(t, root)

	reg := sess.SkillRegistry()
	if reg.Get("go-testing") == nil {
		t.Fatal("expected skill from ${cwd}/skills to be auto-imported")
	}
}

func TestSessionSkillRegistry_NoSkillsDirYieldsEmptyRegistry(t *testing.T) {
	// cwd exists but has no skills/ subdirectory.
	sess := newSessionWithCWD(t, t.TempDir())

	reg := sess.SkillRegistry()
	if n := len(reg.List()); n != 0 {
		t.Fatalf("expected 0 skills, got %d", n)
	}
}

func TestSessionSkillRegistry_EmptyCWDYieldsEmptyRegistry(t *testing.T) {
	sess := NewSession("testns", "prompt")
	sess.Update(func(d *SessionData) { d.ClientCWD = "" })

	if n := len(sess.SkillRegistry().List()); n != 0 {
		t.Fatalf("expected 0 skills, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// ImportSkills — registers into the session's own registry and persists
// ---------------------------------------------------------------------------

func TestSessionImportSkills_RegistersAndPersists(t *testing.T) {
	skillPath := makeStandaloneSkill(t, "custom-skill")
	sess := newSessionWithCWD(t, t.TempDir())

	added, problems, err := sess.ImportSkills(context.Background(), []string{skillPath})
	if err != nil {
		t.Fatalf("ImportSkills: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}

	if sess.SkillRegistry().Get("custom-skill") == nil {
		t.Fatal("expected imported skill in the session registry")
	}
	d := sess.Read()
	if len(d.SkillPaths) != 1 || d.SkillPaths[0] != skillPath {
		t.Fatalf("expected SkillPaths to contain %q, got %v", skillPath, d.SkillPaths)
	}
}

func TestSessionImportSkills_InvalidPathReported(t *testing.T) {
	sess := newSessionWithCWD(t, t.TempDir())

	added, problems, err := sess.ImportSkills(context.Background(), []string{
		filepath.Join(t.TempDir(), "not-a-skill", "README.md"),
	})
	if err != nil {
		t.Fatalf("ImportSkills: %v", err)
	}
	if added != 0 {
		t.Fatalf("expected 0 added, got %d", added)
	}
	if len(problems) != 1 {
		t.Fatalf("expected 1 problem, got %v", problems)
	}
	if len(sess.Read().SkillPaths) != 0 {
		t.Fatalf("expected no persisted paths, got %v", sess.Read().SkillPaths)
	}
}

func TestSessionImportSkills_DuplicateIsNoop(t *testing.T) {
	skillPath := makeStandaloneSkill(t, "dup-skill")
	sess := newSessionWithCWD(t, t.TempDir())

	if added, problems, err := sess.ImportSkills(context.Background(), []string{skillPath}); err != nil || len(problems) != 0 || added != 1 {
		t.Fatalf("first import: added=%d problems=%v err=%v", added, problems, err)
	}
	// Same path again — silently skipped.
	added, problems, err := sess.ImportSkills(context.Background(), []string{skillPath})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if added != 0 || len(problems) != 0 {
		t.Fatalf("expected noop re-import, got added=%d problems=%v", added, problems)
	}
	if got := len(sess.Read().SkillPaths); got != 1 {
		t.Fatalf("expected 1 persisted path, got %d", got)
	}
}

func TestSessionImportSkills_DuplicateNameAcrossSources(t *testing.T) {
	// Session already has "dup-name" from ${cwd}/skills; importing another
	// skill with the same name from elsewhere must be reported as a problem.
	root := makeSkillTree(t, "dup-name")
	sess := newSessionWithCWD(t, root)

	other := filepath.Join(t.TempDir(), "dup-name", "SKILL.md")
	makeSkillInDir(t, filepath.Dir(other), "dup-name", "other dup")

	added, problems, err := sess.ImportSkills(context.Background(), []string{other})
	if err != nil {
		t.Fatalf("ImportSkills: %v", err)
	}
	if added != 0 {
		t.Fatalf("expected 0 added, got %d", added)
	}
	if len(problems) != 1 {
		t.Fatalf("expected 1 problem, got %v", problems)
	}
	if len(sess.Read().SkillPaths) != 0 {
		t.Fatalf("expected no persisted paths, got %v", sess.Read().SkillPaths)
	}
}

// ---------------------------------------------------------------------------
// Session isolation — skills are per-session, never shared
// ---------------------------------------------------------------------------

func TestSessionSkills_Isolation(t *testing.T) {
	skillPath := makeStandaloneSkill(t, "session-a-only")
	sessA := newSessionWithCWD(t, t.TempDir())
	sessB := newSessionWithCWD(t, t.TempDir())

	if added, problems, err := sessA.ImportSkills(context.Background(), []string{skillPath}); err != nil || len(problems) != 0 || added != 1 {
		t.Fatalf("import into A: added=%d problems=%v err=%v", added, problems, err)
	}

	if sessA.SkillRegistry().Get("session-a-only") == nil {
		t.Fatal("expected session A to see the imported skill")
	}
	if sessB.SkillRegistry().Get("session-a-only") != nil {
		t.Fatal("session B must NOT see skills imported into session A")
	}
	if len(sessB.Read().SkillPaths) != 0 {
		t.Fatalf("session B must have empty SkillPaths, got %v", sessB.Read().SkillPaths)
	}
}

// ---------------------------------------------------------------------------
// Fork inheritance
// ---------------------------------------------------------------------------

func TestForkData_InheritsSkillPaths(t *testing.T) {
	skillPath := makeStandaloneSkill(t, "forked-skill")
	sd := NewSessionData("ns", "prompt")
	sd.ID = "parent-1"
	sd.SkillPaths = []string{skillPath}

	child, err := sd.ForkData("")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.SkillPaths) != 1 || child.SkillPaths[0] != skillPath {
		t.Fatalf("skill paths not inherited: %v", child.SkillPaths)
	}
}

// ---------------------------------------------------------------------------
// Active skill content injection uses the SESSION's registry
// ---------------------------------------------------------------------------

func TestBuildCompactContext_InjectsActiveSkillFromSessionRegistry(t *testing.T) {
	root := makeSkillTree(t, "active-skill")
	sess := newSessionWithCWD(t, root)
	if err := sess.AddActiveSkill(context.Background(), "active-skill"); err != nil {
		t.Fatal(err)
	}

	msgs, _, _ := BuildCompactContext(sess, 100000)
	var found bool
	for _, m := range msgs {
		if m.Role == RoleSystem && strings.Contains(m.Content, "## Skill: active-skill") && strings.Contains(m.Content, "Body for active-skill") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected active skill content in context, got %+v", msgs)
	}
}

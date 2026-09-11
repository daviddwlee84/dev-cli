package skill_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/skill"
	"go.yaml.in/yaml/v3"
)

func TestRenderHasValidFrontmatter(t *testing.T) {
	body, err := skill.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("SKILL.md must open with YAML frontmatter — a file that does not parse is silently skipped by every skill loader")
	}
	end := strings.Index(body[4:], "\n---\n")
	if end < 0 {
		t.Fatal("frontmatter is not terminated")
	}
	front := body[4 : end+4]
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(front), &metadata); err != nil {
		t.Fatalf("invalid skill frontmatter: %v", err)
	}
	if metadata.Name != skill.Name {
		t.Errorf("frontmatter name = %q, want %q", metadata.Name, skill.Name)
	}
	if strings.TrimSpace(metadata.Description) == "" {
		t.Error("frontmatter needs a description — it is what a loader matches on")
	}
	if words := len(strings.Fields(metadata.Description)); words > 40 {
		t.Errorf("description uses %d words, want at most 40", words)
	}
}

func TestRenderContextBudget(t *testing.T) {
	body, err := skill.Render()
	if err != nil {
		t.Fatal(err)
	}
	if words := len(strings.Fields(body)); words < 250 || words > 350 {
		t.Errorf("entrypoint uses %d whitespace words including frontmatter, want 250–350; keep details in command help or targeted references", words)
	}
	if len(body) > 4*1024 {
		t.Errorf("entrypoint uses %d bytes, want at most 4 KiB", len(body))
	}
}

func TestRenderMatchesEmbeddedEntrypoint(t *testing.T) {
	body, err := skill.Render()
	if err != nil {
		t.Fatal(err)
	}
	all, err := skill.Files()
	if err != nil {
		t.Fatal(err)
	}
	if body != string(all["SKILL.md"]) {
		t.Fatal("printed skill must match the entrypoint shipped by skill install")
	}
}

func TestFilesIncludeReferences(t *testing.T) {
	all, err := skill.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SKILL.md",
		"references/agent-capabilities.md",
		"references/agent-interop.md",
		"references/agent-retirement.md",
		"references/bootstrap.md",
		"references/dashboard-actions.md",
		"references/local-triage.md",
		"references/notes.md",
		"references/skills-management.md",
		"references/ssh-hosts.md",
		"references/submodules.md",
		"references/worktree-ownership.md",
		"references/task-lifecycle.md",
		"references/runtime-herdr.md",
		"references/parallel-agents.md",
		"references/commands.md",
		"references/repository-bootstrap.md",
		"references/pull-requests.md",
		"references/prompt-handoffs.md",
	} {
		if _, ok := all[want]; !ok {
			t.Errorf("%s is not embedded (have: %v)", want, keys(all))
		}
	}
}

// Every reference the skill links to must actually ship, or an agent following
// the link finds nothing.
func TestSkillReferencesResolve(t *testing.T) {
	all, err := skill.Files()
	if err != nil {
		t.Fatal(err)
	}
	body := string(all["SKILL.md"])
	links := regexp.MustCompile(`\[[^\]]+\]\(([^)\s]+)\)`).FindAllStringSubmatch(body, -1)
	if len(links) == 0 {
		t.Fatal("entrypoint needs targeted links to its advanced guides")
	}
	for _, link := range links {
		reference, _, _ := strings.Cut(link[1], "#")
		if !fs.ValidPath(reference) {
			t.Errorf("SKILL.md link %q is not a relative bundled path", reference)
			continue
		}
		if _, ok := all[reference]; !ok {
			t.Errorf("SKILL.md links to %s but it is not embedded", reference)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dev")

	first, err := skill.Install(dir, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(first.Written) == 0 {
		t.Fatal("the first install should write files")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}

	second, err := skill.Install(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Written) != 0 {
		t.Errorf("re-installing unchanged content should write nothing, wrote %v", second.Written)
	}
	if len(second.Skipped) != len(first.Written) {
		t.Errorf("everything should have been skipped: %v", second.Skipped)
	}
}

func TestInstallRewritesChangedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dev")
	if _, err := skill.Install(dir, false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stale\n"), 0o644)

	res, err := skill.Install(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res.Written {
		if w == "SKILL.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("a modified file should be rewritten, wrote %v", res.Written)
	}
}

func TestInstallLinksIntoToolDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeSkills, 0o755); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".agents", "skills", skill.Name)
	res, err := skill.Install(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(claudeSkills, skill.Name)
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v (links: %v)", link, err, res.Links)
	}
	if target != dir {
		t.Errorf("link points at %q, want %q", target, dir)
	}

	// Re-running must not churn the link.
	if _, err := skill.Install(dir, true); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

// A real directory in the tool's skills folder is someone else's install; it
// must never be replaced by a symlink.
func TestInstallDoesNotClobberRealDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeSkills := filepath.Join(home, ".claude", "skills")
	existing := filepath.Join(claudeSkills, skill.Name)
	os.MkdirAll(existing, 0o755)
	marker := filepath.Join(existing, "hand-written.md")
	os.WriteFile(marker, []byte("mine\n"), 0o644)

	if _, err := skill.Install(filepath.Join(home, ".agents", "skills", skill.Name), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("an existing real directory must be left alone")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

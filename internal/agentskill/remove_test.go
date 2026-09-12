package agentskill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/skill"
)

func TestMain(m *testing.M) {
	if os.Getenv("DEV_SKILL_REMOVE_TEST_HELPER") == "1" {
		args := os.Args[1:]
		if len(args) == 1 && args[0] == "--version" {
			fmt.Println("1.5.25")
			os.Exit(0)
		}
		if len(args) == 1 && args[0] == "--help" {
			fmt.Println("update remove experimental_install experimental_sync")
			os.Exit(0)
		}
		if len(args) == 0 || args[0] != "remove" || slices.Contains(args, "--all") {
			os.Exit(21)
		}
		if os.Getenv("DEV_SKILL_REMOVE_TEST_NOOP") == "1" {
			os.Exit(0)
		}
		names := []string{}
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--") {
				break
			}
			names = append(names, a)
		}
		root, _ := os.Getwd()
		lockPath := filepath.Join(root, "skills-lock.json")
		if slices.Contains(args, "--global") {
			lockPath = GlobalLockPath()
		}
		data, err := os.ReadFile(lockPath)
		if err != nil {
			os.Exit(22)
		}
		var lock map[string]json.RawMessage
		if json.Unmarshal(data, &lock) != nil {
			os.Exit(23)
		}
		var skills map[string]json.RawMessage
		if json.Unmarshal(lock["skills"], &skills) != nil {
			os.Exit(24)
		}
		for _, name := range names {
			if !safePathComponent(name) {
				os.Exit(25)
			}
			if slices.Contains(args, "claude-code") {
				if err := os.RemoveAll(filepath.Join(root, ".claude", "skills", name)); err != nil {
					os.Exit(26)
				}
			}
			if slices.Contains(args, "codex") {
				if err := os.RemoveAll(filepath.Join(root, ".agents", "skills", name)); err != nil {
					os.Exit(27)
				}
				delete(skills, name)
			}
		}
		if os.Getenv("DEV_SKILL_REMOVE_TEST_DRIFT") == "1" {
			_ = os.WriteFile(filepath.Join(root, ".agents", "skills", "keep", "SKILL.md"), []byte("unexpected provider change"), 0o644)
		}
		lock["skills"], _ = json.Marshal(skills)
		data, _ = json.Marshal(lock)
		if os.WriteFile(lockPath, data, 0o644) != nil {
			os.Exit(28)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func removalProvider(t *testing.T) {
	t.Helper()
	home := isolateAgentEnvironment(t)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	bin := t.TempDir()
	name := "skills"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_SKILL_REMOVE_TEST_HELPER", "1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func removalAgents(row Skill) []string {
	set := map[string]bool{}
	for _, installation := range row.Installations {
		for _, id := range installation.AgentIDs {
			set[id] = true
		}
	}
	result := []string{}
	for id := range set {
		result = append(result, id)
	}
	return result
}

func TestSelectedRemovalPreservesOtherSkillsAndChecksProviderOutcome(t *testing.T) {
	for _, mode := range []string{"remove", "noop", "unrelated-drift"} {
		t.Run(mode, func(t *testing.T) {
			removalProvider(t)
			root := initRepository(t)
			chosen := managedFixture(t, root, "chosen")
			managedFixture(t, root, "keep")
			t.Setenv("DEV_SKILL_REMOVE_TEST_NOOP", map[bool]string{true: "1"}[mode == "noop"])
			t.Setenv("DEV_SKILL_REMOVE_TEST_DRIFT", map[bool]string{true: "1"}[mode == "unrelated-drift"])
			ops := PrepareRemovals(t.Context(), []Skill{chosen}, removalAgents(chosen))
			if len(ops) != 1 || ops[0].Blocked != "" {
				t.Fatalf("blocked: %+v", ops)
			}
			receipt := ApplyManagement(t.Context(), ops, nil)
			want := "completed"
			if mode != "remove" {
				want = "unverified"
			}
			if len(receipt.Outcomes) != 1 || receipt.Outcomes[0].Status != want {
				t.Fatalf("result: %+v", receipt)
			}
			lock := readProjectLock(filepath.Join(root, "skills-lock.json"))
			if _, ok := lock.Entries["keep"]; !ok {
				t.Fatal("unrelated lock removed")
			}
		})
	}
}

func TestRemovalStalePathAndLocalEditsPreventProvider(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	ops := PrepareRemovals(t.Context(), []Skill{row}, removalAgents(row))
	if len(ops) != 1 || ops[0].Blocked != "" {
		t.Fatal(ops)
	}
	writeSkill(t, row.Path, "demo", "local edits")
	if got := ApplyManagement(t.Context(), ops, nil); got.Outcomes[0].Status != "stale" {
		t.Fatal(got)
	}
	if _, err := os.Stat(row.Path); err != nil {
		t.Fatal("stale provider removed directory")
	}
	if got := PrepareRemovals(t.Context(), []Skill{row}, removalAgents(row)); got[0].Blocked == "" {
		t.Fatal("modified installation permitted")
	}
}

func TestRemovalSharedConsumersAndHookDependencies(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	if got := PrepareRemovals(t.Context(), []Skill{row}, []string{"codex"}); got[0].Blocked == "" {
		t.Fatal("shared directory could be removed for one consumer")
	}
	writeSkill(t, filepath.Join(root, ".agents", "skills", "agent-history-hygiene"), "agent-history-hygiene", "fixture")
	legacy := row
	legacy.Name = "agent-history-hygiene"
	legacy.Lock = nil
	if got := PrepareRemovals(t.Context(), []Skill{legacy}, removalAgents(row)); got[0].Blocked == "" {
		t.Fatal("legacy finalizer dependency removed")
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("check:\n\t.agents/skills/demo/scripts/check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := PrepareRemovals(t.Context(), []Skill{row}, removalAgents(row)); got[0].Blocked == "" {
		t.Fatal("hook dependency ignored")
	}
}

func TestRemovalNeverDeletesExternalSkillSource(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Rename(row.Path, source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, row.Path); err != nil {
		t.Skip("symlink unavailable")
	}
	rows, err := List(t.Context(), root, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := PrepareRemovals(t.Context(), rows, removalAgents(row)); len(got) == 0 || got[0].Blocked == "" {
		t.Fatal("external source eligible")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source changed")
	}
}

func TestAliasRemovalRetainsCanonicalSkillAndLock(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	alias := filepath.Join(root, ".claude", "skills", "demo")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(row.Path, alias); err != nil {
		t.Skip("symlink unavailable")
	}
	rows, err := List(t.Context(), root, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	ops := PrepareRemovals(t.Context(), rows, []string{"claude-code"})
	if len(ops) != 1 || ops[0].Blocked != "" {
		t.Fatal(ops)
	}
	result := ApplyManagement(t.Context(), ops, nil)
	if result.Outcomes[0].Status != "completed" {
		t.Fatal(result)
	}
	if _, err := os.Lstat(alias); !os.IsNotExist(err) {
		t.Fatal("selected alias remains")
	}
	if _, err := os.Stat(row.Path); err != nil {
		t.Fatal("canonical content removed")
	}
	if _, ok := readProjectLock(filepath.Join(root, "skills-lock.json")).Entries["demo"]; !ok {
		t.Fatal("shared lock entry removed")
	}
}

func TestBundledRemovalUsesManifestAndPreservesUnrelatedFiles(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	dir := filepath.Join(root, ".agents", "skills", "dev-cli")
	if _, err := skill.Install(dir, false); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dir, "my-notes.txt")
	if err := os.WriteFile(extra, []byte("user notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := List(t.Context(), root, ListOptions{Project: true})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	ops := PrepareRemovals(t.Context(), rows, removalAgents(rows[0]))
	if len(ops) != 1 || ops[0].Blocked != "" {
		t.Fatal(ops)
	}
	result := ApplyManagement(t.Context(), ops, nil)
	if result.Outcomes[0].Status != "completed" {
		t.Fatal(result)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("unrelated file removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("managed entry remains")
	}
}

func TestRemovalGlobalScopeKeepsProjectCopy(t *testing.T) {
	removalProvider(t)
	root := initRepository(t)
	project := managedFixture(t, root, "demo")
	global := filepath.Join(homeDirectory(), ".agents", "skills", "demo")
	writeSkill(t, global, "demo", "global copy")
	hashes, err := installedHashes(t.Context(), global)
	if err != nil {
		t.Fatal(err)
	}
	writeLock(t, GlobalLockPath(), 3, map[string]any{"demo": map[string]any{"source": "owner/repo", "sourceType": "github", "sourceUrl": "https://github.com/owner/repo.git", "skillPath": "skills/demo/SKILL.md", "skillFolderHash": hashes["git-tree"]}})
	rows, err := List(t.Context(), root, ListOptions{Global: true})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	ops := PrepareRemovals(t.Context(), rows, removalAgents(rows[0]))
	if len(ops) != 1 || ops[0].Blocked != "" {
		t.Fatal(ops)
	}
	result := ApplyManagement(t.Context(), ops, nil)
	if result.Outcomes[0].Status != "completed" {
		t.Fatal(result)
	}
	if _, err := os.Stat(global); !os.IsNotExist(err) {
		t.Fatal("global copy remains")
	}
	if _, err := os.Stat(project.Path); err != nil {
		t.Fatal("project copy was removed")
	}
}

func TestRemovalEnvironmentExcludesCheckoutAndRelativeSearch(t *testing.T) {
	root := t.TempDir()
	untrusted := filepath.Join(root, "bin")
	trusted := t.TempDir()
	if err := os.Mkdir(untrusted, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{".", untrusted, trusted}, string(os.PathListSeparator)))
	env := managementEnvironment(root, ScopeProject)
	if len(env) != 2 || env[0] != "PATH="+trusted || env[1] != "NoDefaultCurrentDirectoryInExePath=1" {
		t.Fatal("unsafe interpreter search retained")
	}
}

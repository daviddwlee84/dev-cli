package agentskill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func managedFixture(t *testing.T, root, name string) Skill {
	t.Helper()
	path := filepath.Join(root, ".agents", "skills", name)
	writeSkill(t, path, name, "original")
	hash, err := folderHash(path)
	if err != nil {
		t.Fatal(err)
	}
	document := readProjectLock(filepath.Join(root, "skills-lock.json"))
	entries := map[string]any{}
	for key, entry := range document.Entries {
		entries[key] = map[string]any{"source": entry.Source, "sourceType": "github", "skillPath": entry.SkillPath, "computedHash": entry.ComputedHash}
	}
	entries[name] = map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/" + name + "/SKILL.md", "computedHash": hash}
	writeLock(t, filepath.Join(root, "skills-lock.json"), 1, entries)
	rows, err := List(context.Background(), root, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name {
			row.UpdateStatus = UpdateAvailable
			return row
		}
	}
	t.Fatal("fixture row not found")
	return Skill{}
}
func manageProvider(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "skills")
	writeExecutable(t, path, "#!/bin/sh\ncase \"$1\" in --version) echo 1.5.25; exit 0;; --help) echo 'update experimental_install experimental_sync'; exit 0;; esac\n"+body)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}
func TestManagementBindsLockFilesProviderAndInstalledContents(t *testing.T) {
	isolateAgentEnvironment(t)
	ctx := context.Background()
	root := initRepository(t)
	marker := filepath.Join(t.TempDir(), "executed")
	manageProvider(t, "echo ran > '"+marker+"'\n")
	row := managedFixture(t, root, "demo")
	plans := PrepareUpdates(ctx, []Skill{row})
	if len(plans) != 1 || plans[0].Blocked != "" {
		t.Fatalf("plans %+v", plans)
	}
	writeSkill(t, filepath.Join(root, ".agents", "skills", "demo"), "demo", "local edit")
	result := ApplyManagement(ctx, plans, nil)
	if len(result.Outcomes) != 1 || result.Outcomes[0].Status != "stale" {
		t.Fatal(result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("stale operation executed")
	}
	fresh, _ := List(ctx, root, ListOptions{Project: true})
	fresh[0].UpdateStatus = UpdateAvailable
	if blocked := PrepareUpdates(ctx, fresh); blocked[0].Blocked == "" {
		t.Fatal("local drift was allowed")
	}
}
func TestManagementGroupsScopesAndDoesNotClaimNoOpSuccess(t *testing.T) {
	isolateAgentEnvironment(t)
	ctx := context.Background()
	manageProvider(t, "echo 'token=fixture_private_value'\nexit 0\n")
	root := initRepository(t)
	managedFixture(t, root, "a")
	managedFixture(t, root, "b")
	rows, _ := List(ctx, root, ListOptions{Project: true})
	for i := range rows {
		rows[i].UpdateStatus = UpdateAvailable
	}
	plans := PrepareUpdates(ctx, rows)
	if len(plans) != 1 || len(plans[0].Names) != 2 || plans[0].Blocked != "" {
		t.Fatalf("%+v", plans)
	}
	receipt := ApplyManagement(ctx, plans, nil)
	for _, item := range receipt.Outcomes {
		if item.Status != "unverified" || strings.Contains(item.Detail, "fixture_private_value") {
			t.Fatal(item)
		}
	}
	path, err := SaveManageReceipt(t.TempDir(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "fixture_private_value") {
		t.Fatal(string(data), err)
	}
}
func TestNativeRestoreAllowsLockOnlyButRejectsDestinationDrift(t *testing.T) {
	isolateAgentEnvironment(t)
	ctx := context.Background()
	manageProvider(t, "exit 0\n")
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	if err := os.RemoveAll(row.Path); err != nil {
		t.Fatal(err)
	}
	op := PrepareNative(ctx, root, "experimental_install", nil)
	if op.Blocked != "" {
		t.Fatal(op.Blocked)
	}
	writeSkill(t, filepath.Join(root, ".agents", "skills", "unlocked"), "unlocked", "new user file")
	receipt := ApplyManagement(ctx, []ManageOperation{op}, nil)
	if receipt.Outcomes[0].Status != "stale" {
		t.Fatal(receipt)
	}
}
func TestNativeSyncDiscoversDependenciesAndRejectsNameCollision(t *testing.T) {
	isolateAgentEnvironment(t)
	ctx := context.Background()
	manageProvider(t, "exit 0\n")
	root := initRepository(t)
	writeSkill(t, filepath.Join(root, "node_modules", "package", "skills", "demo"), "demo", "dependency")
	op := PrepareNative(ctx, root, "experimental_sync", []string{"codex"})
	if op.Blocked != "" || len(op.Names) != 1 || op.Names[0] != "demo" {
		t.Fatalf("%+v", op)
	}
	writeSkill(t, filepath.Join(root, ".agents", "skills", "demo"), "demo", "user content")
	if op := PrepareNative(ctx, root, "experimental_sync", []string{"codex"}); op.Blocked == "" {
		t.Fatal("sync could overwrite an unrelated skill")
	}
}
func TestCheckedEvidenceInvalidatesWhenLockChanges(t *testing.T) {
	home := isolateAgentEnvironment(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	row := managedFixture(t, initRepository(t), "demo")
	row.UpdateCheckedAt = time.Now().UTC()
	row.UpdateDetail = "checked"
	saveChecks(context.Background(), []Skill{row})
	row.UpdateStatus = UpdateUnchecked
	row.UpdateCheckedAt = time.Time{}
	cached := CachedChecks(context.Background(), []Skill{row})
	if cached[0].UpdateStatus != UpdateAvailable || cached[0].UpdateCheckedAt.IsZero() {
		t.Fatal(cached)
	}
	data, err := os.ReadFile(row.Lock.File)
	if err != nil {
		t.Fatal(err)
	}
	var lock map[string]any
	if err = json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(row.Lock.File, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cached = CachedChecks(context.Background(), []Skill{row})
	if cached[0].UpdateStatus != UpdateUnchecked {
		t.Fatal("changed lock reused old evidence")
	}
}

func TestManagementAcceptsCanonicalAliasesAndGlobalUserDependency(t *testing.T) {
	home := isolateAgentEnvironment(t)
	ctx := context.Background()
	root := initRepository(t)
	manageProvider(t, "exit 0\n")
	managedFixture(t, root, "project-demo")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	inventory, err := scopeInventory(ctx, alias, ScopeProject)
	if err != nil || len(inventory.Skills) != 1 {
		t.Fatal(err, inventory.Diagnostics)
	}
	inventory.Skills[0].UpdateStatus = UpdateAvailable
	if plans := PrepareUpdates(ctx, inventory.Skills); len(plans) != 1 || plans[0].Blocked != "" {
		t.Fatalf("alias plan: %+v", plans)
	}
	globalPath := filepath.Join(home, ".agents", "skills", "global-demo")
	writeSkill(t, globalPath, "global-demo", "original")
	hashes, err := installedHashes(ctx, globalPath)
	if err != nil {
		t.Fatal(err)
	}
	writeLock(t, GlobalLockPath(), 3, map[string]any{"global-demo": map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/global-demo/SKILL.md", "skillFolderHash": hashes["git-tree"]}})
	bin := filepath.Join(home, "bin")
	mustMkdir(t, bin)
	writeExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\ncase \"$1\" in --version) echo 1.5.25;; --help) echo update;; esac\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	global, err := scopeInventory(ctx, home, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	for i := range global.Skills {
		global.Skills[i].UpdateStatus = UpdateAvailable
	}
	plans := PrepareUpdates(ctx, global.Skills)
	if len(plans) != 1 || plans[0].Blocked != "" {
		t.Fatalf("global dependency plan: %+v", plans)
	}
	plans[0].Names = append(plans[0].Names, "unselected")
	receipt := ApplyManagement(ctx, plans, nil)
	if len(receipt.Outcomes) == 0 || receipt.Outcomes[0].Status != "stale" {
		t.Fatal("changed preview was applied", receipt)
	}
}

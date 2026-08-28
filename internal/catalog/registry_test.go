package catalog_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

func idSequence(ids ...string) catalog.IDGenerator {
	index := 0
	return func() string {
		if index >= len(ids) {
			return ids[len(ids)-1]
		}
		id := ids[index]
		index++
		return id
	}
}

func TestRegistryMatchesExactCanonicalPathBeforeRemote(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.MkdirAll(filepath.Join(physical, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID)))
	registry := catalog.NewRegistry(store)
	original, err := registry.EnsureRepository(catalog.Observation{
		Host:           "laptop",
		Path:           physical,
		CommonDir:      filepath.Join(physical, ".git"),
		Name:           "repo",
		RemoteIdentity: "https://github.com/example/repo.git",
	})
	if err != nil {
		t.Fatal(err)
	}

	matched, err := registry.Match(catalog.Observation{
		Host:           "laptop",
		Path:           alias,
		RemoteIdentity: "github.com/someone/entirely-different",
	})
	if err != nil {
		t.Fatalf("Match alias: %v", err)
	}
	if matched.ID != original.ID {
		t.Errorf("canonical path match = %s, want %s", matched.ID, original.ID)
	}
	found, err := registry.FindByPath("laptop", alias)
	if err != nil || found.ID != original.ID {
		t.Errorf("FindByPath = %+v, %v", found, err)
	}
}

func TestRegistryMatchesGitCommonDirectory(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main")
	worktreePath := filepath.Join(root, "worktree")
	commonDir := filepath.Join(root, "git-common")
	for _, path := range []string{mainPath, worktreePath, commonDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(func() string { return firstID })))
	original, err := registry.EnsureRepository(catalog.Observation{
		Host: "desktop", Path: mainPath, CommonDir: commonDir, Name: "repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	matched, err := registry.Match(catalog.Observation{
		Host: "desktop", Path: worktreePath, GitCommonDir: commonDir, Name: "repo",
	})
	if err != nil || matched.ID != original.ID {
		t.Fatalf("common-dir Match = %+v, %v", matched, err)
	}
	found, err := registry.FindByCommonDir("desktop", commonDir)
	if err != nil || found.ID != original.ID {
		t.Errorf("FindByCommonDir = %+v, %v", found, err)
	}
}

func TestRegistryRemoteAmbiguityNeverAutoMergesClones(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "clone-one"),
		filepath.Join(root, "clone-two"),
		filepath.Join(root, "clone-three"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	thirdID := "33333333-3333-4333-8333-333333333333"
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID, thirdID)))
	registry := catalog.NewRegistry(store)
	remote := "git@github.com:example/shared.git"

	first, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: paths[0], Name: "shared", RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: paths[1], Name: "shared-copy", RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("a second live clone was auto-merged by remote identity")
	}

	_, err = registry.Match(catalog.Observation{
		Host: "laptop", Path: paths[2], Name: "third", RemoteIdentity: remote,
	})
	if !errors.Is(err, catalog.ErrAmbiguous) {
		t.Fatalf("ambiguous remote Match = %v, want ErrAmbiguous", err)
	}
	var ambiguous *catalog.AmbiguousMatchError
	if !errors.As(err, &ambiguous) || len(ambiguous.CandidateIDs) != 2 {
		t.Errorf("ambiguous candidates = %#v", err)
	}

	third, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: paths[2], Name: "third", RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID || third.ID == second.ID {
		t.Error("EnsureRepository chose an ambiguous remote candidate")
	}
	entries, err := store.List()
	if err != nil || len(entries) != 3 {
		t.Errorf("catalog should retain three clone identities: %d, %v", len(entries), err)
	}

	// Even with an ambiguous remote, an exact path is authoritative.
	matched, err := registry.Match(catalog.Observation{
		Host: "laptop", Path: paths[0], RemoteIdentity: remote,
	})
	if err != nil || matched.ID != first.ID {
		t.Errorf("exact path did not win: %+v, %v", matched, err)
	}
}

func TestRegistryMatchesRestorePathBeforeRemote(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive", "repo.tar.zst")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := filepath.Join(root, "repos", "repo")
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(func() string { return firstID }))
	entry := &catalog.Entry{
		Kind: catalog.Repository,
		Name: "repo",
		Locations: map[string]catalog.Location{
			"laptop": {State: catalog.Archived, CurrentPath: archive, RestorePath: restore},
		},
	}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	found, err := catalog.NewRegistry(store).FindByPath("laptop", restore)
	if err != nil || found.ID != entry.ID {
		t.Errorf("FindByPath(restore) = %+v, %v", found, err)
	}
}

func TestRegistryUsesUniqueRemoteForMissingRelocation(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old")
	newPath := filepath.Join(root, "new")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID))))
	remote := "https://github.com/example/moved.git"
	original, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: oldPath, Name: "moved", RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	relocated, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: newPath, Name: "moved", RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relocated.ID != original.ID {
		t.Errorf("unique missing relocation created %s, want stable %s", relocated.ID, original.ID)
	}
	location, ok := relocated.LocationFor("laptop")
	if !ok {
		t.Fatal("relocated entry has no laptop location")
	}
	if location.CurrentPath != newPath {
		t.Errorf("relocated path = %q, want %q", location.CurrentPath, newPath)
	}
}

func TestRegistryAttachesHostsAndPatchesAtomically(t *testing.T) {
	root := t.TempDir()
	laptop := filepath.Join(root, "laptop", "repo")
	server := filepath.Join(root, "server", "repo")
	for _, path := range []string{laptop, server} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(func() string { return firstID })))
	entry, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: laptop, Name: "repo", RemoteIdentity: "github.com/example/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err = registry.EnsureRepository(catalog.Observation{
		Host: "server", Path: server, Name: "repo", RemoteIdentity: "github.com/example/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Locations) != 2 {
		t.Fatalf("locations = %+v", entry.Locations)
	}

	entry, err = registry.Patch(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Tags = []string{" Work ", "work", "API"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.HasTag("api") || !entry.HasTag("WORK") || len(entry.Tags) != 2 {
		t.Errorf("Patch did not normalize tags: %v", entry.Tags)
	}

	listed, err := registry.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("Registry.List = %d entries, %v", len(listed), err)
	}
	loaded, err := registry.Get(entry.ID)
	if err != nil || loaded.ID != entry.ID {
		t.Fatalf("Registry.Get = %+v, %v", loaded, err)
	}
	if _, err := registry.Update(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Note = "updated through registry"
		return nil
	}); err != nil {
		t.Fatalf("Registry.Update: %v", err)
	}
}

func TestRegistryIncompleteCatalogBlocksInferenceButNotExactMatch(t *testing.T) {
	root := t.TempDir()
	existingPath := filepath.Join(root, "existing")
	newPath := filepath.Join(root, "new")
	for _, path := range []string{existingPath, newPath} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var reported []catalog.Diagnostic
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, "33333333-3333-4333-8333-333333333333")),
		catalog.WithDiagnosticSink(func(diagnostic catalog.Diagnostic) {
			reported = append(reported, diagnostic)
		}),
	)
	registry := catalog.NewRegistry(store)
	existing, err := registry.EnsureRepository(catalog.Observation{Host: "laptop", Path: existingPath})
	if err != nil {
		t.Fatal(err)
	}
	future := "schema_version = 99\nkind = { future = true }\n"
	if err := os.WriteFile(filepath.Join(store.Dir, secondID+".toml"), []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}

	exact, err := registry.Match(catalog.Observation{Host: "laptop", Path: existingPath})
	if err != nil || exact.ID != existing.ID {
		t.Fatalf("valid exact match was hidden by unrelated corruption: %+v, %v", exact, err)
	}
	if len(reported) != 1 {
		t.Fatalf("exact match should report skipped record once, got %d", len(reported))
	}
	if _, err := registry.EnsureRepository(catalog.Observation{Host: "laptop", Path: newPath}); !errors.Is(err, catalog.ErrIncompleteCatalog) || !errors.Is(err, catalog.ErrUnsupportedSchema) {
		t.Fatalf("incomplete catalog inference = %v", err)
	}
	if len(reported) != 1 {
		t.Fatalf("joined inference error should not also emit the diagnostic, got %d reports", len(reported))
	}
	entries, diagnostics, err := store.ListWithDiagnostics()
	if err != nil || len(entries) != 1 || len(diagnostics) != 1 {
		t.Fatalf("incomplete catalog was modified: %d entries, %d diagnostics, %v", len(entries), len(diagnostics), err)
	}
}

func TestRegistryDoesNotRelocatePastADeletedAlias(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.MkdirAll(filepath.Join(physical, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	secondClone := filepath.Join(root, "second-clone")
	if err := os.Mkdir(secondClone, 0o755); err != nil {
		t.Fatal(err)
	}
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID)))
	registry := catalog.NewRegistry(store)
	remote := "git@example.com:Team/Repo.git"
	first, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: alias, CommonDir: filepath.Join(physical, ".git"), RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}

	second, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: secondClone, RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("deleted navigation alias hid a still-live physical clone")
	}
	preserved, err := store.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := preserved.LocationFor("laptop")
	if !ok {
		t.Fatal("preserved entry has no laptop location")
	}
	canonicalPhysical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	if location.RealPath != canonicalPhysical {
		t.Errorf("first clone location was overwritten: %+v", location)
	}
}

func TestRegistryRejectsContradictoryAndNonLiveObservations(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(func() string { return firstID })))
	if _, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: first, RealPath: second,
	}); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("contradictory Path/RealPath should fail, got %v", err)
	}
	if _, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: filepath.Join(root, "missing"),
	}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing present location should fail, got %v", err)
	}
	if _, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: first, CommonDir: filepath.Join(root, "missing-git-dir"),
	}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing common directory should fail, got %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a repository directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Attach(firstID, catalog.Observation{Host: "laptop", Path: file}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file present location should fail, got %v", err)
	}
}

func TestRegistryPreservesCommonDirOnlyForSameLocation(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "repo")
	commonDir := filepath.Join(repoPath, ".git")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(func() string { return firstID }))
	registry := catalog.NewRegistry(store)
	remote := "git@example.com:Team/Repo.git"
	entry, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: repoPath, CommonDir: commonDir, RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err = registry.EnsureRepository(catalog.Observation{Host: "laptop", Path: repoPath})
	if err != nil {
		t.Fatal(err)
	}
	location, ok := entry.LocationFor("laptop")
	if !ok {
		t.Fatal("entry has no laptop location")
	}
	canonicalCommonDir, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if location.GitCommonDir != canonicalCommonDir {
		t.Errorf("same-path observation erased common dir: %+v", location)
	}

	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatal(err)
	}
	relocatedPath := filepath.Join(root, "relocated")
	if err := os.Mkdir(relocatedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	entry, err = registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: relocatedPath, RemoteIdentity: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	location, ok = entry.LocationFor("laptop")
	if !ok {
		t.Fatal("relocated entry has no laptop location")
	}
	if location.GitCommonDir != "" {
		t.Errorf("relocation retained stale common dir: %+v", location)
	}
}

func TestRemoteIdentityPreservesCaseSensitivePath(t *testing.T) {
	https := catalog.NormalizeRemoteIdentity("https://GitHub.com/Team/Repo.git")
	scp := catalog.NormalizeRemoteIdentity("git@github.com:Team/Repo.git")
	if https != "github.com/Team/Repo" || scp != https {
		t.Fatalf("HTTPS/SCP normalization differs: %q vs %q", https, scp)
	}
	if got := catalog.NormalizeRemoteIdentity("git@host:/srv/Repo.GIT"); got != "host/srv/Repo.GIT" {
		t.Errorf("absolute SCP normalization = %q", got)
	}
	if alice, bob := catalog.NormalizeRemoteIdentity("alice@host:Repo.git"),
		catalog.NormalizeRemoteIdentity("bob@host:Repo.git"); alice == bob {
		t.Errorf("home-relative SCP remotes for different users collapsed to %q", alice)
	}
	if encoded, literal := catalog.NormalizeRemoteIdentity("https://host/Org%2FRepo.git"),
		catalog.NormalizeRemoteIdentity("https://host/Org/Repo.git"); encoded == literal {
		t.Errorf("escaped and literal remote paths collapsed to %q", encoded)
	}
	for _, local := range []string{
		"../origin.git", "file:///srv/origin.git", `C:\repos\origin.git`, "ssh://host", "user@host",
	} {
		if got := catalog.NormalizeRemoteIdentity(local); got != "" {
			t.Errorf("local remote %q became relocation key %q", local, got)
		}
	}

	root := t.TempDir()
	upperPath := filepath.Join(root, "upper")
	lowerPath := filepath.Join(root, "lower")
	for _, path := range []string{upperPath, lowerPath} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID))))
	upper, err := registry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: upperPath, RemoteIdentity: "ssh://git@example.com/Team/Repo.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := registry.EnsureRepository(catalog.Observation{
		Host: "server", Path: lowerPath, RemoteIdentity: "ssh://git@example.com/team/repo.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if upper.ID == lower.ID {
		t.Fatal("case-sensitive remote paths were merged across hosts")
	}

	localRegistry := catalog.NewRegistry(catalog.NewStore(filepath.Join(root, "local-assets"),
		catalog.WithIDGenerator(idSequence(firstID, secondID))))
	firstLocal, err := localRegistry.EnsureRepository(catalog.Observation{
		Host: "laptop", Path: upperPath, RemoteIdentity: "../origin.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondLocal, err := localRegistry.EnsureRepository(catalog.Observation{
		Host: "server", Path: lowerPath, RemoteIdentity: "../origin.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstLocal.ID == secondLocal.ID {
		t.Fatal("host-local remote paths were used as cross-host identity")
	}
}

func TestRemoteIdentityCanonicalizesAzureHTTPSAndSSH(t *testing.T) {
	want := "azure-devops/acme/Platform Tools/api"
	for _, remote := range []string{
		"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api.git",
		"git@ssh.dev.azure.com:v3/acme/Platform%20Tools/api.git",
		"dev.azure.com/acme/Platform%20Tools/_git/api",
		"ssh.dev.azure.com/v3/acme/Platform%20Tools/api",
	} {
		if got := catalog.NormalizeRemoteIdentity(remote); got != want {
			t.Errorf("NormalizeRemoteIdentity(%q) = %q, want %q", remote, got, want)
		}
	}
}

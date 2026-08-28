package catalog_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

const (
	firstID  = "11111111-1111-4111-8111-111111111111"
	secondID = "22222222-2222-4222-8222-222222222222"
)

func fixedClock() time.Time {
	return time.Date(2026, time.August, 27, 9, 30, 0, 123, time.FixedZone("test", 8*60*60))
}

func sampleEntry(root string) *catalog.Entry {
	return &catalog.Entry{
		Kind:           catalog.KindTry,
		Name:           "  Redis Streams  ",
		Note:           "compare consumer groups",
		Tags:           []string{" Go ", "infra", "GO", ""},
		RemoteIdentity: "github.com/example/redis-streams",
		Experiment: &catalog.Experiment{
			Phase:        catalog.PhaseActive,
			Slug:         "redis-streams",
			OriginURL:    "https://github.com/example/redis-streams.git",
			OriginalPath: filepath.Join(root, "tries", "redis-streams"),
		},
		Locations: map[string]catalog.Location{
			"laptop": {
				State:        catalog.LocationPresent,
				CurrentPath:  filepath.Join(root, "tries", "redis-streams"),
				RealPath:     filepath.Join(root, "tries", "redis-streams"),
				GitCommonDir: filepath.Join(root, "tries", "redis-streams", ".git"),
			},
			"server": {
				State:       catalog.LocationArchived,
				CurrentPath: filepath.Join(root, "archives", "redis-streams.tar.zst"),
				RestorePath: "/srv/tries/redis-streams",
			},
		},
		RecoveryReceipt: &catalog.RecoveryReceipt{
			Host:           "server",
			Method:         "archive",
			ArchivePath:    filepath.Join(root, "archives", "redis-streams.tar.zst"),
			RestorePath:    "/srv/tries/redis-streams",
			RemoteIdentity: "https://github.com/example/redis-streams.git",
			RemoteName:     "origin",
			RemoteURL:      "https://github.com/example/redis-streams.git",
			RefsDigest:     "sha256:refs",
			Checksum:       "sha256:abc123",
			Verified:       fixedClock(),
		},
		MoveIntent: &catalog.MoveIntent{
			Host:            "laptop",
			Operation:       "archive",
			SourcePath:      filepath.Join(root, "tries", "redis-streams"),
			DestinationPath: filepath.Join(root, "archives", "redis-streams"),
		},
	}
}

func newFixedStore(dir string) *catalog.Store {
	return catalog.NewStore(dir,
		catalog.WithIDGenerator(func() string { return firstID }),
		catalog.WithClock(fixedClock),
	)
}

func TestStoreRoundTripAndFilenameIdentity(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	entry := sampleEntry(dir)

	if err := store.Create(entry); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.ID != firstID {
		t.Errorf("ID = %q, want injected UUID", entry.ID)
	}
	if entry.SchemaVersion != catalog.CurrentSchemaVersion {
		t.Errorf("schema = %d", entry.SchemaVersion)
	}
	if entry.Name != "Redis Streams" {
		t.Errorf("name was not trimmed: %q", entry.Name)
	}
	if want := []string{"go", "infra"}; !reflect.DeepEqual(entry.Tags, want) {
		t.Errorf("tags = %v, want %v", entry.Tags, want)
	}
	if entry.Created.Location() != time.UTC || !entry.Created.Equal(fixedClock()) {
		t.Errorf("created = %v, want UTC %v", entry.Created, fixedClock())
	}

	path := filepath.Join(dir, firstID+".toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\nid =") || strings.HasPrefix(string(body), "id =") {
		t.Errorf("ID must live only in the filename:\n%s", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("record mode = %v, want 0644", info.Mode().Perm())
	}

	got, err := store.Get(firstID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != firstID || got.Experiment == nil || got.Experiment.Slug != "redis-streams" ||
		got.Experiment.OriginURL != entry.Experiment.OriginURL ||
		got.Experiment.OriginalPath != entry.Experiment.OriginalPath {
		t.Errorf("experiment/identity did not round trip: %+v", got)
	}
	for _, host := range []string{"laptop", "server"} {
		location, ok := got.LocationFor(host)
		if !ok || location.State != entry.Locations[host].State || location.CurrentPath != entry.Locations[host].CurrentPath {
			t.Errorf("location %s did not round trip: %+v", host, location)
		}
	}
	if got.RecoveryReceipt == nil || got.RecoveryReceipt.Checksum != "sha256:abc123" ||
		got.RecoveryReceipt.RemoteName != "origin" || got.RecoveryReceipt.RefsDigest != "sha256:refs" ||
		got.RecoveryReceipt.RemoteIdentity != "github.com/example/redis-streams" || got.MoveIntent == nil {
		t.Errorf("recovery/move metadata did not round trip: %+v", got)
	}
}

func TestUpdatePreservesIdentityAndIsAtomicOnMutationFailure(t *testing.T) {
	dir := t.TempDir()
	currentTime := fixedClock()
	store := catalog.NewStore(dir,
		catalog.WithIDGenerator(func() string { return firstID }),
		catalog.WithClock(func() time.Time { return currentTime }),
	)
	entry := sampleEntry(dir)
	entry.MoveIntent = nil
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	created, discovered := entry.Created, entry.Discovered
	path := filepath.Join(dir, firstID+".toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("stop update")
	if _, err := store.Update(firstID, func(candidate *catalog.Entry) error {
		candidate.Note = "must not persist"
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want sentinel", err)
	}
	afterRejected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRejected) != string(before) {
		t.Error("a rejected mutation changed the record")
	}

	currentTime = currentTime.Add(time.Minute)
	updated, err := store.Update(firstID, func(candidate *catalog.Entry) error {
		candidate.Note = "new note"
		candidate.Tags = append(candidate.Tags, " DATABASE ")
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != firstID || !updated.Created.Equal(created) || !updated.Discovered.Equal(discovered) {
		t.Errorf("stable fields changed: %+v", updated)
	}
	if updated.Note != "new note" || !updated.HasTag("database") || !updated.Updated.Equal(currentTime) {
		t.Errorf("update not applied: %+v", updated)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestListMissingDirectoryAndDeterministicOrder(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "not-created")
	store := catalog.NewStore(missing)
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing directory = %d entries, %v", len(entries), err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("List should not create its directory, stat error = %v", err)
	}
	invalidStore := catalog.NewStore(missing, catalog.WithIDGenerator(func() string { return firstID }))
	if err := invalidStore.Create(&catalog.Entry{Kind: catalog.Repository}); err == nil {
		t.Fatal("invalid entry should be rejected")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("invalid Create should not create its directory, stat error = %v", err)
	}

	ids := []string{secondID, firstID}
	index := 0
	store = catalog.NewStore(missing, catalog.WithIDGenerator(func() string {
		id := ids[index]
		index++
		return id
	}))
	for _, name := range []string{"second", "first"} {
		if err := store.Create(&catalog.Entry{Kind: catalog.Repository, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != firstID || entries[1].ID != secondID {
		t.Errorf("List order = %+v", entries)
	}
}

func TestListIsolatesCorruptionAndReportsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	var diagnostics []catalog.Diagnostic
	store := catalog.NewStore(dir,
		catalog.WithIDGenerator(func() string { return firstID }),
		catalog.WithDiagnosticSink(func(diagnostic catalog.Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		}),
	)
	if err := store.Create(&catalog.Entry{Kind: catalog.Repository, Name: "good"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("corruption must not fail the directory listing: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != firstID {
		t.Errorf("valid record was hidden: %+v", entries)
	}
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "broken.toml") {
		t.Errorf("diagnostics = %+v", diagnostics)
	}

	entries, explicit, err := store.ListWithDiagnostics()
	if err != nil || len(entries) != 1 || len(explicit) != 1 {
		t.Errorf("ListWithDiagnostics = %d entries, %d diagnostics, %v", len(entries), len(explicit), err)
	}
}

func TestUnknownSchemaIsSurfacedAndNeverGuessed(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	if err := store.Create(&catalog.Entry{Kind: catalog.Repository, Name: "good"}); err != nil {
		t.Fatal(err)
	}
	futureID := secondID
	// The future kind deliberately has an incompatible shape. Reading the schema
	// header first must still produce the typed version error rather than trying
	// to decode it as today's string field.
	future := `schema_version = 99
kind = { future = true }
`
	if err := os.WriteFile(filepath.Join(dir, futureID+".toml"), []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(futureID); !errors.Is(err, catalog.ErrUnsupportedSchema) {
		t.Fatalf("Get future schema = %v, want ErrUnsupportedSchema", err)
	} else {
		var schemaError *catalog.UnsupportedSchemaError
		if !errors.As(err, &schemaError) || schemaError.Version != 99 {
			t.Errorf("typed schema error = %#v", err)
		}
	}
	entries, diagnostics, err := store.ListWithDiagnostics()
	if err != nil || len(entries) != 1 || len(diagnostics) != 1 {
		t.Fatalf("ListWithDiagnostics = %d entries, %d diagnostics, %v", len(entries), len(diagnostics), err)
	}
	if !errors.Is(diagnostics[0], catalog.ErrUnsupportedSchema) {
		t.Errorf("schema diagnostic lost its cause: %v", diagnostics[0])
	}
}

func TestUpdateRejectsStableFieldChanges(t *testing.T) {
	store := newFixedStore(t.TempDir())
	entry := &catalog.Entry{Kind: catalog.Repository, Name: "repo"}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*catalog.Entry){
		"ID":         func(candidate *catalog.Entry) { candidate.ID = secondID },
		"Created":    func(candidate *catalog.Entry) { candidate.Created = candidate.Created.Add(time.Hour) },
		"Discovered": func(candidate *catalog.Entry) { candidate.Discovered = candidate.Discovered.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Update(entry.ID, func(candidate *catalog.Entry) error {
				mutate(candidate)
				return nil
			}); err == nil {
				t.Error("stable-field mutation should fail")
			}
		})
	}
}

func TestGetRejectsUnknownSchemaFields(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	entry := &catalog.Entry{Kind: catalog.Repository, Name: "repo"}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, entry.ID+".toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append([]byte("mystery = true\n"), body...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(entry.ID); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown schema field should be rejected, got %v", err)
	}
	entries, diagnostics, err := store.ListWithDiagnostics()
	if err != nil || len(entries) != 0 || len(diagnostics) != 1 {
		t.Fatalf("unknown field listing = %d entries, %d diagnostics, %v", len(entries), len(diagnostics), err)
	}
}

func TestGetDoesNotGuessMissingExperimentPhase(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	entry := sampleEntry(dir)
	entry.RecoveryReceipt = nil
	entry.MoveIntent = nil
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, entry.ID+".toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	filtered := lines[:0]
	removed := false
	for _, line := range lines {
		if !removed && strings.HasPrefix(strings.TrimSpace(line), "phase =") {
			removed = true
			continue
		}
		filtered = append(filtered, line)
	}
	if !removed {
		t.Fatal("encoded experiment had no phase field")
	}
	if err := os.WriteFile(path, []byte(strings.Join(filtered, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(entry.ID); err == nil || !strings.Contains(err.Error(), "experiment phase") {
		t.Fatalf("missing phase should be diagnosed, got %v", err)
	}
}

func TestUpdateDoesNotGuessClearedExperimentPhase(t *testing.T) {
	dir := t.TempDir()
	store := newFixedStore(dir)
	entry := sampleEntry(dir)
	entry.RecoveryReceipt = nil
	entry.MoveIntent = nil
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Experiment.Phase = ""
		return nil
	}); err == nil || !strings.Contains(err.Error(), "experiment phase") {
		t.Fatalf("cleared phase should be rejected, got %v", err)
	}
}

func TestValidateRejectsIncompleteNestedMetadata(t *testing.T) {
	now := fixedClock()
	base := catalog.Entry{
		SchemaVersion: catalog.CurrentSchemaVersion,
		ID:            firstID,
		Kind:          catalog.Repository,
		Name:          "repo",
		Created:       now,
		Discovered:    now,
		Updated:       now,
	}

	missingLocationTime := base
	missingLocationTime.Locations = map[string]catalog.Location{
		"laptop": {State: catalog.Present, CurrentPath: "/repo"},
	}
	if err := missingLocationTime.Validate(); err == nil || !strings.Contains(err.Error(), "updated timestamp") {
		t.Errorf("zero location timestamp should fail, got %v", err)
	}

	unverified := base
	unverified.RecoveryReceipt = &catalog.RecoveryReceipt{Host: "laptop", Method: "remote", Created: now}
	if err := unverified.Validate(); err == nil || !strings.Contains(err.Error(), "verified") {
		t.Errorf("unverified receipt should fail, got %v", err)
	}

	missingOperation := base
	missingOperation.MoveIntent = &catalog.MoveIntent{
		Host: "laptop", SourcePath: "/repo", DestinationPath: "/archive/repo", Started: now,
	}
	if err := missingOperation.Validate(); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Errorf("move intent without operation should fail, got %v", err)
	}

	equivalentPaths := base
	equivalentPaths.MoveIntent = &catalog.MoveIntent{
		Host: "laptop", Operation: "archive", SourcePath: "/repo", DestinationPath: "/repo/.", Started: now,
	}
	if err := equivalentPaths.Validate(); err == nil || !strings.Contains(err.Error(), "identical") {
		t.Errorf("equivalent move paths should fail, got %v", err)
	}

	if filepath.Separator != '\\' {
		distinctUnixPaths := base
		distinctUnixPaths.MoveIntent = &catalog.MoveIntent{
			Host: "laptop", Operation: "archive", SourcePath: `/repo/a\b`, DestinationPath: "/repo/a/b", Started: now,
		}
		if err := distinctUnixPaths.Validate(); err != nil {
			t.Errorf("distinct Unix paths should remain distinct, got %v", err)
		}
	}
}

func TestUpdateStampsOnlyChangedLocations(t *testing.T) {
	dir := t.TempDir()
	currentTime := fixedClock()
	store := catalog.NewStore(dir,
		catalog.WithIDGenerator(func() string { return firstID }),
		catalog.WithClock(func() time.Time { return currentTime }),
	)
	entry := &catalog.Entry{
		Kind: catalog.Repository,
		Name: "repo",
		Locations: map[string]catalog.Location{
			"laptop": {State: catalog.Present, CurrentPath: filepath.Join(dir, "repo")},
		},
	}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	originalLocation := entry.Locations["laptop"].Updated

	currentTime = currentTime.Add(time.Minute)
	unchanged, err := store.Update(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Note = "metadata only"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := unchanged.Locations["laptop"].Updated; !got.Equal(originalLocation) {
		t.Errorf("metadata-only update changed location time from %v to %v", originalLocation, got)
	}

	currentTime = currentTime.Add(time.Minute)
	changed, err := store.Update(entry.ID, func(candidate *catalog.Entry) error {
		location := candidate.Locations["laptop"]
		location.RealPath = filepath.Join(dir, "physical-repo")
		candidate.Locations["laptop"] = location
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := changed.Locations["laptop"].Updated; !got.Equal(currentTime) {
		t.Errorf("changed location time = %v, want %v", got, currentTime)
	}
}

func TestStoreWithLockSerializesAndStaysOutsideAssetDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	first := catalog.NewStore(dir)
	second := catalog.NewStore(dir)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.WithLock(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	called := make(chan struct{}, 1)
	go func() {
		secondDone <- second.WithLock(ctx, func() error {
			called <- struct{}{}
			return nil
		})
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second lock completed while first was held: %v", err)
	case <-time.After(50 * time.Millisecond):
		cancel()
	}
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock = %v, want context.Canceled", err)
	}
	select {
	case <-called:
		t.Fatal("canceled waiter ran its operation")
	default:
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("operation failed")
	if err := second.WithLock(context.Background(), func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("locked operation error = %v, want sentinel", err)
	}
	if err := first.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock after failed operation: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("coordination files leaked into asset directory: %v", entries)
	}
}

func TestStoreLockReleasedWhenHolderProcessExits(t *testing.T) {
	if os.Getenv("DEV_CATALOG_LOCK_HELPER") == "1" {
		store := catalog.NewStore(os.Getenv("DEV_CATALOG_LOCK_DIR"))
		err := store.WithLock(context.Background(), func() error {
			if err := os.WriteFile(os.Getenv("DEV_CATALOG_LOCK_READY"), []byte("ready"), 0o600); err != nil {
				return err
			}
			for {
				time.Sleep(time.Hour)
			}
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}

	root := t.TempDir()
	dir := filepath.Join(root, "assets")
	ready := filepath.Join(root, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestStoreLockReleasedWhenHolderProcessExits$")
	command.Env = append(os.Environ(),
		"DEV_CATALOG_LOCK_HELPER=1",
		"DEV_CATALOG_LOCK_DIR="+dir,
		"DEV_CATALOG_LOCK_READY="+ready,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock holder did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := catalog.NewStore(dir).WithLock(waitCtx, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock = %v, want context deadline", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed lock helper exited successfully")
	}
	if err := catalog.NewStore(dir).WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock was not released after holder process exited: %v", err)
	}
}

func TestValidateKeepsKindDistinctFromExperimentTagsAndLifecycle(t *testing.T) {
	now := fixedClock()
	entry := func(kind catalog.Kind, experiment *catalog.Experiment) catalog.Entry {
		return catalog.Entry{
			SchemaVersion: catalog.CurrentSchemaVersion,
			ID:            firstID,
			Kind:          kind,
			Name:          "asset",
			Experiment:    experiment,
			Created:       now,
			Discovered:    now,
			Updated:       now,
		}
	}

	tryWithoutMetadata := entry(catalog.KindTry, nil)
	if err := tryWithoutMetadata.Validate(); err == nil || !strings.Contains(err.Error(), "requires experiment metadata") {
		t.Errorf("try without experiment metadata should fail, got %v", err)
	}

	active := &catalog.Experiment{
		Phase: catalog.PhaseActive, Slug: "asset", Started: now, OriginalPath: "/tries/asset",
	}
	activeRepository := entry(catalog.KindRepository, active)
	if err := activeRepository.Validate(); err == nil || !strings.Contains(err.Error(), "must be graduated") {
		t.Errorf("active experiment recorded as repository should fail, got %v", err)
	}
	activeTry := entry(catalog.KindTry, active)
	if err := activeTry.Validate(); err != nil {
		t.Errorf("active try with provenance should be valid: %v", err)
	}

	missingSlug := *active
	missingSlug.Slug = ""
	if err := entry(catalog.KindTry, &missingSlug).Validate(); err == nil || !strings.Contains(err.Error(), "slug is required") {
		t.Errorf("experiment without a slug should fail, got %v", err)
	}

	missingOrigin := *active
	missingOrigin.OriginalPath = ""
	if err := entry(catalog.KindTry, &missingOrigin).Validate(); err == nil || !strings.Contains(err.Error(), "original path") {
		t.Errorf("experiment without an original path should fail, got %v", err)
	}

	deprecated := &catalog.Experiment{
		Phase:          catalog.PhaseDeprecated,
		Slug:           "asset",
		Started:        now,
		OriginalPath:   "/tries/asset",
		DeprecatedAt:   now,
		DeprecatedPath: "/tries/asset",
	}
	if err := entry(catalog.KindTry, deprecated).Validate(); err != nil {
		t.Errorf("deprecated try with provenance should be valid: %v", err)
	}
	deprecated.DeprecatedPath = ""
	if err := entry(catalog.KindTry, deprecated).Validate(); err == nil || !strings.Contains(err.Error(), "deprecated experiment") {
		t.Errorf("deprecated try without provenance should fail, got %v", err)
	}

	graduated := &catalog.Experiment{
		Phase:         catalog.PhaseGraduated,
		Slug:          "asset",
		Started:       now,
		OriginalPath:  "/tries/asset",
		GraduatedAt:   now,
		GraduatedPath: "/projects/asset",
	}
	graduatedTry := entry(catalog.KindTry, graduated)
	if err := graduatedTry.Validate(); err == nil || !strings.Contains(err.Error(), "must be a repository") {
		t.Errorf("graduated experiment recorded as try should fail, got %v", err)
	}
	missingGraduatedPath := *graduated
	missingGraduatedPath.GraduatedPath = ""
	if err := entry(catalog.KindRepository, &missingGraduatedPath).Validate(); err == nil || !strings.Contains(err.Error(), "graduated experiment") {
		t.Errorf("graduated repository without provenance should fail, got %v", err)
	}
	graduatedRepository := entry(catalog.KindRepository, graduated)
	if err := graduatedRepository.Validate(); err != nil {
		t.Errorf("graduated repository history should remain valid: %v", err)
	}

	ordinary := entry(catalog.KindRepository, nil)
	ordinary.Tags = []string{"experiment"}
	if err := ordinary.Validate(); err != nil {
		t.Errorf("experiment tag must not create Try lifecycle: %v", err)
	}
}

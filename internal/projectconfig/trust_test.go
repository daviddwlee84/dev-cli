package projectconfig_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

func TestCanonicalRepoIdentityUsesGitCommonDirectory(t *testing.T) {
	repository := gittest.New(t)
	worktree := filepath.Join(filepath.Dir(repository.Root), "linked")
	repository.Git("worktree", "add", "-b", "feature", worktree)

	mainIdentity, err := projectconfig.CanonicalRepoIdentity(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	linkedIdentity, err := projectconfig.CanonicalRepoIdentity(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if mainIdentity != linkedIdentity {
		t.Fatalf("linked worktree identity %q != main identity %q", linkedIdentity, mainIdentity)
	}
	if filepath.Base(mainIdentity) != ".git" {
		t.Fatalf("identity should be Git common directory, got %q", mainIdentity)
	}
}

func TestTrustStoreCanonicalizesAliasesAndInvalidatesChangedHash(t *testing.T) {
	repository := gittest.New(t)
	alias := filepath.Join(filepath.Dir(repository.Root), "repo-alias")
	if err := os.Symlink(repository.Root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := projectconfig.NewTrustStore(filepath.Join(t.TempDir(), "state", "project-config-trust.json"))
	approvedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	store.Clock = func() time.Time { return approvedAt }
	firstHash := "sha256:" + strings.Repeat("1", 64)
	secondHash := "sha256:" + strings.Repeat("2", 64)

	record, err := store.Approve(context.Background(), alias, firstHash)
	if err != nil {
		t.Fatal(err)
	}
	if record.Repository == alias || record.ApprovedAt.Location() != time.UTC {
		t.Fatalf("record was not canonical/UTC: %+v", record)
	}
	trusted, err := store.Check(context.Background(), repository.Root, firstHash)
	if err != nil || !trusted {
		t.Fatalf("real path did not share alias approval: trusted=%v err=%v", trusted, err)
	}
	trusted, err = store.Check(context.Background(), repository.Root, secondHash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("changed execution hash remained trusted")
	}

	if _, err := store.Approve(context.Background(), repository.Root, secondHash); err != nil {
		t.Fatal(err)
	}
	trusted, err = store.Check(context.Background(), repository.Root, firstHash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("approving changed content retained the stale approval")
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ExecutionHash != secondHash {
		t.Fatalf("records = %+v", records)
	}
	removed, err := store.Revoke(context.Background(), alias)
	if err != nil || !removed {
		t.Fatalf("revoke = %v, %v", removed, err)
	}
	if records, err := store.List(); err != nil || len(records) != 0 {
		t.Fatalf("records after revoke = %+v, %v", records, err)
	}
}

func TestTrustStoreDoesNotPersistConfigOrSecretContent(t *testing.T) {
	repository := gittest.New(t)
	secret := "TOP-SECRET-bootstrap-command"
	writeProjectFile(t, repository.Root, projectconfig.ConfigFilename, "[worktree]\npost_create = [\"echo "+secret+"\"]\n")
	loaded, err := projectconfig.Load(repository.Root, nil)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "private", "trust.json")
	store := projectconfig.NewTrustStore(storePath)
	if _, err := store.Approve(context.Background(), repository.Root, loaded.ExecutionHash); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{secret, "post_create", "echo ", "config.toml"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("trust store persisted forbidden config content %q: %s", forbidden, text)
		}
	}
	var raw struct {
		Version int                      `json:"version"`
		Records []map[string]interface{} `json:"records"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Version != projectconfig.TrustStoreVersion || len(raw.Records) != 1 {
		t.Fatalf("trust file = %+v", raw)
	}
	if len(raw.Records[0]) != 3 {
		t.Fatalf("trust record should contain identity/hash/timestamp only: %v", raw.Records[0])
	}
	for _, key := range []string{"repository", "execution_hash", "approved_at"} {
		if _, ok := raw.Records[0][key]; !ok {
			t.Fatalf("missing trust record key %q: %v", key, raw.Records[0])
		}
	}
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("trust store mode = %o", info.Mode().Perm())
	}
}

func TestTrustStoreRejectsMalformedStateAndHashes(t *testing.T) {
	repository := gittest.New(t)
	path := filepath.Join(t.TempDir(), "trust.json")
	store := projectconfig.NewTrustStore(path)
	if _, err := store.Check(context.Background(), repository.Root, "not-a-hash"); err == nil {
		t.Fatal("invalid execution hash should fail closed")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"records":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("malformed trust state should fail closed")
	}
}

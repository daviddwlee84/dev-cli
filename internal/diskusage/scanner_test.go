package diskusage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestScanPlainCountsLogicalFilesWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	sparse := filepath.Join(root, "sparse")
	file, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(outside, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	measuredAt := time.Date(2026, time.August, 28, 1, 0, 0, 0, time.UTC)
	usage, err := (diskusage.Scanner{Clock: func() time.Time { return measuredAt }}).Scan(
		context.Background(), diskusage.Plain(root))
	if err != nil {
		t.Fatal(err)
	}
	want := int64(5+(1<<20)) + linkInfo.Size()
	if usage.CheckoutBytes != want || usage.OwnedBytes != want || usage.TotalBytes == nil || *usage.TotalBytes != want {
		t.Fatalf("plain usage = %+v, want %d logical bytes", usage, want)
	}
	if usage.PrivateGitBytes != nil || usage.SharedGitBytes != nil || !usage.Complete || !usage.MeasuredAt.Equal(measuredAt) {
		t.Errorf("plain ownership metadata = %+v", usage)
	}
}

func TestScanStandaloneRepositoryOwnsPrivateGitDirectory(t *testing.T) {
	repository := gittest.New(t)
	discovered, err := gitx.Discover(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if discovered.GitDir == "" || discovered.GitDir != discovered.GitCommonDir {
		t.Fatalf("Git discovery lacks checkout-specific directory: %+v", discovered)
	}
	usage, err := diskusage.Scan(context.Background(), diskusage.FromGit(discovered, 0))
	if err != nil {
		t.Fatal(err)
	}
	if usage.PrivateGitBytes == nil || *usage.PrivateGitBytes == 0 || usage.SharedGitBytes != nil ||
		usage.TotalBytes == nil || *usage.TotalBytes != usage.OwnedBytes || usage.OwnedBytes <= usage.CheckoutBytes {
		t.Fatalf("standalone ownership = %+v", usage)
	}
}

func TestScanSeparatesMainAndLinkedWorktreeGitOwnership(t *testing.T) {
	repository := gittest.New(t)
	linkedPath := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(context.Background(), repository.Root, linkedPath, "feat/size", "main"); err != nil {
		t.Fatal(err)
	}
	mainRepo, err := gitx.Discover(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	linkedRepo, err := gitx.Discover(context.Background(), linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !linkedRepo.IsLinkedWorktree || linkedRepo.GitDir == linkedRepo.GitCommonDir {
		t.Fatalf("linked discovery = %+v", linkedRepo)
	}

	mainUsage, err := diskusage.Scan(context.Background(), diskusage.FromGit(mainRepo, 1))
	if err != nil {
		t.Fatal(err)
	}
	if mainUsage.PrivateGitBytes != nil || mainUsage.SharedGitBytes == nil || *mainUsage.SharedGitBytes == 0 || mainUsage.TotalBytes != nil {
		t.Fatalf("main checkout with worktree ownership = %+v", mainUsage)
	}
	linkedUsage, err := diskusage.Scan(context.Background(), diskusage.FromGit(linkedRepo, 1))
	if err != nil {
		t.Fatal(err)
	}
	if linkedUsage.PrivateGitBytes == nil || *linkedUsage.PrivateGitBytes == 0 ||
		linkedUsage.SharedGitBytes == nil || *linkedUsage.SharedGitBytes == 0 || linkedUsage.TotalBytes != nil {
		t.Fatalf("linked checkout ownership = %+v", linkedUsage)
	}
	if linkedUsage.OwnedBytes != linkedUsage.CheckoutBytes+*linkedUsage.PrivateGitBytes {
		t.Errorf("linked owned bytes double-counted shared storage: %+v", linkedUsage)
	}
}

func TestScanExternalGitDirectoryIsSharedNotCheckoutOwned(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitDir := filepath.Join(root, "admin.git")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	command := gittest.New(t)
	command.GitIn(checkout, "init", "--initial-branch=main", "--separate-git-dir", gitDir)
	discovered, err := gitx.Discover(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := diskusage.Scan(context.Background(), diskusage.FromGit(discovered, 0))
	if err != nil {
		t.Fatal(err)
	}
	if usage.PrivateGitBytes != nil || usage.SharedGitBytes == nil || *usage.SharedGitBytes == 0 || usage.TotalBytes != nil {
		t.Fatalf("external Git directory ownership = %+v", usage)
	}
}

func TestScanHonorsCancellationAndMissingRoots(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := diskusage.Scan(ctx, diskusage.Plain(root)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled scan = %v", err)
	}
	if _, err := diskusage.Scan(context.Background(), diskusage.Plain(filepath.Join(root, "missing"))); err == nil {
		t.Fatal("missing root scan succeeded")
	}
}

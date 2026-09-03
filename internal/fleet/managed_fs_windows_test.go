//go:build windows

package fleet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDefaultManagedFragmentWriterProtectsUpdatesAndRemoves(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedWindowsPathIdentity(filepath.Dir(path), true); err != nil {
		t.Fatalf("managed directory DACL: %v", err)
	}
	if _, err := readManagedWindowsSnapshot(path); err != nil {
		t.Fatalf("managed fragment DACL: %v", err)
	}

	host.Name = "renamed"
	if _, err := WriteManagedFragment(context.Background(), primary, host, nil); err != nil {
		t.Fatal(err)
	}
	parsed, err := ValidateManagedFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != host {
		t.Fatalf("updated fragment = %+v, want %+v", parsed, host)
	}
	if _, err := RemoveManagedFragment(context.Background(), primary, host.SSHAlias, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("removed fragment remains: %v", err)
	}
}

func TestWindowsDefaultManagedFragmentWriterRejectsInheritedDirectoryDACL(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	directory := ManagedFragmentDir(primary)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := WriteManagedFragment(context.Background(), primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}, nil)
	if err == nil || !errors.Is(err, ErrManagedFragmentConflict) {
		t.Fatalf("inherited directory DACL error = %v", err)
	}
}

func TestWindowsManagedFragmentRejectsReparseAncestor(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "remotes.toml")
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, ManagedFragmentDir(primary)); err != nil {
		t.Skipf("Windows symlink privilege or developer mode unavailable: %v", err)
	}
	_, err := WriteManagedFragment(context.Background(), primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}, nil)
	if err == nil || !errors.Is(err, ErrManagedFragmentConflict) {
		t.Fatalf("reparse directory error = %v", err)
	}
}

func TestWindowsManagedFragmentRevalidatesExpectedSourceBytes(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := RenderManagedFragment(ManagedHost{Name: "updated", SSHAlias: "lab", RemoteOS: RemoteOSWindows})
	if err != nil {
		t.Fatal(err)
	}
	writeErr := writeManagedFragmentOS(ManagedFragmentWriteRequest{
		Path: path, Content: updated, PreviousContent: []byte("stale"), Existed: true,
	})
	if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
		t.Fatalf("stale replacement error = %v", writeErr)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("stale replacement changed the fragment")
	}
	removeErr := removeManagedFragmentOS(ManagedFragmentRemoveRequest{Path: path, ExpectedContent: []byte("stale")})
	if removeErr == nil || !errors.Is(removeErr, ErrManagedFragmentConflict) {
		t.Fatalf("stale removal error = %v", removeErr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale removal deleted the fragment: %v", err)
	}
}

func TestWindowsManagedFragmentAtomicReplacementPreservesConcurrentTarget(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	initial := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	path, err := WriteManagedFragment(context.Background(), primary, initial, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	concurrent := ManagedHost{Name: "concurrent", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	concurrentBytes, err := RenderManagedFragment(concurrent)
	if err != nil {
		t.Fatal(err)
	}
	concurrentPath := filepath.Join(filepath.Dir(path), ".concurrent.toml")
	writeProtectedWindowsFile(t, concurrentPath, concurrentBytes)
	var backupPath string
	managedWindowsAfterBackup = func(backup, target string) {
		backupPath = backup
		from, _ := windows.UTF16PtrFromString(concurrentPath)
		to, _ := windows.UTF16PtrFromString(target)
		if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { managedWindowsAfterBackup = nil })
	_, writeErr := WriteManagedFragment(context.Background(), primary, ManagedHost{
		Name: "desired", SSHAlias: "lab", RemoteOS: RemoteOSWindows,
	}, nil)
	managedWindowsAfterBackup = nil
	if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
		t.Fatalf("concurrent replacement error = %v", writeErr)
	}
	installed, err := ValidateManagedFragment(path)
	if err != nil || installed != concurrent {
		t.Fatalf("concurrent target = %+v, err %v", installed, err)
	}
	if backupPath == "" {
		t.Fatal("atomic replacement did not retain a private backup")
	}
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil || !bytes.Equal(backupBytes, initialBytes) {
		t.Fatalf("private backup did not preserve original bytes: %q, err %v", backupBytes, err)
	}
}

func TestWindowsManagedFragmentRejectsStagingByteMutation(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := RenderManagedFragment(ManagedHost{Name: "updated", SSHAlias: "lab", RemoteOS: RemoteOSWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { managedWindowsBeforePublish = nil }()
	managedWindowsBeforePublish = func(stagedPath, _ string) {
		if err := os.WriteFile(stagedPath, []byte("mutated staging bytes"), 0o600); err != nil {
			t.Fatalf("mutate staging bytes: %v", err)
		}
	}
	writeErr := writeManagedFragmentOS(ManagedFragmentWriteRequest{
		Path: path, Content: updated, PreviousContent: original, Existed: true,
	})
	managedWindowsBeforePublish = nil
	if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
		t.Fatalf("staging byte mutation error = %v", writeErr)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("mutated staging bytes were published")
	}
}

func TestWindowsManagedFragmentRejectsStagingNameReplacement(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSWindows}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := RenderManagedFragment(ManagedHost{Name: "updated", SSHAlias: "lab", RemoteOS: RemoteOSWindows})
	if err != nil {
		t.Fatal(err)
	}

	var replacementPath, movedOwnedPath string
	attackerBytes := []byte("unexpected staging replacement")
	defer func() { managedWindowsBeforePublish = nil }()
	managedWindowsBeforePublish = func(stagedPath, _ string) {
		replacementPath = stagedPath
		movedOwnedPath = stagedPath + ".owned"
		if err := os.Rename(stagedPath, movedOwnedPath); err != nil {
			t.Fatalf("move owned staging file: %v", err)
		}
		writeProtectedWindowsFile(t, stagedPath, attackerBytes)
	}
	writeErr := writeManagedFragmentOS(ManagedFragmentWriteRequest{
		Path: path, Content: updated, PreviousContent: original, Existed: true,
	})
	managedWindowsBeforePublish = nil
	if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
		t.Fatalf("staging replacement error = %v", writeErr)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("unexpected staging replacement was published")
	}
	replacement, err := os.ReadFile(replacementPath)
	if err != nil {
		t.Fatalf("unowned staging replacement was removed: %v", err)
	}
	if !bytes.Equal(replacement, attackerBytes) {
		t.Fatalf("unowned staging replacement = %q", replacement)
	}
	if _, err := os.Stat(movedOwnedPath); !os.IsNotExist(err) {
		t.Fatalf("owned staging object was not deleted through its held handle: %v", err)
	}
}

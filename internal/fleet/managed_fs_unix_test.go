//go:build !windows

package fleet

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDefaultManagedFragmentWriterModesIdempotencyAndRemoval(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("managed modes = directory %04o file %04o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	fixedMtime := time.Unix(1_700_000_100, 0)
	if err := os.Chtimes(path, fixedMtime, fixedMtime); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteManagedFragment(context.Background(), primary, host, nil); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.ModTime().Equal(fixedMtime) {
		t.Fatalf("idempotent write changed mtime from %v to %v", fixedMtime, unchanged.ModTime())
	}

	host.Name = "renamed-profile"
	if _, err := WriteManagedFragment(context.Background(), primary, host, nil); err != nil {
		t.Fatal(err)
	}
	parsed, err := ValidateManagedFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != host {
		t.Fatalf("updated managed host = %+v, want %+v", parsed, host)
	}
	if _, err := RemoveManagedFragment(context.Background(), primary, host.SSHAlias, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed fragment still exists: %v", err)
	}
}

func TestUnixManagedFragmentRemovalRenamesOutBeforeDelete(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	path, err := WriteManagedFragment(context.Background(), primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}

	var observedBackup string
	managedUnixAfterRenameOut = func(directory, backupPath, targetPath string) {
		observedBackup = backupPath
		if directory != filepath.Dir(path) || targetPath != path {
			t.Fatalf("rename-out hook paths = %q, %q, %q", directory, backupPath, targetPath)
		}
		backupName := filepath.Base(backupPath)
		if !strings.HasPrefix(backupName, ".dev-fleet-fragment-backup-") || !strings.HasSuffix(backupName, ".tmp") {
			t.Fatalf("rename-out backup name = %q", backupName)
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("canonical fragment exists after rename-out: %v", err)
		}
		info, err := os.Lstat(backupPath)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != managedFragmentFileMode {
			t.Fatalf("rename-out backup mode = %v", info.Mode())
		}
		cfg, err := LoadConfig(primary)
		if err != nil {
			t.Fatalf("LoadConfig during rename-out: %v", err)
		}
		if len(cfg.Hosts) != 0 {
			t.Fatalf("LoadConfig loaded ignored rename-out backup: %+v", cfg.Hosts)
		}
		if _, err := ValidateManagedFragment(backupPath); !errors.Is(err, ErrManagedFragmentConflict) {
			t.Fatalf("rename-out backup validated as a managed fragment: %v", err)
		}
	}
	t.Cleanup(func() { managedUnixAfterRenameOut = nil })

	if _, err := RemoveManagedFragment(context.Background(), primary, host.SSHAlias, nil); err != nil {
		t.Fatal(err)
	}
	managedUnixAfterRenameOut = nil
	if observedBackup == "" {
		t.Fatal("removal did not reach rename-out hook")
	}
	for _, removedPath := range []string{path, observedBackup} {
		if _, err := os.Lstat(removedPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("removed path %s still exists: %v", removedPath, err)
		}
	}
}

func TestLoadConfigRejectsInsecureManagedModes(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		dir  bool
	}{
		{name: "file", mode: 0o644},
		{name: "directory", mode: 0o755, dir: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			path := writeManagedFixture(t, primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
			insecurePath := path
			if test.dir {
				insecurePath = filepath.Dir(path)
			}
			if err := os.Chmod(insecurePath, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(primary); err == nil || !strings.Contains(err.Error(), "mode") {
				t.Fatalf("LoadConfig error = %v, want private mode rejection", err)
			}
		})
	}
}

func TestLoadConfigRejectsManagedSymlink(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	path := writeManagedFixture(t, primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
	target := filepath.Join(t.TempDir(), "target.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(primary); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadConfig error = %v, want symlink rejection", err)
	}
}

func TestUnixManagedFragmentConcurrentWritersConflict(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	initial := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	if _, err := WriteManagedFragment(context.Background(), primary, initial, nil); err != nil {
		t.Fatal(err)
	}

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	managedFragmentBeforeLock = func(string) {
		arrived <- struct{}{}
		<-release
	}
	defer func() { managedFragmentBeforeLock = nil }()

	desired := []ManagedHost{
		{Name: "writer-one", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX},
		{Name: "writer-two", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX},
	}
	results := make([]error, len(desired))
	var writers sync.WaitGroup
	for index := range desired {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			_, results[index] = WriteManagedFragment(context.Background(), primary, desired[index], nil)
		}(index)
	}
	<-arrived
	<-arrived
	close(release)
	writers.Wait()
	managedFragmentBeforeLock = nil

	succeeded, conflicted := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrManagedFragmentConflict):
			conflicted++
		default:
			t.Fatalf("concurrent write returned %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent writes succeeded/conflicted = %d/%d, want 1/1; errors=%v", succeeded, conflicted, results)
	}
	path, _ := ManagedFragmentPath(primary, "lab")
	installed, err := ValidateManagedFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if installed != desired[0] && installed != desired[1] {
		t.Fatalf("installed concurrent result = %+v", installed)
	}
}

func TestUnixManagedFragmentRejectsParentSwap(t *testing.T) {
	for _, operation := range []string{"replace", "remove"} {
		t.Run(operation, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			initial := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
			path, err := WriteManagedFragment(context.Background(), primary, initial, nil)
			if err != nil {
				t.Fatal(err)
			}
			initialBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			victimBytes, err := RenderManagedFragment(ManagedHost{Name: "victim", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Dir(path)
			movedDirectory := directory + ".moved"
			defer func() { managedUnixBeforePublish = nil }()
			managedUnixBeforePublish = func(_, _, targetPath string) {
				if err := os.Rename(directory, movedDirectory); err != nil {
					t.Errorf("swap managed directory: %v", err)
					return
				}
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Errorf("create replacement directory: %v", err)
					return
				}
				if err := os.Chmod(directory, 0o700); err != nil {
					t.Errorf("protect replacement directory: %v", err)
					return
				}
				writeManagedTestBytes(t, filepath.Join(directory, filepath.Base(targetPath)), victimBytes)
			}
			var mutationErr error
			if operation == "replace" {
				_, mutationErr = WriteManagedFragment(context.Background(), primary, ManagedHost{Name: "desired", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}, nil)
			} else {
				_, mutationErr = RemoveManagedFragment(context.Background(), primary, "lab", nil)
			}
			managedUnixBeforePublish = nil
			if mutationErr == nil || !errors.Is(mutationErr, ErrManagedFragmentConflict) {
				t.Fatalf("%s after parent swap = %v", operation, mutationErr)
			}
			movedBytes, err := os.ReadFile(filepath.Join(movedDirectory, filepath.Base(path)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(movedBytes, initialBytes) {
				t.Fatalf("%s changed the fragment in the held original directory", operation)
			}
			replacementBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(replacementBytes, victimBytes) {
				t.Fatalf("%s mutated the swapped-in directory", operation)
			}
		})
	}
}

func TestUnixManagedFragmentRejectsTargetSwapAtPublication(t *testing.T) {
	for _, operation := range []string{"replace", "remove"} {
		t.Run(operation, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			initial := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
			path, err := WriteManagedFragment(context.Background(), primary, initial, nil)
			if err != nil {
				t.Fatal(err)
			}
			concurrent := ManagedHost{Name: "concurrent", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
			concurrentBytes, err := RenderManagedFragment(concurrent)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { managedUnixBeforePublish = nil }()
			managedUnixBeforePublish = func(_, _, targetPath string) {
				temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".concurrent-*.tmp")
				if err != nil {
					t.Errorf("create concurrent target: %v", err)
					return
				}
				temporaryPath := temporary.Name()
				if err := temporary.Chmod(0o600); err != nil {
					_ = temporary.Close()
					t.Errorf("protect concurrent target: %v", err)
					return
				}
				if _, err := temporary.Write(concurrentBytes); err != nil {
					_ = temporary.Close()
					t.Errorf("write concurrent target: %v", err)
					return
				}
				if err := temporary.Close(); err != nil {
					t.Errorf("close concurrent target: %v", err)
					return
				}
				if err := os.Rename(temporaryPath, targetPath); err != nil {
					t.Errorf("publish concurrent target: %v", err)
				}
			}
			var mutationErr error
			if operation == "replace" {
				_, mutationErr = WriteManagedFragment(context.Background(), primary, ManagedHost{Name: "desired", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}, nil)
			} else {
				_, mutationErr = RemoveManagedFragment(context.Background(), primary, "lab", nil)
			}
			managedUnixBeforePublish = nil
			if mutationErr == nil || !errors.Is(mutationErr, ErrManagedFragmentConflict) {
				t.Fatalf("%s after target swap = %v", operation, mutationErr)
			}
			installed, err := ValidateManagedFragment(path)
			if err != nil {
				t.Fatal(err)
			}
			if installed != concurrent {
				t.Fatalf("%s overwrote concurrent target with %+v", operation, installed)
			}
		})
	}
}

func TestUnixManagedFragmentCreateRejectsParentAndTargetSwap(t *testing.T) {
	host := ManagedHost{Name: "desired", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	t.Run("target", func(t *testing.T) {
		primary := filepath.Join(t.TempDir(), "remotes.toml")
		path, err := ManagedFragmentPath(primary, host.SSHAlias)
		if err != nil {
			t.Fatal(err)
		}
		concurrent := ManagedHost{Name: "concurrent", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
		concurrentBytes, err := RenderManagedFragment(concurrent)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { managedUnixBeforePublish = nil }()
		managedUnixBeforePublish = func(_, _, targetPath string) {
			writeManagedTestBytes(t, targetPath, concurrentBytes)
		}
		_, writeErr := WriteManagedFragment(context.Background(), primary, host, nil)
		managedUnixBeforePublish = nil
		if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
			t.Fatalf("create after target appearance = %v", writeErr)
		}
		installed, err := ValidateManagedFragment(path)
		if err != nil {
			t.Fatal(err)
		}
		if installed != concurrent {
			t.Fatalf("create overwrote concurrent target with %+v", installed)
		}
	})

	t.Run("parent", func(t *testing.T) {
		primary := filepath.Join(t.TempDir(), "remotes.toml")
		path, err := ManagedFragmentPath(primary, host.SSHAlias)
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Dir(path)
		movedDirectory := directory + ".moved"
		victim := ManagedHost{Name: "victim", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
		victimBytes, err := RenderManagedFragment(victim)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { managedUnixBeforePublish = nil }()
		managedUnixBeforePublish = func(_, _, targetPath string) {
			if err := os.Rename(directory, movedDirectory); err != nil {
				t.Fatalf("swap managed directory: %v", err)
			}
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatalf("create replacement directory: %v", err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatalf("protect replacement directory: %v", err)
			}
			writeManagedTestBytes(t, filepath.Join(directory, filepath.Base(targetPath)), victimBytes)
		}
		_, writeErr := WriteManagedFragment(context.Background(), primary, host, nil)
		managedUnixBeforePublish = nil
		if writeErr == nil || !errors.Is(writeErr, ErrManagedFragmentConflict) {
			t.Fatalf("create after parent swap = %v", writeErr)
		}
		if _, err := os.Lstat(filepath.Join(movedDirectory, filepath.Base(path))); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("rolled-back create remains in original directory: %v", err)
		}
		installed, err := ValidateManagedFragment(path)
		if err != nil {
			t.Fatal(err)
		}
		if installed != victim {
			t.Fatalf("create mutated swapped-in directory with %+v", installed)
		}
	})
}

func TestUnixManagedFragmentAdvisoryLockCoordinatesProcesses(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "remotes.toml")
	if _, err := WriteManagedFragment(context.Background(), primary, ManagedHost{
		Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX,
	}, nil); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	heldMarker := filepath.Join(root, "held")
	releaseMarker := filepath.Join(root, "release")
	snapshotMarker := filepath.Join(root, "snapshot")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	first := exec.CommandContext(ctx, executable, "managed-fragment-helper")
	first.Env = append(os.Environ(),
		"DEV_FLEET_MANAGED_TEST_HELPER=1",
		"DEV_FLEET_MANAGED_TEST_PRIMARY="+primary,
		"DEV_FLEET_MANAGED_TEST_NAME=writer-one",
		"DEV_FLEET_MANAGED_TEST_HELD_MARKER="+heldMarker,
		"DEV_FLEET_MANAGED_TEST_RELEASE_MARKER="+releaseMarker,
	)
	var firstStderr bytes.Buffer
	first.Stderr = &firstStderr
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	waitForManagedTestMarker(t, ctx, heldMarker)

	second := exec.CommandContext(ctx, executable, "managed-fragment-helper")
	second.Env = append(os.Environ(),
		"DEV_FLEET_MANAGED_TEST_HELPER=1",
		"DEV_FLEET_MANAGED_TEST_PRIMARY="+primary,
		"DEV_FLEET_MANAGED_TEST_NAME=writer-two",
		"DEV_FLEET_MANAGED_TEST_SNAPSHOT_MARKER="+snapshotMarker,
	)
	var secondStderr bytes.Buffer
	second.Stderr = &secondStderr
	if err := second.Start(); err != nil {
		_ = first.Process.Kill()
		t.Fatal(err)
	}
	waitForManagedTestMarker(t, ctx, snapshotMarker)
	if err := os.WriteFile(releaseMarker, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := first.Wait(); err != nil {
		t.Fatalf("first managed writer: %v: %s", err, firstStderr.Bytes())
	}
	secondErr := second.Wait()
	var exitErr *exec.ExitError
	if !errors.As(secondErr, &exitErr) || exitErr.ExitCode() != managedFragmentConflictExitCode {
		t.Fatalf("second managed writer = %v: %s", secondErr, secondStderr.Bytes())
	}
	path, _ := ManagedFragmentPath(primary, "lab")
	installed, err := ValidateManagedFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "writer-one" {
		t.Fatalf("cross-process loser overwrote winner: %+v", installed)
	}
}

func waitForManagedTestMarker(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", filepath.Base(path), ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func writeManagedTestBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

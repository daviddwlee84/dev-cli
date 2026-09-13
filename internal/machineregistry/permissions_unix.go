//go:build unix

package machineregistry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/daviddwlee84/dev-cli/internal/platformfs"
	"golang.org/x/sys/unix"
)

func pathDiagnostic(path, reason string, info fs.FileInfo, want fs.FileMode, ancestor bool) *PathError {
	owner := "unknown"
	mine := false
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		owner = strconv.FormatUint(uint64(stat.Uid), 10)
		mine = int(stat.Uid) == os.Geteuid()
	}
	expected := fmt.Sprintf("uid %d", os.Geteuid())
	if ancestor {
		expected += " or uid 0; no group/other write"
	}
	return &PathError{Path: path, Reason: reason, Owner: owner, ExpectedOwner: expected, Mode: info.Mode(), ExpectedMode: want, Repairable: mine && info.Mode()&os.ModeSymlink == 0 && (info.IsDir() || info.Mode().IsRegular()) && want != 0 && want.Perm()&^info.Mode().Perm() == 0}
}

func planPermissions(path string) (PermissionPlan, error) {
	plan := PermissionPlan{state: &permissionState{path: path}}
	anchor, err := platformfs.Resolve(path)
	if err != nil {
		return plan, err
	}
	plan.state.anchor = anchor
	add := func(p string, info fs.FileInfo, want fs.FileMode, ancestor bool) {
		var err error
		if ancestor {
			err = checkAncestor(p, info)
		} else {
			err = checkPrivate(p, info, want)
		}
		entry := permissionEntry{path: p, info: info}
		if err != nil {
			var diagnostic *PathError
			if !errors.As(err, &diagnostic) {
				diagnostic = pathDiagnostic(p, err.Error(), info, want, ancestor)
			}
			if diagnostic.Repairable {
				entry.after = diagnostic.ExpectedMode
				change := PermissionChange{Path: p, BeforeMode: info.Mode(), AfterMode: entry.after}
				plan.Changes = append(plan.Changes, change)
				plan.state.changes = append(plan.state.changes, change)
			} else {
				plan.Diagnostics = append(plan.Diagnostics, diagnostic)
			}
		}
		plan.state.entries = append(plan.state.entries, entry)
	}
	root := anchor.Path
	current := root
	dir := filepath.Dir(path)
	for _, part := range strings.Split(strings.TrimPrefix(dir, root), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return plan, err
		}
		if runtime.GOOS == "darwin" && (current == "/var" || current == "/tmp") && info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(current)
			if e == nil && (target == "private"+current || target == "/private"+current) {
				continue
			}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			plan.Diagnostics = append(plan.Diagnostics, pathDiagnostic(current, "parent must be a real directory", info, 0, false))
			break
		}
		want := info.Mode().Perm() &^ 0o022
		if current == dir {
			want = 0o700
		}
		add(current, info, want, current != dir)
	}
	if len(plan.Diagnostics) == 0 {
		for _, p := range []string{path, filepath.Join(dir, ".registry.lock"), path + "-journal", path + "-wal", path + "-shm"} {
			info, err := os.Lstat(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return plan, err
			}
			if !info.Mode().IsRegular() {
				plan.Diagnostics = append(plan.Diagnostics, pathDiagnostic(p, "must be a regular unlinked file", info, 0, false))
				continue
			}
			add(p, info, 0o600, false)
		}
	}
	plan.state.blocked = len(plan.Diagnostics) > 0
	return plan, anchor.Verify()
}

func permissionMetadataEqual(a, b fs.FileInfo) bool {
	if !os.SameFile(a, b) || a.Mode() != b.Mode() {
		return false
	}
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	// Directory link counts change when unrelated child directories appear or
	// disappear (including under shared temporary ancestors). They do not prove
	// a changed directory identity; regular-file link counts still guard aliases.
	return xok && yok && x.Uid == y.Uid && x.Gid == y.Gid && (a.IsDir() || x.Nlink == y.Nlink)
}
func verifyPermissionEntries(entries []permissionEntry) error {
	for _, entry := range entries {
		now, err := os.Lstat(entry.path)
		if err != nil || !permissionMetadataEqual(entry.info, now) {
			return errors.Join(ErrStale, err)
		}
	}
	return nil
}
func applyPermissions(ctx context.Context, state *permissionState) (PermissionResult, error) {
	out := PermissionResult{Status: "unchanged"}
	entries := append([]permissionEntry(nil), state.entries...)
	verify := func() error {
		if err := state.anchor.Verify(); err != nil {
			return errors.Join(ErrStale, err)
		}
		return verifyPermissionEntries(entries)
	}
	if err := verify(); err != nil {
		return out, err
	}
	for index, entry := range entries {
		if entry.after == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := verify(); err != nil {
			return out, err
		}
		fd, err := unix.Open(entry.path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return out, err
		}
		file := os.NewFile(uintptr(fd), entry.path)
		held, err := file.Stat()
		if err != nil || !permissionMetadataEqual(entry.info, held) {
			file.Close()
			return out, errors.Join(ErrStale, err)
		}
		if err = verify(); err != nil {
			file.Close()
			return out, err
		}
		err = file.Chmod(entry.after)
		if err == nil {
			entries[index].info, err = file.Stat()
		}
		closeErr := file.Close()
		if err = errors.Join(err, closeErr); err != nil {
			out.Status = "partial"
			return out, err
		}
		out.Status = "partial"
		out.Outcomes = append(out.Outcomes, PermissionOutcome{Path: entry.path, Status: "tightened"})
	}
	if err := verify(); err != nil {
		return out, err
	}
	out.Status = "complete"
	return out, nil
}

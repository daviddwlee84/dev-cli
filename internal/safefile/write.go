package safefile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var (
	// ErrReplaceTarget reports a replacement target that is absent, unsafe, or
	// no longer matches the state supplied by the caller.
	ErrReplaceTarget = errors.New("replacement target is not unchanged regular file")
)

// OwnerPrivateMode returns 0600, or 0700 when executable content was explicitly
// authorized.
func OwnerPrivateMode(executable bool) fs.FileMode {
	if executable {
		return 0o700
	}
	return 0o600
}

// CreateNoClobber stages data in an unguessable owner-private file, syncs it,
// applies finalMode, and atomically hard-links it to the absent destination.
// The hard-link publication is same-directory and cannot replace an existing
// path. This generic helper exists so callers such as repotemplate can preserve
// intentional public modes; secret-bearing callers should use
// CreatePrivateNoClobber.
func CreateNoClobber(ctx context.Context, root *os.Root, name string, data []byte, finalMode fs.FileMode) (fs.FileInfo, error) {
	return createNoClobberWithHooks(ctx, root, name, data, finalMode, createNoClobberHooks{})
}

type createNoClobberHooks struct {
	removeStage func(*os.Root, string) error
	syncRoot    func(*os.Root) error
}

func createNoClobberWithHooks(ctx context.Context, root *os.Root, name string, data []byte, finalMode fs.FileMode, hooks createNoClobberHooks) (fs.FileInfo, error) {
	if root == nil {
		return nil, errors.New("atomic create: nil root")
	}
	if err := validateTargetName(name); err != nil {
		return nil, err
	}
	removeStage := hooks.removeStage
	if removeStage == nil {
		removeStage = func(root *os.Root, name string) error { return root.Remove(name) }
	}
	syncDirectory := hooks.syncRoot
	if syncDirectory == nil {
		syncDirectory = syncRoot
	}
	stagedName, staged, err := stageRegular(ctx, root, data, finalMode)
	if err != nil {
		return nil, err
	}
	defer func() {
		if stagedName != "" {
			_ = root.Remove(stagedName)
		}
	}()
	if err := root.Link(stagedName, name); err != nil {
		return nil, fmt.Errorf("publish %q without replacement: %w", name, err)
	}
	rollbackPublication := func(cause error) error {
		current, statErr := root.Lstat(name)
		if statErr != nil || unsafeLink(current) || !current.Mode().IsRegular() || !SameFileState(staged, current) {
			return fmt.Errorf("create %q failed after publication; destination ownership is indeterminate: %w",
				name, errors.Join(cause, statErr, ErrChanged))
		}
		removeErr := root.Remove(name)
		var syncErr error
		if removeErr == nil {
			syncErr = syncDirectory(root)
		}
		if removeErr != nil {
			return fmt.Errorf("create %q failed and publication rollback failed: %w", name, errors.Join(cause, removeErr))
		}
		return fmt.Errorf("create %q failed after publication and was rolled back: %w", name, errors.Join(cause, syncErr))
	}
	published, err := root.Lstat(name)
	if err != nil {
		return nil, rollbackPublication(fmt.Errorf("verify published file: %w", err))
	}
	if unsafeLink(published) || !published.Mode().IsRegular() || !SameFileState(staged, published) {
		return nil, rollbackPublication(fmt.Errorf("published file does not match staging file: %w", ErrChanged))
	}
	if err := removeStage(root, stagedName); err != nil {
		return nil, rollbackPublication(fmt.Errorf("remove staging link: %w", err))
	}
	stagedName = ""
	published, err = root.Lstat(name)
	if err != nil {
		return nil, rollbackPublication(fmt.Errorf("refresh published file: %w", err))
	}
	if err := syncDirectory(root); err != nil {
		return nil, rollbackPublication(fmt.Errorf("sync destination directory: %w", err))
	}
	return published, nil
}

// CreatePrivateNoClobber is CreateNoClobber with a clamped owner-only final
// mode. executable is the only way to grant execute permission.
func CreatePrivateNoClobber(ctx context.Context, root *os.Root, name string, data []byte, executable bool) (fs.FileInfo, error) {
	if int64(len(data)) > CompiledMaxFileBytes {
		return nil, fmt.Errorf("file %q is %d bytes; maximum is %d: %w", name, len(data), CompiledMaxFileBytes, ErrFileLimit)
	}
	return CreateNoClobber(ctx, root, name, data, OwnerPrivateMode(executable))
}

// AtomicReplace stages and syncs data in the target directory, revalidates the
// existing regular file against observed, and atomically renames the staged file
// over it. Atomic describes publication, not a filesystem compare-and-swap: the
// metadata check and rename are separate syscalls. Callers performing digest-CAS
// must hold an exclusive cooperative operation lease, obtain observed from a
// fresh stable read/hash, and retain any rollback copy before invoking this
// primitive. It never follows a symlink or reparse point.
func AtomicReplace(ctx context.Context, root *os.Root, name string, observed fs.FileInfo, data []byte, finalMode fs.FileMode) (fs.FileInfo, error) {
	if root == nil {
		return nil, errors.New("atomic replace: nil root")
	}
	if observed == nil {
		return nil, fmt.Errorf("replace %q without observed state: %w", name, ErrReplaceTarget)
	}
	if err := validateTargetName(name); err != nil {
		return nil, err
	}
	if err := verifyReplaceTarget(root, name, observed); err != nil {
		return nil, err
	}
	stagedName, staged, err := stageRegular(ctx, root, data, finalMode)
	if err != nil {
		return nil, err
	}
	defer func() {
		if stagedName != "" {
			_ = root.Remove(stagedName)
		}
	}()
	if err := verifyReplaceTarget(root, name, observed); err != nil {
		return nil, err
	}
	if err := root.Rename(stagedName, name); err != nil {
		return nil, fmt.Errorf("atomically replace %q: %w", name, err)
	}
	stagedName = ""
	published, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("verify replacement %q: %w", name, err)
	}
	if unsafeLink(published) || !published.Mode().IsRegular() || !SameFileState(staged, published) {
		return nil, fmt.Errorf("replacement %q does not match staging file: %w", name, ErrChanged)
	}
	if err := syncRoot(root); err != nil {
		return nil, fmt.Errorf("sync destination directory after replacing %q: %w", name, err)
	}
	return published, nil
}

// AtomicReplacePrivate is AtomicReplace with owner-only final permissions.
func AtomicReplacePrivate(ctx context.Context, root *os.Root, name string, observed fs.FileInfo, data []byte, executable bool) (fs.FileInfo, error) {
	if int64(len(data)) > CompiledMaxFileBytes {
		return nil, fmt.Errorf("file %q is %d bytes; maximum is %d: %w", name, len(data), CompiledMaxFileBytes, ErrFileLimit)
	}
	return AtomicReplace(ctx, root, name, observed, data, OwnerPrivateMode(executable))
}

func verifyReplaceTarget(root *os.Root, name string, observed fs.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect replacement target %q: %w: %w", name, err, ErrReplaceTarget)
	}
	if unsafeLink(current) || !current.Mode().IsRegular() || !sameStableSourceState(observed, current) {
		return fmt.Errorf("replacement target %q changed or is not a regular file: %w", name, ErrReplaceTarget)
	}
	return nil
}

func stageRegular(ctx context.Context, root *os.Root, data []byte, finalMode fs.FileMode) (string, fs.FileInfo, error) {
	if finalMode&^fs.ModePerm != 0 {
		return "", nil, fmt.Errorf("unsupported final file mode %s", finalMode)
	}
	var name string
	var writer *os.File
	for attempt := 0; attempt < 32; attempt++ {
		candidate, err := randomStageName()
		if err != nil {
			return "", nil, err
		}
		writer, err = root.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			name = candidate
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("create private staging file: %w", err)
		}
	}
	if writer == nil {
		return "", nil, errors.New("create private staging file: too many name collisions")
	}
	keep := true
	defer func() {
		if keep {
			_ = writer.Close()
			_ = root.Remove(name)
		}
	}()
	if err := writeAllContext(ctx, writer, data); err != nil {
		return "", nil, err
	}
	if err := writer.Chmod(finalMode.Perm()); err != nil {
		return "", nil, fmt.Errorf("set staged file mode: %w", err)
	}
	if err := writer.Sync(); err != nil {
		return "", nil, fmt.Errorf("sync staged file: %w", err)
	}
	opened, err := writer.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("inspect staged file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", nil, fmt.Errorf("close staged file: %w", err)
	}
	keep = false
	current, err := root.Lstat(name)
	if err != nil {
		_ = root.Remove(name)
		return "", nil, fmt.Errorf("reopen staged file name: %w", err)
	}
	if unsafeLink(current) || !current.Mode().IsRegular() || !SameFileState(opened, current) {
		_ = root.Remove(name)
		return "", nil, fmt.Errorf("staging file changed before publication: %w", ErrChanged)
	}
	return name, current, nil
}

func validateTargetName(name string) error {
	if err := pathx.ValidatePortableComponent(name, CompiledMaxComponentBytes); err != nil {
		return fmt.Errorf("invalid atomic file name %q: %w", name, err)
	}
	return nil
}

func writeAllContext(ctx context.Context, writer io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return context.Cause(ctx)
}

func randomStageName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate staging file name: %w", err)
	}
	return ".dev-safefile-" + hex.EncodeToString(random[:]) + ".tmp", nil
}

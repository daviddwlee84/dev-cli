// Package safefile provides held-root filesystem operations for untrusted
// relative paths. It keeps traversal relative to os.Root handles, rejects links
// and special files, revalidates filesystem identity, and detects observable
// source mutation while reading.
package safefile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var (
	// ErrChanged reports that a named root, child, or file no longer identifies
	// the object observed by the caller.
	ErrChanged = errors.New("filesystem object changed")
	// ErrUnsafeType reports a symlink, reparse point, directory, or other
	// non-regular object where a regular file was required.
	ErrUnsafeType = errors.New("unsafe filesystem object type")
	// ErrFileLimit reports a regular file whose bytes exceed the read policy.
	ErrFileLimit = errors.New("file size limit exceeded")
)

// OpenRoot opens path as a held os.Root and proves that the pre-open and
// post-open names identify the held directory. Symlinks and reparse points are
// rejected rather than followed.
func OpenRoot(path string) (*os.Root, fs.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if unsafeLink(before) {
		return nil, nil, fmt.Errorf("root is a symlink or reparse point: %w", ErrUnsafeType)
	}
	if !before.IsDir() {
		return nil, nil, fmt.Errorf("root is not a directory: %w", ErrUnsafeType)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	held, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || unsafeLink(held) || unsafeLink(current) || !current.IsDir() ||
		!os.SameFile(before, held) || !os.SameFile(current, held) {
		_ = root.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("root changed while opening: %w", ErrChanged)
	}
	return root, held, nil
}

// OpenChildRoot opens one child directory through parent and proves that its
// name still identifies the held directory.
func OpenChildRoot(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	if parent == nil {
		return nil, nil, errors.New("open child root: nil parent")
	}
	if err := pathx.ValidatePortableComponent(name, CompiledMaxComponentBytes); err != nil {
		return nil, nil, err
	}
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if unsafeLink(before) {
		return nil, nil, fmt.Errorf("component %q is a symlink or reparse point: %w", name, ErrUnsafeType)
	}
	if !before.IsDir() {
		return nil, nil, fmt.Errorf("component %q is not a directory: %w", name, ErrUnsafeType)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	held, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	current, err := parent.Lstat(name)
	if err != nil || unsafeLink(held) || unsafeLink(current) || !current.IsDir() ||
		!os.SameFile(before, held) || !os.SameFile(current, held) {
		_ = child.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("component %q changed while opening: %w", name, ErrChanged)
	}
	return child, held, nil
}

// VerifyRoot proves that path still names the previously held root directory.
func VerifyRoot(path string, expected fs.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if expected == nil || unsafeLink(current) || !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("root path changed: %w", ErrChanged)
	}
	return nil
}

// VerifyChildRoot proves that name below parent still names the previously held
// child directory.
func VerifyChildRoot(parent *os.Root, name string, expected fs.FileInfo) error {
	if parent == nil {
		return errors.New("verify child root: nil parent")
	}
	if err := pathx.ValidatePortableComponent(name, CompiledMaxComponentBytes); err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if expected == nil || unsafeLink(current) || !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("component %q changed: %w", name, ErrChanged)
	}
	return nil
}

// ReadStableRegular reads one child file while proving that the directory name,
// opened descriptor, and final directory name all identify the expected regular
// file with unchanged observable metadata. maxBytes must be non-negative; zero
// means unbounded. Supplying the FileInfo from an earlier directory listing
// detects replacement or mutation between selection and opening.
func ReadStableRegular(ctx context.Context, root *os.Root, name string, expected fs.FileInfo, maxBytes int64) ([]byte, fs.FileInfo, error) {
	if root == nil {
		return nil, nil, errors.New("read stable file: nil root")
	}
	if maxBytes < 0 {
		return nil, nil, fmt.Errorf("negative maximum file size: %w", ErrFileLimit)
	}
	if err := pathx.ValidatePortableComponent(name, CompiledMaxComponentBytes); err != nil {
		return nil, nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if expected == nil {
		expected = before
	}
	if unsafeLink(before) || !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("source is not a regular file: %w", ErrUnsafeType)
	}
	if !sameStableSourceState(expected, before) {
		return nil, nil, fmt.Errorf("source changed before opening file: %w", ErrChanged)
	}
	if maxBytes > 0 && before.Size() > maxBytes {
		return nil, nil, fmt.Errorf("source is %d bytes; maximum is %d: %w", before.Size(), maxBytes, ErrFileLimit)
	}

	file, err := openRegularForRead(root, name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if unsafeLink(opened) || !opened.Mode().IsRegular() || !sameStableSourceState(expected, opened) {
		return nil, nil, fmt.Errorf("source changed while opening file: %w", ErrChanged)
	}
	current, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if unsafeLink(current) || !sameStableSourceState(opened, current) {
		return nil, nil, fmt.Errorf("source changed while opening file: %w", ErrChanged)
	}

	data, err := readAllContext(ctx, file, maxBytes)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	current, err = root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if unsafeLink(after) || unsafeLink(current) || !sameStableSourceState(opened, after) ||
		!sameStableSourceState(after, current) || after.Size() != int64(len(data)) {
		return nil, nil, fmt.Errorf("source changed during read: %w", ErrChanged)
	}
	return data, opened, nil
}

// SameFileState compares identity, mode, size, and modification time. Stable
// reads and observed-target replacement additionally compare platform change
// time where the operating system exposes it.
func SameFileState(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func sameStableSourceState(left, right fs.FileInfo) bool {
	return SameFileState(left, right) && samePlatformState(left, right)
}

func readAllContext(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, error) {
	var data []byte
	buffer := make([]byte, 64*1024)
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			if maxBytes > 0 && int64(read) > maxBytes-int64(len(data)) {
				return nil, fmt.Errorf("source exceeds %d bytes: %w", maxBytes, ErrFileLimit)
			}
			data = append(data, buffer[:read]...)
		}
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
		if read == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

func unsafeLink(info fs.FileInfo) bool {
	return info != nil && (info.Mode()&os.ModeSymlink != 0 || isReparsePoint(info))
}

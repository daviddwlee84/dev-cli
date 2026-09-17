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

// RemoveEmptyChildDir removes only the reviewed empty directory, never a file
// substituted at its name. Traversal and identity checks use held roots. Native
// directory-only deletion also rejects late content. External writers do not
// share our locks: Unix cannot atomically compare an inode and rmdir its name.
func RemoveEmptyChildDir(ctx context.Context, parent *os.Root, name string, expected fs.FileInfo) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := pathx.ValidatePortableComponent(name, CompiledMaxComponentBytes); err != nil {
		return err
	}
	if expected == nil || unsafeLink(expected) || !expected.IsDir() {
		return fmt.Errorf("remove empty directory needs a normal directory identity: %w", ErrUnsafeType)
	}
	child, info, err := OpenChildRoot(parent, name)
	if err != nil {
		return err
	}
	defer child.Close()
	if !os.SameFile(info, expected) {
		return ErrChanged
	}
	file, err := child.Open(".")
	if err != nil {
		return err
	}
	err = verifyEmptyDirectory(file)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return err
	}
	if err := VerifyChildRoot(parent, name, expected); err != nil {
		return err
	}
	return removeEmptyChildDirNative(ctx, parent, name, expected)
}

func verifyEmptyDirectory(file *os.File) error {
	names, err := file.Readdirnames(1)
	if len(names) != 0 {
		return errors.New("directory is not empty")
	}
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return io.ErrNoProgress
		}
		return err
	}
	return nil
}

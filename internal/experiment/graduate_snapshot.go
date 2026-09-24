package experiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// The product view excludes the root Git administration entry and directory
// mtimes, which our own init/commit may change. Regular file bytes, identities,
// modes and times remain bound; snapshots never follow repository symlinks.
func graduateTree(ctx context.Context, source string, products, skipREADME bool) (string, error) {
	root, _, err := safefile.OpenRoot(source)
	if err != nil {
		return "", err
	}
	defer root.Close()
	digest := sha256.New()
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if products && strings.EqualFold(name, ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if skipREADME && name == "README.md" {
			return nil
		}
		count++
		if count > 1000000 {
			return errors.New("Try has too many entries to bind a graduation review")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(digest, "%q link %q\n", name, target)
			return nil
		}
		identity, _, err := removalIdentity(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if info.IsDir() && products {
			fmt.Fprintf(digest, "%q directory %s %d\n", name, identity, info.Mode())
			return nil
		}
		fmt.Fprintf(digest, "%q %s %d %d %d\n", name, identity, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := openGraduateRegular(root, name)
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			_ = file.Close()
			return errors.New("Try file identity changed during graduation review")
		}
		content := sha256.New()
		read, readErr := io.CopyBuffer(content, &graduateContextReader{ctx: ctx, reader: io.LimitReader(file, info.Size()+1)}, make([]byte, 32*1024))
		after, statErr := file.Stat()
		closeErr := file.Close()
		if err := errors.Join(readErr, statErr, closeErr); err != nil {
			return err
		}
		currentIdentity, _, err := removalIdentity(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil || currentIdentity != identity || read != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return errors.New("Try file changed during graduation review")
		}
		fmt.Fprintf(digest, "content %x\n", content.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type graduateContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *graduateContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

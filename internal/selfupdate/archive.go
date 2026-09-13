package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractSource accepts the rootless git archive published by release.yml.
// Symlinks (e.g. CLAUDE.md) are omitted, as in Go module archives. No archive
// member can write outside the new private directory or replace another file.
func extractSource(archive []byte, dest string) error {
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var size int64
	const limit = 128 << 20
	for count := 0; ; count++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:`) {
			return fmt.Errorf("unsafe source archive path %q", header.Name)
		}
		top, _, _ := strings.Cut(name, "/")
		if strings.EqualFold(top, ".git") || strings.EqualFold(top, ".specstory") {
			return fmt.Errorf("source archive contains excluded directory %q", top)
		}
		if count >= 50000 || header.Size < 0 || header.Size > limit-size {
			return errors.New("source archive exceeds extraction limits")
		}
		size += header.Size
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// No links are materialized in the build tree.
		case tar.TypeXGlobalHeader:
			// git archive includes the commit ID in a global PAX comment.
			// tar.Reader consumes the metadata; it is not a filesystem entry.
		default:
			return fmt.Errorf("unsupported source archive entry %q", name)
		}
	}
	for _, required := range []string{"go.mod", "go.sum", "cmd/dev/main.go", "internal/skill/dev-cli/SKILL.md"} {
		info, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(required)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("source archive is missing regular file %s", required)
		}
	}
	return nil
}

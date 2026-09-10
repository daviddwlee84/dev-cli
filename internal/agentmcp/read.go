package agentmcp

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var errSymlinkLimit = errors.New("symlink limit reached")

func (s *Scanner) readSourceFromDisk(ctx context.Context, spec sourceSpec) readResult {
	if ctx.Err() != nil {
		return readResult{}
	}
	resolved, err := resolveBoundedSymlinks(spec.path, s.options.MaxSymlinkDepth)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return readResult{}
		}
		if errors.Is(err, errSymlinkLimit) {
			return readResult{code: DiagnosticSymlinkLimit}
		}
		return readResult{code: DiagnosticUnreadable}
	}
	if spec.projectRoot != "" {
		inside, err := pathx.Contains(spec.projectRoot, resolved)
		if err != nil || !inside {
			return readResult{code: DiagnosticProjectSymlinkEscape}
		}
	}

	file, err := os.Open(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return readResult{}
		}
		return readResult{code: DiagnosticUnreadable}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return readResult{code: DiagnosticUnreadable}
	}
	if !info.Mode().IsRegular() {
		return readResult{code: DiagnosticNotRegular}
	}
	if info.Size() > s.options.MaxFileBytes {
		return readResult{code: DiagnosticTooLarge}
	}

	var body bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return readResult{}
		}
		n, readErr := file.Read(chunk)
		if n > 0 {
			if int64(body.Len()+n) > s.options.MaxFileBytes {
				return readResult{code: DiagnosticTooLarge}
			}
			_, _ = body.Write(chunk[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, fs.ErrClosed) || !errors.Is(readErr, fs.ErrNotExist) && readErr.Error() != "EOF" {
				if readErr.Error() != "EOF" {
					return readResult{code: DiagnosticUnreadable}
				}
			}
			break
		}
	}
	if ctx.Err() != nil {
		return readResult{}
	}
	return readResult{data: body.Bytes(), present: true}
}

// resolveBoundedSymlinks follows symlinks in every path component while placing
// a small deterministic bound on cycles and unusually deep chains.
func resolveBoundedSymlinks(filename string, limit int) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}
	return resolveSymlinkPath(filepath.Clean(absolute), limit, 0)
}

func resolveSymlinkPath(absolute string, limit, followed int) (string, error) {
	volume := filepath.VolumeName(absolute)
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, root)
	components := strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' })
	current := root
	for index, component := range components {
		candidate := filepath.Join(current, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = candidate
			continue
		}
		if followed >= limit {
			return "", errSymlinkLimit
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(candidate), target)
		}
		if index+1 < len(components) {
			target = filepath.Join(target, filepath.Join(components[index+1:]...))
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return "", err
		}
		return resolveSymlinkPath(filepath.Clean(target), limit, followed+1)
	}
	return filepath.Clean(current), nil
}

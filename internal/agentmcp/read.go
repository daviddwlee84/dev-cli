package agentmcp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
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

	file, openedInfo, err := safefile.OpenRegular(resolved)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return readResult{}
		case errors.Is(err, safefile.ErrNotRegular):
			return readResult{code: DiagnosticNotRegular}
		default:
			return readResult{code: DiagnosticUnreadable}
		}
	}
	defer file.Close()
	if openedInfo.Size() > s.options.MaxFileBytes {
		return readResult{code: DiagnosticTooLarge}
	}
	if spec.projectRoot != "" {
		currentPath, err := resolveBoundedSymlinks(spec.path, s.options.MaxSymlinkDepth)
		if err != nil {
			return readResult{code: DiagnosticProjectSymlinkEscape}
		}
		inside, err := pathx.Contains(spec.projectRoot, currentPath)
		currentInfo, statErr := os.Stat(currentPath)
		if err != nil || !inside || statErr != nil || !os.SameFile(openedInfo, currentInfo) {
			return readResult{code: DiagnosticProjectSymlinkEscape}
		}
	}

	body, err := safefile.ReadAll(ctx, file, s.options.MaxFileBytes)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return readResult{}
		case errors.Is(err, safefile.ErrTooLarge):
			return readResult{code: DiagnosticTooLarge}
		default:
			return readResult{code: DiagnosticUnreadable}
		}
	}
	return readResult{data: body, present: true}
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
	components := strings.FieldsFunc(relative, func(r rune) bool { return r == rune(filepath.Separator) })
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

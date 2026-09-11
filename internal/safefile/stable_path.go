package safefile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ReadStablePath walks held directory handles and rejects links in every
// user-controlled component, then verifies the chain after reading. Only the
// fixed macOS /var and /tmp platform aliases may be resolved.
func ReadStablePath(ctx context.Context, path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("stable read requires a clean absolute path")
	}
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp"} {
			if strings.HasPrefix(path, alias+"/") {
				target, e := os.Readlink(alias)
				if e == nil && (target == "private"+alias || target == "/private"+alias) {
					path = "/private" + path
				}
			}
		}
	}
	base := filepath.VolumeName(path) + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, base), string(filepath.Separator))
	if len(parts) == 0 || len(parts) > 128 {
		return nil, ErrUnsafeType
	}
	root, info, err := OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	type edge struct {
		parent *os.Root
		name   string
		info   fs.FileInfo
	}
	var edges []edge
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, id, e := OpenChildRoot(current, part)
		if e != nil {
			return nil, e
		}
		defer next.Close()
		edges = append(edges, edge{current, part, id})
		current = next
	}
	data, _, err := ReadStableRegular(ctx, current, parts[len(parts)-1], nil, limit)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if err = VerifyChildRoot(e.parent, e.name, e.info); err != nil {
			return nil, err
		}
	}
	if err = VerifyRoot(base, info); err != nil {
		return nil, err
	}
	return data, nil
}

package sshremote

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// The root walk rejects every symlink component, while held os.Root handles
// prevent a changed path from redirecting reads or writes outside the walked
// tree. verify rechecks every named component before and after publication.
func openCacheRoot(path string, create bool) (*os.Root, func() error, error) {
	if err := cachePlatformSupported(); err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, nil, ErrUnsafeCache
	}
	volume := filepath.VolumeName(path)
	base := volume + string(filepath.Separator)
	root, rootInfo, err := safefile.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	if err := checkCacheAncestor(base, rootInfo); err != nil {
		root.Close()
		return nil, nil, err
	}
	type heldDirectory struct {
		path string
		info fs.FileInfo
	}
	held := []heldDirectory{{path: base, info: rootInfo}}
	components := strings.Split(strings.TrimPrefix(path, base), string(filepath.Separator))
	currentPath := base
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			root.Close()
			return nil, nil, ErrUnsafeCache
		}
		currentPath = filepath.Join(currentPath, component)
		info, err := root.Lstat(component)
		if errors.Is(err, fs.ErrNotExist) && create {
			err = root.Mkdir(component, 0o700)
			if err == nil {
				err = setCachePrivate(currentPath, 0o700)
			}
			if err == nil || errors.Is(err, fs.ErrExist) {
				info, err = root.Lstat(component)
			}
		}
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		if index == len(components)-1 {
			err = checkCacheDirectory(currentPath, info)
		} else {
			err = checkCacheAncestor(currentPath, info)
		}
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		child, childInfo, err := safefile.OpenChildRoot(root, component)
		root.Close()
		if err != nil {
			return nil, nil, err
		}
		root = child
		held = append(held, heldDirectory{path: currentPath, info: childInfo})
	}
	verify := func() error {
		for index, directory := range held {
			if err := safefile.VerifyRoot(directory.path, directory.info); err != nil {
				return err
			}
			info, err := os.Lstat(directory.path)
			if err != nil {
				return err
			}
			if index == len(held)-1 {
				err = checkCacheDirectory(directory.path, info)
			} else {
				err = checkCacheAncestor(directory.path, info)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		root.Close()
		return nil, nil, err
	}
	return root, verify, nil
}

func prepareCacheStage(file *os.File) error {
	return setCachePrivate(file.Name(), 0o600)
}

// Package pathx provides the path-safety checks shared by filesystem lifecycle
// operations. The checks are strict: a destination must be below its root, not
// merely share a string prefix with it, and symlinks are resolved before that
// decision is made.
package pathx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var (
	// ErrOutsideRoot reports a path that does not resolve below the requested root.
	ErrOutsideRoot = errors.New("path is outside root")
	// ErrRoot reports an operation that named the root itself rather than a child.
	ErrRoot = errors.New("path is the root itself")
	// ErrTraversal reports an explicit .. component in an unclean input path.
	ErrTraversal = errors.New("path contains parent traversal")
	// ErrInvalidComponent reports a value that is not one filesystem component.
	ErrInvalidComponent = errors.New("invalid path component")
)

// Canonical returns an absolute, clean path with symlinks resolved. If the final
// path does not exist yet, it resolves the nearest existing ancestor and then
// appends the missing suffix. This is the form needed to validate a destination
// before creating or moving anything into it.
func Canonical(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("canonical path: empty path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make %q absolute: %w", path, err)
	}
	absolute = filepath.Clean(absolute)

	probe := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			slices.Reverse(missing)
			parts := append([]string{resolved}, missing...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %q: %w", path, err)
		}
		// EvalSymlinks reports ErrNotExist both for a genuinely missing component
		// and for a symlink whose target is missing. Treating the latter as an
		// ordinary lexical suffix would make an escape look as though it remained
		// below its root. Resolve the link target explicitly before appending the
		// still-missing suffix.
		if info, lstatErr := os.Lstat(probe); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(probe)
			if readErr != nil {
				return "", fmt.Errorf("read unresolved symlink %q: %w", probe, readErr)
			}
			if hasParentTraversal(target) {
				return "", fmt.Errorf("unresolved symlink %q target %q: %w", probe, target, ErrTraversal)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(probe), target)
			}
			resolved, resolveErr := Canonical(target)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve target of %q: %w", probe, resolveErr)
			}
			slices.Reverse(missing)
			parts := append([]string{resolved}, missing...)
			return filepath.Clean(filepath.Join(parts...)), nil
		} else if lstatErr != nil && !errors.Is(lstatErr, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect unresolved path %q: %w", probe, lstatErr)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("resolve %q: no existing ancestor: %w", path, err)
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

// CanonicalChild returns candidate in canonical form when it resolves strictly
// below root. It rejects root itself, lexical .. traversal, sibling-prefix
// confusion, and paths that escape through a symlink.
func CanonicalChild(root, candidate string) (string, error) {
	if hasParentTraversal(candidate) {
		return "", fmt.Errorf("%q: %w", candidate, ErrTraversal)
	}
	canonicalRoot, err := Canonical(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize root: %w", err)
	}
	canonicalCandidate, err := Canonical(candidate)
	if err != nil {
		return "", fmt.Errorf("canonicalize candidate: %w", err)
	}

	rel, err := filepath.Rel(canonicalRoot, canonicalCandidate)
	if err != nil {
		return "", fmt.Errorf("compare %q with root %q: %w", candidate, root, err)
	}
	switch {
	case rel == ".":
		return "", fmt.Errorf("%q: %w", candidate, ErrRoot)
	case filepath.IsAbs(rel), rel == "..", strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		return "", fmt.Errorf("%q is not below %q: %w", candidate, root, ErrOutsideRoot)
	default:
		return canonicalCandidate, nil
	}
}

// IsChild reports whether candidate resolves strictly below root. Policy
// rejections are returned as false without hiding filesystem errors.
func IsChild(root, candidate string) (bool, error) {
	_, err := CanonicalChild(root, candidate)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrOutsideRoot) || errors.Is(err, ErrRoot) || errors.Is(err, ErrTraversal) {
		return false, nil
	}
	return false, err
}

// Contains reports whether candidate is root itself or resolves below it.
func Contains(root, candidate string) (bool, error) {
	canonicalRoot, err := Canonical(root)
	if err != nil {
		return false, err
	}
	canonicalCandidate, err := Canonical(candidate)
	if err != nil {
		return false, err
	}
	if canonicalRoot == canonicalCandidate {
		return true, nil
	}
	return IsChild(canonicalRoot, canonicalCandidate)
}

// ValidateComponent requires a non-empty relative value containing exactly one
// path component. Both slash styles are rejected so state synced between Unix
// and Windows cannot acquire a different meaning on another host.
func ValidateComponent(component string) error {
	switch {
	case strings.TrimSpace(component) == "":
		return fmt.Errorf("empty component: %w", ErrInvalidComponent)
	case strings.ContainsRune(component, '\x00'):
		return fmt.Errorf("component contains NUL: %w", ErrInvalidComponent)
	case component == "." || component == "..":
		return fmt.Errorf("component %q is reserved: %w", component, ErrInvalidComponent)
	case filepath.IsAbs(component) || filepath.VolumeName(component) != "" || driveQualified(component):
		return fmt.Errorf("component %q is absolute or drive-qualified: %w", component, ErrInvalidComponent)
	case strings.ContainsAny(component, `/\\`):
		return fmt.Errorf("component %q contains a path separator: %w", component, ErrInvalidComponent)
	case filepath.Clean(component) != component || filepath.Base(component) != component:
		return fmt.Errorf("component %q is not a single clean name: %w", component, ErrInvalidComponent)
	default:
		return nil
	}
}

// JoinChild validates every component, joins it below root, and returns the
// canonical destination. It is suitable for paths such as
// <project-root>/<category>/<name>, where each user-supplied value must remain a
// single component.
func JoinChild(root string, components ...string) (string, error) {
	if len(components) == 0 {
		return "", fmt.Errorf("join child: no components: %w", ErrRoot)
	}
	for i, component := range components {
		if err := ValidateComponent(component); err != nil {
			return "", fmt.Errorf("component %d: %w", i, err)
		}
	}
	canonicalRoot, err := Canonical(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize root: %w", err)
	}
	return CanonicalChild(canonicalRoot, filepath.Join(append([]string{canonicalRoot}, components...)...))
}

func driveQualified(component string) bool {
	if len(component) < 2 || component[1] != ':' {
		return false
	}
	letter := component[0]
	return letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z'
}

func hasParentTraversal(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == ".." {
			return true
		}
	}
	return false
}

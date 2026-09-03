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
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
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
	// ErrNotPortable reports a relative path that cannot be represented safely
	// and with the same meaning on supported Unix and Windows filesystems.
	ErrNotPortable = errors.New("path is not portable")
	// ErrPathCollision reports distinct spellings that identify the same path on
	// a case-insensitive or Unicode-normalizing filesystem.
	ErrPathCollision = errors.New("portable path collision")
	// ErrPathLimit reports a portable path that exceeds an explicit policy limit.
	ErrPathLimit = errors.New("portable path limit exceeded")
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
		// Rel only fails when the two paths cannot be expressed relative to one
		// another — most often different Windows volumes. That is a definitive
		// "not below root", not a filesystem error to surface.
		return "", fmt.Errorf("%q is not below %q: %w", candidate, root, ErrOutsideRoot)
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

// PortablePathLimits bounds the encoded path, each encoded component, and path
// depth. Zero means unbounded for that field; negative values are invalid.
// File-count and byte-size ceilings live in safefile because they describe a
// collection of files rather than a path.
type PortablePathLimits struct {
	MaxPathBytes      int
	MaxComponentBytes int
	MaxDepth          int
}

// ValidatePortableSlashPath accepts only a non-empty, clean, slash-separated
// relative path that retains exactly one meaning on Unix and Windows. It rejects
// Git metadata aliases, platform-specific separators and volumes, Windows
// device names and invalid characters, trailing dots/spaces, controls, and
// invalid UTF-8.
func ValidatePortableSlashPath(value string, limits PortablePathLimits) error {
	if limits.MaxPathBytes < 0 || limits.MaxComponentBytes < 0 || limits.MaxDepth < 0 {
		return fmt.Errorf("negative portable path limit: %w", ErrPathLimit)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("path contains invalid UTF-8: %w", ErrNotPortable)
	}
	if value == "" {
		return fmt.Errorf("path is empty: %w", ErrNotPortable)
	}
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("path contains a backslash path separator: %w", ErrNotPortable)
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || driveQualified(value) {
		return fmt.Errorf("path %q is absolute, drive-qualified, or UNC: %w", value, ErrNotPortable)
	}
	if limits.MaxPathBytes > 0 && len(value) > limits.MaxPathBytes {
		return fmt.Errorf("path is %d bytes; maximum is %d: %w", len(value), limits.MaxPathBytes, ErrPathLimit)
	}
	components := strings.Split(value, "/")
	if limits.MaxDepth > 0 && len(components) > limits.MaxDepth {
		return fmt.Errorf("path has %d components; maximum is %d: %w", len(components), limits.MaxDepth, ErrPathLimit)
	}
	for _, component := range components {
		if component == ".." {
			return fmt.Errorf("path contains parent traversal: %w", ErrTraversal)
		}
		if err := ValidatePortableComponent(component, limits.MaxComponentBytes); err != nil {
			return fmt.Errorf("path component %q: %w", component, err)
		}
	}
	return nil
}

// ValidatePortableComponent applies the per-component part of
// ValidatePortableSlashPath. maxBytes may be zero for no size limit.
func ValidatePortableComponent(component string, maxBytes int) error {
	if maxBytes < 0 {
		return fmt.Errorf("negative component limit: %w", ErrPathLimit)
	}
	if !utf8.ValidString(component) {
		return fmt.Errorf("component contains invalid UTF-8: %w", ErrNotPortable)
	}
	if err := ValidateComponent(component); err != nil {
		return fmt.Errorf("%w: %w", err, ErrNotPortable)
	}
	if maxBytes > 0 && len(component) > maxBytes {
		return fmt.Errorf("component is %d bytes; maximum is %d: %w", len(component), maxBytes, ErrPathLimit)
	}
	if strings.IndexFunc(component, isPortableControl) >= 0 {
		return fmt.Errorf("path component contains control characters: %w", ErrNotPortable)
	}
	windowsAlias := strings.TrimRight(component, ". ")
	if strings.EqualFold(windowsAlias, ".git") {
		return fmt.Errorf("path contains reserved Git metadata: %w", ErrNotPortable)
	}
	if windowsAlias != component {
		return fmt.Errorf("path component %q has a trailing dot or space and is not portable to Windows: %w", component, ErrNotPortable)
	}
	if strings.ContainsAny(component, `<>:"|?*`) {
		return fmt.Errorf("path component %q contains characters that are not portable to Windows: %w", component, ErrNotPortable)
	}
	if windowsReservedName(component) {
		return fmt.Errorf("path component %q is a reserved Windows device name: %w", component, ErrNotPortable)
	}
	return nil
}

// ValidatePortablePathSet validates every path and rejects collisions at any
// component boundary after Unicode normalization and full Unicode case folding.
// Exact duplicate paths are left to the caller's type/cardinality policy.
func ValidatePortablePathSet(paths []string, limits PortablePathLimits) error {
	type collisionNode struct {
		spelling string
		first    string
		children map[string]*collisionNode
	}
	root := &collisionNode{children: map[string]*collisionNode{}}
	for _, value := range paths {
		if err := ValidatePortableSlashPath(value, limits); err != nil {
			return fmt.Errorf("portable path %q: %w", value, err)
		}
		current := root
		for _, component := range strings.Split(value, "/") {
			key := portableCollisionKey(component)
			next, exists := current.children[key]
			if exists && next.spelling != component {
				return fmt.Errorf("paths %q and %q collide on case-insensitive or Unicode-normalizing filesystems at %q and %q: %w",
					next.first, value, next.spelling, component, ErrPathCollision)
			}
			if !exists {
				next = &collisionNode{spelling: component, first: value, children: map[string]*collisionNode{}}
				current.children[key] = next
			}
			current = next
		}
	}
	return nil
}

func portableCollisionKey(component string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(component)))
}

func isPortableControl(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func windowsReservedName(component string) bool {
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " ")
	upper := strings.ToUpper(base)
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}
	if len([]rune(upper)) != 4 {
		return false
	}
	runes := []rune(upper)
	prefix := string(runes[:3])
	if prefix != "COM" && prefix != "LPT" {
		return false
	}
	last := runes[3]
	return last >= '1' && last <= '9' || last == '¹' || last == '²' || last == '³'
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

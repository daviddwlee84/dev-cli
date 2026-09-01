package localfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const maxWalkEntries = 200_000

var dependencyComponents = map[string]struct{}{
	"node_modules": {}, ".venv": {}, "venv": {}, "vendor": {},
	"__pycache__": {}, ".tox": {}, "target": {}, ".terraform": {},
	"dist": {}, "build": {}, ".next": {},
}

// Expand resolves validated project and --file patterns locally into a sorted,
// duplicate-free list of exact slash paths. It never follows links or descends
// into Git metadata, nested repositories, submodules, or dependency trees.
func Expand(root string, patterns []Pattern, limits safefile.Limits) ([]string, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, errors.New("no local-file patterns were configured or passed with --file")
	}
	for _, pattern := range patterns {
		if err := projectconfig.ValidateLocalFilePattern(pattern.Value); err != nil {
			return nil, fmt.Errorf("local-file pattern from %s: %w", patternSource(pattern), err)
		}
		for _, component := range strings.Split(pattern.Value, "/") {
			if _, blocked := dependencyComponents[strings.ToLower(component)]; blocked {
				return nil, fmt.Errorf("local-file pattern from %s enters dependency directory %q", patternSource(pattern), component)
			}
		}
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect source checkout: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source checkout is not a held regular directory")
	}

	matches := map[string]struct{}{}
	matchedPatterns := make([]bool, len(patterns))
	visited := 0
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == root {
			return nil
		}
		visited++
		if visited > maxWalkEntries {
			return fmt.Errorf("source checkout exceeds %d entries while expanding local-file patterns", maxWalkEntries)
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(relative)
		components := strings.Split(slash, "/")
		base := strings.ToLower(components[len(components)-1])
		if entry.IsDir() {
			if base == ".git" {
				return filepath.SkipDir
			}
			if _, blocked := dependencyComponents[base]; blocked {
				for _, pattern := range patterns {
					if matchPattern(pattern.Value, slash) {
						return fmt.Errorf("local-file pattern enters dependency directory %q", slash)
					}
				}
				return filepath.SkipDir
			}
			nested, err := nestedRepositoryBoundary(filename)
			if err != nil {
				return fmt.Errorf("inspect nested repository boundary %s: %w", slash, err)
			}
			if nested {
				return filepath.SkipDir
			}
		}
		matched := false
		for index, pattern := range patterns {
			if !entry.IsDir() && matchPattern(pattern.Value, slash) {
				matchedPatterns[index] = true
				matched = true
			}
		}
		if matched {
			matches[slash] = struct{}{}
		}
		if entry.Type()&os.ModeSymlink != 0 && entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index, matched := range matchedPatterns {
		if !matched {
			return nil, fmt.Errorf("local-file pattern from %s matched no source path", patternSource(patterns[index]))
		}
	}
	paths := make([]string, 0, len(matches))
	for path := range matches {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > limits.MaxFiles {
		return nil, fmt.Errorf("patterns expanded to %d paths; maximum is %d", len(paths), limits.MaxFiles)
	}
	return paths, nil
}

func nestedRepositoryBoundary(path string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	// Bare repositories have no .git entry. Their required top-level skeleton
	// is still a repository boundary and may contain credentials, hooks, refs,
	// or private objects that must never match a portable-file glob.
	for _, marker := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(path, marker)); errors.Is(err, fs.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

func patternSource(pattern Pattern) string {
	if strings.TrimSpace(pattern.Source) == "" {
		return "explicit input"
	}
	return pattern.Source
}

func matchPattern(pattern, candidate string) bool {
	patternParts := strings.Split(pattern, "/")
	candidateParts := strings.Split(candidate, "/")
	type key struct{ pattern, candidate int }
	memo := map[key]bool{}
	seen := map[key]bool{}
	var match func(int, int) bool
	match = func(patternIndex, candidateIndex int) bool {
		state := key{patternIndex, candidateIndex}
		if seen[state] {
			return memo[state]
		}
		seen[state] = true
		var result bool
		switch {
		case patternIndex == len(patternParts):
			result = candidateIndex == len(candidateParts)
		case patternParts[patternIndex] == "**":
			result = match(patternIndex+1, candidateIndex) ||
				candidateIndex < len(candidateParts) && match(patternIndex, candidateIndex+1)
		case candidateIndex < len(candidateParts) && matchComponent(patternParts[patternIndex], candidateParts[candidateIndex]):
			result = match(patternIndex+1, candidateIndex+1)
		}
		memo[state] = result
		return result
	}
	return match(0, 0)
}

func matchComponent(pattern, candidate string) bool {
	p, c := []rune(pattern), []rune(candidate)
	type key struct{ pattern, candidate int }
	memo := map[key]bool{}
	seen := map[key]bool{}
	var match func(int, int) bool
	match = func(pi, ci int) bool {
		state := key{pi, ci}
		if seen[state] {
			return memo[state]
		}
		seen[state] = true
		var result bool
		switch {
		case pi == len(p):
			result = ci == len(c)
		case p[pi] == '*':
			result = match(pi+1, ci) || ci < len(c) && match(pi, ci+1)
		case ci < len(c) && (p[pi] == '?' || p[pi] == c[ci]):
			result = match(pi+1, ci+1)
		}
		memo[state] = result
		return result
	}
	return match(0, 0)
}

func hasDependencyComponent(path string) bool {
	for _, component := range strings.Split(path, "/") {
		if _, blocked := dependencyComponents[strings.ToLower(component)]; blocked {
			return true
		}
	}
	return false
}

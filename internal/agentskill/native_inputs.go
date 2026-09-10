package agentskill

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// nativeInputs binds project destinations (including unlocked content) and the
// locally installed dependency skill sources. Nothing executes project code.
func nativeInputs(ctx context.Context, root string, includeDependencies bool) (string, []string, error) {
	h := sha256.New()
	directories := map[string]bool{}
	for _, agent := range Registry() {
		if agent.ProjectSkillsDir != "" {
			directories[filepath.Join(root, agent.ProjectSkillsDir)] = true
		}
	}
	paths := make([]string, 0, len(directories))
	for p := range directories {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, directory := range paths {
		canonical, err := pathx.Canonical(directory)
		if err != nil {
			return "", nil, err
		}
		if inside, err := pathx.Contains(root, canonical); err != nil || !inside {
			return "", nil, errors.New("project agent directory resolves outside the checkout")
		}
		fmt.Fprintf(h, "%s\x00%s\x00", directory, canonical)
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", nil, err
			}
			info, err := os.Stat(real)
			if err != nil {
				return "", nil, err
			}
			fmt.Fprintf(h, "%s\x00%s\x00%d\x00", path, real, info.Mode())
			if info.IsDir() {
				hashes, err := installedHashes(ctx, real)
				if err != nil {
					return "", nil, err
				}
				fmt.Fprintln(h, hashes["snapshot"])
			} else {
				data, err := safefile.ReadRegular(ctx, real, 1<<20)
				if err != nil {
					return "", nil, err
				}
				h.Write(data)
			}
		}
	}
	if !includeDependencies {
		return fmt.Sprintf("%x", h.Sum(nil)), nil, nil
	}
	modules := filepath.Join(root, "node_modules")
	packages, err := os.ReadDir(modules)
	if err != nil {
		return "", nil, err
	}
	var packagePaths []string
	for _, pkg := range packages {
		if strings.HasPrefix(pkg.Name(), ".") {
			continue
		}
		path := filepath.Join(modules, pkg.Name())
		if strings.HasPrefix(pkg.Name(), "@") {
			children, e := os.ReadDir(path)
			if e != nil {
				return "", nil, e
			}
			for _, child := range children {
				packagePaths = append(packagePaths, filepath.Join(path, child.Name()))
			}
		} else {
			packagePaths = append(packagePaths, path)
		}
	}
	if len(packagePaths) > 10000 {
		return "", nil, errors.New("dependency inventory exceeds limit")
	}
	sort.Strings(packagePaths)
	found := map[string]string{}
	add := func(path string) (bool, error) {
		name, exists, err := readSkillName(filepath.Join(path, "SKILL.md"))
		if err != nil {
			return exists, err
		}
		if !exists {
			return false, nil
		}
		if !safeProviderSkillName(name) || directManagedSkillName(name) {
			return true, errors.New("dependency skill has an unsupported name")
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return true, err
		}
		if previous, ok := found[name]; ok && previous != real {
			return true, errors.New("dependency skill names collide")
		}
		hashes, err := installedHashes(ctx, real)
		if err != nil {
			return true, err
		}
		found[name] = real
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", name, real, hashes["snapshot"])
		return true, nil
	}
	for _, pkg := range packagePaths {
		fmt.Fprintln(h, pkg)
		if info, e := os.Stat(pkg); e != nil || !info.IsDir() {
			continue
		}
		if exists, e := add(pkg); e != nil {
			return "", nil, e
		} else if exists {
			continue
		}
		for _, base := range []string{pkg, filepath.Join(pkg, "skills"), filepath.Join(pkg, ".agents", "skills")} {
			children, e := os.ReadDir(base)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return "", nil, e
			}
			for _, child := range children {
				if child.Name() == "node_modules" || strings.HasPrefix(child.Name(), ".") {
					continue
				}
				p := filepath.Join(base, child.Name())
				if info, e := os.Stat(p); e != nil || !info.IsDir() {
					continue
				}
				if _, e := add(p); e != nil {
					return "", nil, e
				}
			}
		}
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("%x", h.Sum(nil)), names, nil
}

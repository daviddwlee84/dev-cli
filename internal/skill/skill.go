// Package skill embeds and installs dev's agent skill.
//
// The binary is the authority for its own skill, the same way `herdr --skill`
// is: a skill vendored separately drifts from the tool it documents, and an
// agent reading a stale command list is worse than one reading none. Embedding
// means the skill shipped is always the skill this build implements.
package skill

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

//go:embed all:dev-cli
var files embed.FS

// Name is the skill's directory and frontmatter name.
//
// Deliberately not "dev", matching the binary: a skill is selected by its name
// and description, and "dev" describes nothing. The binary is typed dozens of
// times a day and should stay short; the skill is read by an agent deciding
// whether it is relevant, and should say what it covers.
const Name = "dev-cli"

// Render returns the SKILL.md body.
func Render() (string, error) {
	b, err := files.ReadFile("dev-cli/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("bundled skill is missing: %w", err)
	}
	return string(b), nil
}

// Files walks the embedded skill tree, yielding relative paths and contents.
func Files() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(files, "dev-cli", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "dev-cli/")] = b
		return nil
	})
	return out, err
}

// InstallResult reports what an install changed.
type InstallResult struct {
	Dir       string
	Written   []string
	Skipped   []string
	Links     []string
	Removed   []string
	Preserved []string
}

// DefaultDir is where the skill is installed: the shared agent skills
// directory, which Claude Code, Codex and others all read.
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agents", "skills", Name)
}

// LinkDirs are the tool-specific directories that get a symlink back to the
// shared install, matching the layout these tools already use.
func LinkDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude", "skills")}
}

// Install writes the embedded skill to dir and links it into the per-tool
// skill directories. Writes are content-compared first, so re-running is a
// no-op and does not churn mtimes that other tools watch.
func Install(dir string, link bool) (InstallResult, error) {
	return install(dir, link, false)
}

// Refresh updates an existing install only. It never installs an absent skill or
// adds agent links, and refuses locally modified files recorded by our manifest.
func Refresh(dir string) (InstallResult, error) {
	return install(dir, false, true)
}

func install(dir string, link, existingOnly bool) (InstallResult, error) {
	dir, err := filepath.Abs(dir)
	res := InstallResult{Dir: dir}
	if err != nil {
		return res, err
	}
	if existingOnly {
		status, err := Check(dir)
		if err != nil || !status.Installed {
			return res, err
		}
	}
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return res, fmt.Errorf("skill directory must be a real directory: %s", dir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return res, err
	}
	err = lockx.WithFile(context.Background(), filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".install.lock"), "bundled skill", func() error {
		if existingOnly {
			status, err := Check(dir)
			if err != nil || !status.Installed {
				return err
			}
			if len(status.Modified) > 0 {
				return fmt.Errorf("installed skill has local changes (%s); preserve them before running dev skill install", strings.Join(status.Modified, ", "))
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		root, err := openSkillRoot(dir)
		if err != nil {
			return err
		}
		defer root.Close()
		previous, err := readManifest(root)
		if err != nil {
			return err
		}
		all, err := Files()
		if err != nil {
			return err
		}
		// Validate the complete destination set before writing any file.
		for _, rel := range sortedPaths(all) {
			if err := validateFilePath(root, rel); err != nil {
				return err
			}
		}
		if err := validateFilePath(root, manifestName); err != nil {
			return err
		}
		if previous != nil {
			for path := range previous.Files {
				if err := validateFilePath(root, path); err != nil {
					return err
				}
			}
		}
		for _, rel := range sortedPaths(all) {
			content := all[rel]
			if existing, err := root.ReadFile(rel); err == nil && string(existing) == string(content) {
				res.Skipped = append(res.Skipped, rel)
				continue
			}
			if err := writeSkillFile(root, rel, content); err != nil {
				return err
			}
			res.Written = append(res.Written, rel)
		}
		if previous != nil {
			for _, path := range sortedPaths(previous.Files) {
				if _, retained := all[path]; retained {
					continue
				}
				body, err := root.ReadFile(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if digest(body) != previous.Files[path] {
					res.Preserved = append(res.Preserved, path)
					continue
				}
				if err := root.Remove(path); err != nil {
					return err
				}
				res.Removed = append(res.Removed, path)
			}
		}
		if err := writeManifest(root, all); err != nil {
			return err
		}
		if !link {
			return nil
		}
		for _, base := range LinkDirs() {
			if _, err := os.Stat(base); err != nil {
				continue
			}
			target := filepath.Join(base, Name)
			if _, err := os.Lstat(target); err == nil {
				continue // Preserve existing links and directories, including foreign links.
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Symlink(dir, target); err != nil {
				return fmt.Errorf("link %s: %w", target, err)
			}
			res.Links = append(res.Links, target)
		}
		return nil
	})
	return res, err
}

func resolveLink(base, link string) string {
	if filepath.IsAbs(link) {
		return filepath.Clean(link)
	}
	return filepath.Clean(filepath.Join(base, link))
}

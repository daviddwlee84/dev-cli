// Package skill embeds and installs dev's agent skill.
//
// The binary is the authority for its own skill, the same way `herdr --skill`
// is: a skill vendored separately drifts from the tool it documents, and an
// agent reading a stale command list is worse than one reading none. Embedding
// means the skill shipped is always the skill this build implements.
package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	Dir     string
	Written []string
	Skipped []string
	Links   []string
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
	res := InstallResult{Dir: dir}
	all, err := Files()
	if err != nil {
		return res, err
	}
	if len(all) == 0 {
		return res, fmt.Errorf("no skill files are embedded in this build")
	}

	for rel, content := range all {
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		if existing, err := os.ReadFile(dst); err == nil && string(existing) == string(content) {
			res.Skipped = append(res.Skipped, rel)
			continue
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return res, fmt.Errorf("write %s: %w", dst, err)
		}
		res.Written = append(res.Written, rel)
	}

	if !link {
		return res, nil
	}
	for _, base := range LinkDirs() {
		if _, err := os.Stat(base); err != nil {
			continue // that tool is not installed here
		}
		target := filepath.Join(base, Name)
		if existing, err := os.Readlink(target); err == nil {
			if resolveLink(base, existing) == dir {
				continue // already correct
			}
			os.Remove(target)
		} else if _, err := os.Stat(target); err == nil {
			// A real directory, not our symlink: never clobber it.
			continue
		}
		if err := os.Symlink(dir, target); err != nil {
			return res, fmt.Errorf("link %s: %w", target, err)
		}
		res.Links = append(res.Links, target)
	}
	return res, nil
}

func resolveLink(base, link string) string {
	if filepath.IsAbs(link) {
		return filepath.Clean(link)
	}
	return filepath.Clean(filepath.Join(base, link))
}

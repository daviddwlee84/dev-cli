package submodule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// The superproject-workspace agent skill opens worktrees of the children of a
// superproject, never of the outer repository. dev only recognizes and labels
// that layout; it never creates, moves or removes those worktrees itself.
const (
	ConventionName         = "superproject-workspace"
	conventionMarker       = ".superproject"
	conventionTasksDir     = "_tasks"
	conventionDefaultRoot  = ".worktrees"
	conventionLocalRootKey = "superproject.worktreeroot"
	conventionMarkerKey    = "workspace.worktree-root"
)

// Convention is a read-only observation of the superproject-workspace layout
// for one checkout. Detection is evidence of intent, not ownership proof.
type Convention struct {
	Detected bool `json:"detected"`
	// Sources lists what triggered detection: "gitmodules-x-policy" and/or
	// "superproject-file".
	Sources []string `json:"sources,omitempty"`
	// WorktreeRoot is the absolute child worktree root the skill would use.
	WorktreeRoot string `json:"worktree_root,omitempty"`
	// WorktreeRootSource is "git-config", "superproject-file" or "default".
	WorktreeRootSource string `json:"worktree_root_source,omitempty"`
	// WorktreeRootInside reports a root inside the checkout, which conflicts
	// with dev's rule that managed worktrees stay outside repositories.
	WorktreeRootInside bool `json:"worktree_root_inside_repository"`
}

// ChildWorktree is one linked worktree registered by an initialized child
// repository of the observed graph.
type ChildWorktree struct {
	Submodule string `json:"submodule"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Locked    bool   `json:"locked,omitempty"`
	Prunable  bool   `json:"prunable,omitempty"`
	// Store is "canonical" when the child's Git store is under the
	// superproject's shared <common>/modules, "outer-worktree" when it belongs
	// to a linked worktree of the superproject (and dies with it), else "other".
	Store string `json:"store"`
	// ConventionManaged reports a worktree under the convention's worktree root
	// of a top-level child, in a detected superproject-workspace.
	ConventionManaged bool `json:"convention_managed"`
	// Layout is "child" (<root>/<child-path>/<branch-slug>) or "task"
	// (<root>/_tasks/<task>/<child-path>) for convention-managed worktrees.
	Layout string `json:"layout,omitempty"`
	Task   string `json:"task,omitempty"`
	// Guidance names the skill command that closes the worktree.
	Guidance string `json:"guidance,omitempty"`
}

// DetectConvention reads the checkout's working .gitmodules and .superproject
// files and the repository-local superproject.worktreeRoot setting, mirroring
// the skill's precedence. It never writes or contacts a remote.
func DetectConvention(ctx context.Context, root string) (Convention, error) {
	var c Convention
	gitmodules := filepath.Join(root, ".gitmodules")
	if regularFile(gitmodules) {
		values, err := configFileValues(ctx, root, gitmodules)
		if err != nil {
			return c, fmt.Errorf("read .gitmodules: %w", err)
		}
		for key := range values {
			if strings.HasPrefix(key, "submodule.") && strings.HasSuffix(key, ".x-policy") {
				c.Sources = append(c.Sources, "gitmodules-x-policy")
				break
			}
		}
	}
	marker := filepath.Join(root, conventionMarker)
	var markerValues map[string]string
	if regularFile(marker) {
		c.Sources = append(c.Sources, "superproject-file")
		var err error
		if markerValues, err = configFileValues(ctx, root, marker); err != nil {
			return c, fmt.Errorf("read %s: %w", conventionMarker, err)
		}
	}
	c.Detected = len(c.Sources) > 0
	if !c.Detected {
		return c, nil
	}
	value, source := conventionDefaultRoot, "default"
	local, err := gitx.Run(ctx, root, "config", "--local", "--null", "--list")
	if err != nil {
		return c, fmt.Errorf("read local Git configuration: %w", err)
	}
	if v, ok := nulConfig(local)[conventionLocalRootKey]; ok && v != "" {
		value, source = v, "git-config"
	} else if v := markerValues[conventionMarkerKey]; v != "" {
		value, source = v, "superproject-file"
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~"))
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	if c.WorktreeRoot, err = pathx.Canonical(value); err != nil {
		return c, err
	}
	c.WorktreeRootSource = source
	if c.WorktreeRootInside, err = pathx.Contains(root, c.WorktreeRoot); err != nil {
		return c, err
	}
	return c, nil
}

// ChildWorktrees lists every additional worktree registered by the graph's
// initialized children and labels the superproject-workspace layout.
func ChildWorktrees(ctx context.Context, g gitx.SubmoduleGraph) (Convention, []ChildWorktree, error) {
	c, err := DetectConvention(ctx, g.Root)
	if err != nil {
		return c, nil, err
	}
	var out []ChildWorktree
	for _, n := range g.Nodes {
		rows, err := childWorktreesOf(ctx, g, c, n)
		if err != nil {
			return c, out, err
		}
		out = append(out, rows...)
	}
	return c, out, nil
}

func childWorktreesOf(ctx context.Context, g gitx.SubmoduleGraph, c Convention, n gitx.SubmoduleNode) ([]ChildWorktree, error) {
	if !n.Initialized {
		return nil, nil
	}
	child := filepath.Join(g.Root, filepath.FromSlash(n.Path))
	list, err := gitx.Worktrees(ctx, child)
	if err != nil {
		return nil, fmt.Errorf("submodule %s worktrees: %w", n.Path, err)
	}
	store, err := childStore(ctx, g.Root, n)
	if err != nil {
		return nil, err
	}
	var out []ChildWorktree
	for _, w := range list {
		if w.Main || w.Bare || samePath(w.Path, child) {
			continue
		}
		row := ChildWorktree{Submodule: n.Path, Name: n.Name, Path: w.Path, Branch: w.Branch, Head: w.Head, Locked: w.Locked, Prunable: w.Prunable, Store: store}
		if c.Detected && n.Parent == "" {
			row.ConventionManaged, row.Layout, row.Task = classifyConventionPath(c.WorktreeRoot, w.Path, n.Path)
		}
		row.Guidance = conventionGuidance(row)
		out = append(out, row)
	}
	return out, nil
}

// classifyConventionPath mirrors the skill: anything under the worktree root is
// managed; the exact single-child and task layouts are named.
func classifyConventionPath(wtRoot, path, childPath string) (bool, string, string) {
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return false, "", ""
	}
	inside, err := pathx.Contains(wtRoot, canonical)
	if err != nil || !inside || samePath(wtRoot, canonical) {
		return false, "", ""
	}
	rel, err := filepath.Rel(wtRoot, canonical)
	if err != nil {
		return false, "", ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 && parts[0] == conventionTasksDir {
		if strings.Join(parts[2:], "/") == childPath {
			return true, "task", parts[1]
		}
		return true, "", parts[1]
	}
	if len(parts) >= 2 && strings.Join(parts[:len(parts)-1], "/") == childPath {
		return true, "child", ""
	}
	return true, "", ""
}

func conventionGuidance(w ChildWorktree) string {
	if !w.ConventionManaged {
		if w.Store == "outer-worktree" {
			return "registered in this outer worktree's child store and orphaned when it is removed; commit, push and `git worktree remove` it first"
		}
		return ""
	}
	var close string
	switch {
	case w.Task != "":
		close = fmt.Sprintf("close it with the %s skill: `superproject.py task close %s`", ConventionName, w.Task)
	case w.Layout == "child" && w.Branch != "" && filepath.Base(w.Path) == strings.ReplaceAll(w.Branch, "/", "-"):
		close = fmt.Sprintf("remove it with the %s skill: `superproject.py worktree remove %s %s`", ConventionName, w.Name, w.Branch)
	default:
		close = fmt.Sprintf("close it with the %s skill (`superproject.py worktree list --all`)", ConventionName)
	}
	if w.Store == "outer-worktree" {
		return "registered in this outer worktree's child store and orphaned when it is removed; commit and push, then " + close
	}
	return "commit and push, then " + close
}

// Label is a one-line description for text output and blocker messages.
func (w ChildWorktree) Label() string {
	kind := "linked worktree"
	switch {
	case w.ConventionManaged && w.Task != "":
		kind = fmt.Sprintf("%s task %q worktree", ConventionName, w.Task)
	case w.ConventionManaged:
		kind = ConventionName + " child worktree"
	}
	text := w.Path + " (" + kind
	if w.Guidance != "" {
		text += "; " + w.Guidance
	}
	return text + ")"
}

// DescribeChildWorktrees explains the additional worktrees of one child for a
// cleanup blocker. Observation failures degrade to the opaque blocker.
func DescribeChildWorktrees(ctx context.Context, g gitx.SubmoduleGraph, n gitx.SubmoduleNode) string {
	c, err := DetectConvention(ctx, g.Root)
	if err != nil {
		return ""
	}
	rows, err := childWorktreesOf(ctx, g, c, n)
	if err != nil || len(rows) == 0 {
		return ""
	}
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		labels = append(labels, row.Label())
	}
	return strings.Join(labels, "; ")
}

func childStore(ctx context.Context, root string, n gitx.SubmoduleNode) (string, error) {
	r, err := gitx.Discover(ctx, root)
	if err != nil {
		return "", err
	}
	for _, candidate := range []struct{ dir, kind string }{
		{filepath.Join(r.GitCommonDir, "modules"), "canonical"},
		{filepath.Join(r.GitCommonDir, "worktrees"), "outer-worktree"},
	} {
		inside, err := pathx.Contains(candidate.dir, n.CommonDir)
		if err != nil {
			return "", err
		}
		if inside {
			return candidate.kind, nil
		}
	}
	return "other", nil
}

func configFileValues(ctx context.Context, dir, file string) (map[string]string, error) {
	out, err := gitx.Run(ctx, dir, "config", "--file", file, "--null", "--list")
	if err != nil {
		return nil, err
	}
	return nulConfig(out), nil
}

// nulConfig parses `git config --null --list`: key LF value NUL. Section and
// variable names are already lower-cased by Git.
func nulConfig(out string) map[string]string {
	values := map[string]string{}
	for record := range strings.SplitSeq(out, "\x00") {
		if record == "" {
			continue
		}
		key, value, _ := strings.Cut(record, "\n")
		values[key] = value
	}
	return values
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func samePath(a, b string) bool {
	ca, errA := pathx.Canonical(a)
	cb, errB := pathx.Canonical(b)
	return errA == nil && errB == nil && ca == cb
}

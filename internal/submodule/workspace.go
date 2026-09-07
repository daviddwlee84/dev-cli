// Package submodule owns workspace member intent. Git observations remain in
// gitx and lifecycle policy remains in taskflow.
package submodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

type Member struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
}

// Workspace is a separate versioned intent record: older task writers cannot
// drop member intent when saving a legacy task TOML. No observation is stored.
type Workspace struct {
	Version    int      `json:"version"`
	Repository string   `json:"repository"`
	Branch     string   `json:"branch"`
	Init       string   `json:"init"`
	Members    []Member `json:"members"`
}

func Settings(cfg config.Config, root, explicit string) (config.Submodules, error) {
	s := cfg.Submodules
	if s.Init == "" {
		s.Init = "recursive"
	}
	p, err := projectconfig.Load(root, nil)
	if err != nil {
		return s, err
	}
	if p.Effective.Submodules.Init != nil {
		s.Init = *p.Effective.Submodules.Init
	}
	if p.Effective.Submodules.Develop != nil {
		s.Develop = append([]string(nil), (*p.Effective.Submodules.Develop)...)
	}
	if explicit != "" {
		s.Init = explicit
	}
	return s, s.Validate()
}

func workspaceFile(cfg config.Config, repository, branch string) string {
	sum := sha256.Sum256([]byte(repository + "\x00" + branch))
	return filepath.Join(filepath.Dir(cfg.TasksDir()), "workspaces", hex.EncodeToString(sum[:])+".json")
}

func Load(cfg config.Config, repository, branch string) (Workspace, string, error) {
	w := Workspace{Version: 1, Repository: repository, Branch: branch, Init: "recursive"}
	data, err := os.ReadFile(workspaceFile(cfg, repository, branch))
	if os.IsNotExist(err) {
		return w, "", nil
	}
	if err != nil {
		return w, "", err
	}
	if err = json.Unmarshal(data, &w); err != nil {
		return w, "", err
	}
	if w.Version != 1 || w.Repository != repository || w.Branch != branch {
		return w, "", errors.New("workspace intent identity/version mismatch")
	}
	sum := sha256.Sum256(data)
	return w, hex.EncodeToString(sum[:]), nil
}

func save(cfg config.Config, w Workspace) error {
	return writeJSON(workspaceFile(cfg, w.Repository, w.Branch), w)
}

// Prepare runs before a runtime is opened. It retains an intent before branch
// creation, so a partial failure remains explicitly recoverable on retry.
func Prepare(ctx context.Context, cfg config.Config, root, mode string, develop []string, bases map[string]string, taskStart bool) (gitx.SubmoduleGraph, error) {
	s, err := Settings(cfg, root, mode)
	if err != nil {
		return gitx.SubmoduleGraph{}, err
	}
	g, err := gitx.SubmodulesOf(ctx, root)
	if err != nil {
		return g, err
	}
	if len(g.Nodes) == 0 {
		if len(develop) > 0 {
			return g, errors.New("selected submodule does not exist")
		}
		return g, nil
	}
	r, err := gitx.Discover(ctx, root)
	if err != nil {
		return g, err
	}
	st, err := gitx.StatusOf(ctx, root)
	if err != nil {
		return g, err
	}
	if r.IsLinkedWorktree && !taskStart && mode == "" {
		previous, revision, err := Load(cfg, r.GitCommonDir, st.Branch)
		if err != nil {
			return g, err
		}
		if revision != "" {
			s.Init = previous.Init
		}
	}
	if s.Init == "recursive" {
		g, err = gitx.InitSubmodules(ctx, root)
		if err != nil {
			return g, err
		}
	}
	if !r.IsLinkedWorktree || st.Branch == "" {
		return g, nil
	}
	if taskStart && develop == nil && s.Init != "none" {
		develop = s.Develop
	}
	if len(develop) > 0 && s.Init == "none" {
		return g, errors.New("developing submodules requires recursive initialization")
	}
	err = lockx.WithDir(ctx, filepath.Dir(workspaceFile(cfg, r.GitCommonDir, st.Branch)), "workspace intent", func() error {
		w, _, err := Load(cfg, r.GitCommonDir, st.Branch)
		if err != nil {
			return err
		}
		if taskStart {
			w.Members = nil
		}
		w.Init = s.Init
		nodes := map[string]gitx.SubmoduleNode{}
		for _, n := range g.Nodes {
			nodes[n.Path] = n
		}
		known := map[string]Member{}
		for _, m := range w.Members {
			known[m.Path] = m
		}
		var pending []Member
		for _, p := range develop {
			n, ok := nodes[p]
			if !ok || !n.Initialized {
				return fmt.Errorf("submodule %s is unavailable", p)
			}
			if _, ok := known[p]; ok {
				continue
			}
			if n.HEAD != n.Gitlink || n.Status.Dirty() {
				return fmt.Errorf("submodule %s must match its clean gitlink before developing", p)
			}
			child := filepath.Join(root, filepath.FromSlash(p))
			if gitx.BranchExists(ctx, child, st.Branch) || gitx.RefExists(ctx, child, "refs/remotes/origin/"+st.Branch) {
				return fmt.Errorf("submodule %s already has branch %s; not adopting it", p, st.Branch)
			}
			base := bases[p]
			if base == "" {
				base = gitx.DefaultBranch(ctx, child)
			}
			if base == "" {
				return fmt.Errorf("submodule %s needs --submodule-base %s=REF", p, p)
			}
			if !strings.HasPrefix(base, "refs/") && !strings.HasPrefix(base, "origin/") {
				base = "refs/remotes/origin/" + base
			}
			if !gitx.RefExists(ctx, child, base) {
				return fmt.Errorf("submodule %s base %s unavailable", p, base)
			}
			m := Member{Path: p, Branch: st.Branch, Base: base}
			pending = append(pending, m)
			known[p] = m
			w.Members = append(w.Members, m)
		}
		sort.Slice(w.Members, func(i, j int) bool { return w.Members[i].Path < w.Members[j].Path })
		if err = save(cfg, w); err != nil {
			return err
		}
		for _, m := range pending {
			child := filepath.Join(root, filepath.FromSlash(m.Path))
			if _, err = gitx.Run(ctx, child, "switch", "-c", m.Branch, nodes[m.Path].Gitlink); err != nil {
				return fmt.Errorf("develop %s: %w (workspace intent retained)", m.Path, err)
			}
		}
		// Existing members are recreated when resuming a COLD workspace, but
		// only at the exact committed gitlink, never a moving remote branch.
		for _, m := range w.Members {
			n, ok := nodes[m.Path]
			if !ok || !n.Initialized {
				return fmt.Errorf("workspace member %s unavailable", m.Path)
			}
			child := filepath.Join(root, filepath.FromSlash(m.Path))
			current, err := gitx.StatusOf(ctx, child)
			if err != nil {
				return err
			}
			if current.Branch == m.Branch {
				continue
			}
			if !current.Detached || current.Dirty() {
				return fmt.Errorf("workspace member %s changed branch/content", m.Path)
			}
			if n.HEAD != n.Gitlink {
				return fmt.Errorf("workspace member %s moved away from its gitlink; preserving its HEAD", m.Path)
			}
			if gitx.BranchExists(ctx, child, m.Branch) {
				return fmt.Errorf("workspace member %s has a conflicting local branch", m.Path)
			}
			if _, err = gitx.Run(ctx, child, "switch", "-c", m.Branch, n.Gitlink); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return g, err
	}
	return gitx.SubmodulesOf(ctx, root)
}

func Forget(cfg config.Config, repository, branch string) error {
	err := os.Remove(workspaceFile(cfg, repository, branch))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

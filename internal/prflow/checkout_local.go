package prflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Preserve the user's native fetch transport and authentication context. Every
// candidate must independently identify the provider's base repository.
func selectCheckoutFetchURL(ctx context.Context, root string, d Detail, selected string) (string, error) {
	top, err := gitx.RecoveryTopologyOf(ctx, root)
	if err != nil {
		return "", err
	}
	candidates := map[string]string{}
	for _, remote := range top.Remotes {
		matches := false
		for _, endpoint := range remote.FetchURLs {
			matches = matches || samePRRepository(forge.ParseRemoteIdentity(endpoint), d.Reference.Forge, d.Reference.Host, d.Reference.Repo)
		}
		if !matches {
			continue
		}
		if len(remote.FetchURLs) != 1 {
			return "", fmt.Errorf("base remote %s has multiple fetch endpoints; select an unambiguous native remote", remote.Name)
		}
		candidates[remote.Name] = remote.FetchURLs[0]
	}
	if selected != "" {
		if endpoint, ok := candidates[selected]; ok {
			return endpoint, nil
		}
		return "", errors.New("selected source remote does not identify the PR base repository")
	}
	branches, err := gitx.BranchStates(ctx, root)
	if err != nil {
		return "", err
	}
	for _, branch := range branches {
		if branch.Ref == "refs/heads/"+d.BaseBranch && branch.RemoteRef == "refs/heads/"+d.BaseBranch {
			if endpoint, ok := candidates[branch.Remote]; ok {
				return endpoint, nil
			}
		}
	}
	for _, name := range []string{"upstream", "origin"} {
		if endpoint, ok := candidates[name]; ok {
			return endpoint, nil
		}
	}
	endpoint := ""
	for _, candidate := range candidates {
		if endpoint != "" && endpoint != candidate {
			return "", errors.New("multiple custom base fetch endpoints; choose --source-remote")
		}
		endpoint = candidate
	}
	if endpoint != "" {
		return endpoint, nil
	}
	return d.BaseURL, nil
}

// Local performs only local Git/catalog reads. Rows with no Path identify a
// matching repository in which a new worktree can be prepared. It deliberately
// does not refresh remote observations or enroll any Try.
func (s *CheckoutService) Local(ctx context.Context, d Detail) ([]LocalCheckout, error) {
	return s.local(ctx, d, "")
}

func (s *CheckoutService) local(ctx context.Context, d Detail, explicit string) ([]LocalCheckout, error) {
	paths := append([]string(nil), s.cfg.Repositories...)
	catalogIDs := map[string]string{}
	tryRoots := map[string]bool{}
	if s.cfg.Experiments != nil {
		items, _, err := s.cfg.Experiments.List(ctx, experiment.ListOptions{ReadOnly: true, SkipEnrichment: true})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.CurrentPath() != "" {
				paths = append(paths, item.CurrentPath())
				if canonical, e := pathx.Canonical(item.CurrentPath()); e == nil {
					catalogIDs[canonical] = item.ID
					tryRoots[canonical] = true
				}
			}
		}
	}
	if explicit != "" {
		paths = []string{explicit}
	}
	seen := map[string]bool{}
	out := []LocalCheckout{}
	headRepo := d.HeadRepo
	if headRepo == "" && !d.CrossRepository {
		headRepo = d.Reference.Repo
	}
	for _, path := range paths {
		g, err := gitx.Discover(ctx, path)
		if err != nil {
			if explicit != "" {
				return nil, err
			}
			continue
		}
		common, err := pathx.Canonical(g.GitCommonDir)
		if err != nil {
			return nil, err
		}
		if seen[common] {
			continue
		}
		seen[common] = true
		top, err := gitx.RecoveryTopologyOf(ctx, g.MainRoot)
		if err != nil {
			return nil, err
		}
		matching := false
		headRemotes := map[string]bool{}
		for _, remote := range top.Remotes {
			for _, raw := range remote.FetchURLs {
				identity := forge.ParseRemoteIdentity(raw)
				if samePRRepository(identity, d.Reference.Forge, d.Reference.Host, d.Reference.Repo) {
					matching = true
				}
				if headRepo != "" && samePRRepository(identity, d.Reference.Forge, d.Reference.Host, headRepo) {
					matching = true
					headRemotes[remote.Name] = true
				}
			}
		}
		if !matching {
			if explicit != "" {
				return nil, errors.New("selected repository has no remote matching the PR base or head repository")
			}
			continue
		}
		branches, err := gitx.BranchStates(ctx, g.MainRoot)
		if err != nil {
			return nil, err
		}
		byBranch := map[string]gitx.BranchState{}
		for _, branch := range branches {
			byBranch[strings.TrimPrefix(branch.Ref, "refs/heads/")] = branch
		}
		worktrees, err := gitx.Worktrees(ctx, g.MainRoot)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, w := range worktrees {
			if w.Branch == "" || w.Prunable || w.Bare {
				continue
			}
			binding, _ := gitx.Run(ctx, g.MainRoot, "config", "--local", "--get-all", "branch."+w.Branch+".dev-pr-url")
			bound := binding == d.Reference.URL()
			b := byBranch[w.Branch]
			natural := w.Branch == d.HeadBranch && ((headRemotes[b.Remote] && b.RemoteRef == "refs/heads/"+d.HeadBranch) || (len(headRemotes) > 0 && w.Head == d.HeadOID))
			if !bound && !natural {
				continue
			}
			if _, err := os.Stat(w.Path); err != nil {
				continue
			}
			status, err := gitx.StatusOf(ctx, w.Path)
			if err != nil {
				return nil, err
			}
			if status.Branch != w.Branch {
				continue
			}
			// Resolve by exact path to reject duplicate registry aliases.
			if _, err := gitx.ResolveRegisteredWorktree(ctx, g.MainRoot, w.Path); err != nil {
				return nil, err
			}
			out = append(out, LocalCheckout{RepoPath: g.MainRoot, GitCommonDir: common, Path: w.Path, Branch: w.Branch, HeadOID: w.Head, Dirty: status.Dirty(), Differs: w.Head != d.HeadOID, Bound: bound, CatalogID: catalogIDs[g.MainRoot]})
			matched = true
		}
		if !matched {
			// A standalone Try must remain movable and removable through its
			// own lifecycle. Its remote identifies a source, not authority to
			// turn it into a shared worktree hub for another request or retry.
			if tryRoots[g.MainRoot] {
				if explicit != "" {
					return nil, errors.New("selected Try has no checkout for this PR; create a new independent Try with --try and omit --repo")
				}
				continue
			}
			out = append(out, LocalCheckout{RepoPath: g.MainRoot, GitCommonDir: common, CatalogID: catalogIDs[g.MainRoot]})
		}
	}
	if explicit != "" {
		g, err := gitx.Discover(ctx, explicit)
		if err != nil {
			return nil, err
		}
		for _, candidate := range out {
			if candidate.Path == g.Root {
				return []LocalCheckout{candidate}, nil
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RepoPath != out[j].RepoPath {
			return out[i].RepoPath < out[j].RepoPath
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

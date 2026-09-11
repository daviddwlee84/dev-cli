package feedback

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// Sources uses the same discovery as REPOS. An explicit or configured path is
// authoritative: an invalid value never falls through to a different checkout.
func Sources(ctx context.Context, cfg config.Config, explicit, configured string) ([]RepairSource, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	selected := explicit
	if selected == "" {
		selected = configured
	}
	if selected != "" {
		source, err := VerifySource(ctx, config.Expand(selected))
		if err != nil {
			return nil, err
		}
		return []RepairSource{source}, nil
	}
	repos, err := repo.Discover(ctx, cfg.DiscoveryRoots(), repo.DefaultOptions())
	if err != nil {
		return nil, errors.New("repository discovery unavailable")
	}
	result := []RepairSource{}
	seen := map[string]bool{}
	for _, r := range repos {
		source, err := VerifySource(ctx, r.Path)
		if err == nil && !seen[source.CommonDir] {
			seen[source.CommonDir] = true
			result = append(result, source)
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, nil
}
func VerifySource(ctx context.Context, path string) (RepairSource, error) {
	r, err := gitx.Discover(ctx, path)
	if err != nil || r.Bare {
		return RepairSource{}, errors.New("repair source must be a usable dev-cli checkout")
	}
	remotes, err := gitx.Run(ctx, r.MainRoot, "remote")
	if err != nil {
		return RepairSource{}, errors.New("repair source remotes unavailable")
	}
	source := RepairSource{Path: r.MainRoot, CommonDir: r.GitCommonDir}
	originMatches := false
	for _, remote := range strings.Fields(remotes) {
		raw, err := gitx.Run(ctx, r.MainRoot, "remote", "get-url", remote)
		if err != nil {
			continue
		}
		identity := forge.ParseRemoteIdentity(strings.TrimSpace(raw))
		if identity.Host == "github.com" && strings.EqualFold(identity.Name, "daviddwlee84/dev-cli") {
			if source.Remote == "" || remote == "upstream" {
				source.Remote = remote
			}
			if remote == "origin" {
				originMatches = true
			}
			source.Repository = forge.DefaultIssueRepository
		}
	}
	if source.Remote == "" {
		return source, errors.New("repair source has no verified dev-cli upstream remote; a directory name or fork origin alone is insufficient")
	}
	source.Fork = !originMatches
	return source, nil
}

package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func tuiDiscoveryActions(state *tuiAppState, resolver *tuiProjectRootResolver) tui.DiscoveryActions {
	file := state.Current().configPath
	if file == "" {
		file = config.ConfigFile()
	}
	file = config.Expand(file)
	return tui.DiscoveryActions{
		Read: func(ctx context.Context) (tui.StartupRepository, error) {
			target, err := resolver.ResolveTarget(ctx)
			if err != nil {
				return tui.StartupRepository{}, err
			}
			if target.CommonDir == "" || target.CommonDir == target.CheckoutRoot {
				return tui.StartupRepository{}, nil
			}
			covered, coverageErr := repo.DiscoveryCovered(state.Current().Cfg, target.RepoPath)
			return tui.StartupRepository{Path: target.RepoPath, CommonDir: target.CommonDir, Covered: covered, CoverageErr: coverageErr}, nil
		},
		Plan: func(ctx context.Context, startup tui.StartupRepository, scope repo.DiscoveryScope) (repo.DiscoveryRegistrationPlan, error) {
			return repo.PlanDiscoveryRegistration(ctx, file, startup.Path, startup.CommonDir, scope)
		},
		Apply: func(ctx context.Context, plan repo.DiscoveryRegistrationPlan) (string, error) {
			result, err := repo.ApplyDiscoveryRegistration(ctx, plan)
			status := "Configuration " + result.Status
			if result.Receipt != "" {
				status += fmt.Sprintf(" (recovery %s)", result.Receipt)
			}
			return status, err
		},
	}
}

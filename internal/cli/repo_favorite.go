package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func setRepositoryFavorite(ctx context.Context, app *App, row tui.RepoRow, desired bool) (*catalog.Entry, error) {
	if app == nil || app.Registry == nil {
		return nil, errors.New("repository catalog is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := ""
	if row.Asset != nil {
		id = row.Asset.ID
	}
	if id == "" {
		path, err := repo.ValidateNavigation(ctx, row.Repo, "")
		if err != nil {
			return nil, err
		}
		realPath, err := pathx.Canonical(path)
		if err != nil {
			return nil, err
		}
		observation := catalog.Observation{Host: config.Hostname(), Path: path, RealPath: realPath, Name: row.Repo.Name}
		if row.Repo.Bare {
			// ValidateNavigation verified this exact bare hub; Discover's ordinary
			// working-tree probe cannot supply --show-toplevel for it.
			observation.CommonDir = realPath
			observation.RemoteIdentity = gitx.RemoteFromConfig(realPath, "origin")
		} else if row.Repo.HasGit || row.Repo.CommonDir != "" || row.Repo.GitDir != "" {
			git, err := gitx.Discover(ctx, path)
			if err != nil {
				return nil, fmt.Errorf("validate favorite repository: %w", err)
			}
			observation.CommonDir = git.GitCommonDir
			observation.RemoteIdentity = gitx.RemoteFromConfig(git.GitCommonDir, "origin")
		}
		entry, err := app.Registry.EnsureRepository(observation)
		if err != nil {
			return nil, err
		}
		id = entry.ID
	}
	// The preference belongs to the stable asset, not the current availability
	// of its checkout. In particular a missing favorite can still be removed.
	return app.Registry.Patch(id, func(entry *catalog.Entry) error {
		if entry.Kind != catalog.KindRepository {
			return errors.New("favorites are only available for repositories")
		}
		entry.Favorite = desired
		return nil
	})
}

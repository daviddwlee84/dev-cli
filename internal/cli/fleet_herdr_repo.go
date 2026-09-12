package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/spf13/cobra"
)

func newFleetHerdrRepoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use: fleet.HerdrRepoHelper, Hidden: true, Args: cobra.NoArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			// A check must not initialize registries, refresh release metadata or
			// perform the ordinary startup binary cleanup.
			if app.In == nil {
				app.In = os.Stdin
			}
			if app.Out == nil {
				app.Out = os.Stdout
			}
			if app.Err == nil {
				app.Err = os.Stderr
			}
			cfg, err := config.Load(app.configPath)
			if err != nil {
				return err
			}
			app.Cfg = cfg
			return validateColorMode(app.colorMode)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			var request fleet.HerdrRepoRequest
			if err := fleet.DecodeStrict(app.In, fleet.MaxHerdrRepoBytes, &request); err != nil {
				return err
			}
			if err := request.Validate(); err != nil {
				return err
			}
			result, err := prepareFleetHerdrRepository(cmd.Context(), app, request)
			if err != nil {
				return err
			}
			if err := result.Validate(request); err != nil {
				return err
			}
			data, err := fleet.MarshalBounded(result, fleet.MaxHerdrRepoBytes)
			if err != nil {
				return err
			}
			_, err = app.Out.Write(data)
			return err
		},
	}
}

func inspectFleetHerdrRepository(ctx context.Context, app *App, request fleet.HerdrRepoRequest) (repo.Repo, string, error) {
	if !filepath.IsAbs(request.Repository.Path) {
		return repo.Repo{}, "", errors.New("selected remote repository path must be absolute")
	}
	repository, err := resolveFleetOpenRepository(ctx, app, request.Repository)
	if err != nil {
		return repo.Repo{}, "", err
	}
	identity, err := gitx.Discover(ctx, repository.Path)
	if err != nil || identity.Bare || identity.Root == "" {
		return repo.Repo{}, "", errors.New("selected repository has no verifiable checkout")
	}
	fingerprint, err := fleet.HerdrRepositoryIdentity(identity.Root, identity.GitDir, identity.GitCommonDir)
	if err != nil {
		return repo.Repo{}, "", err
	}
	if request.ExpectedIdentity != "" && request.ExpectedIdentity != fingerprint {
		return repo.Repo{}, "", errors.New("selected repository changed since checking")
	}
	return repository, fingerprint, nil
}

func prepareFleetHerdrRepository(ctx context.Context, app *App, request fleet.HerdrRepoRequest) (fleet.HerdrRepoResult, error) {
	result := fleet.HerdrRepoResult{SchemaVersion: fleet.HerdrRepoSchemaVersion, Phase: request.Phase, Session: request.Session, Path: request.Repository.Path, RemoteIdentity: request.Repository.RemoteIdentity}
	if err := request.Validate(); err != nil {
		return result, err
	}
	repository, identity, err := inspectFleetHerdrRepository(ctx, app, request)
	if err != nil {
		return result, err
	}
	result.RepositoryIdentity = identity
	h, err := runtime.NewHerdr().WithMetadataSource(app.Cfg.Runtime.MetadataSource).WithSession(request.Session)
	if err != nil {
		return result, err
	}
	result.RuntimeState, result.RuntimeReason = fleetHerdrSessionState(ctx, h)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Phase == "check" {
		return result, nil
	}
	if result.RuntimeState != "ready" {
		return result, fmt.Errorf("selected Herdr session is not ready: %s", result.RuntimeReason)
	}
	// Native readiness probes may have taken time. Bind opening to a second
	// fresh path/Git identity observation immediately before the native call.
	recheck := request
	recheck.ExpectedIdentity = identity
	repository, _, err = inspectFleetHerdrRepository(ctx, app, recheck)
	if err != nil {
		return result, err
	}
	opened, err := h.OpenWorktree(ctx, repository.Path, repository.Name)
	if err != nil {
		return result, err
	}
	result.Workspace, result.Surface, result.Created = opened.Handle, opened.Surface, opened.Created
	if err := h.VerifyWorkspace(ctx, opened.Handle, repository.Path); err != nil {
		return result, fmt.Errorf("workspace %s in Herdr session %s could not be verified; retained for inspection: %w", opened.Handle, request.Session, err)
	}
	return result, nil
}

func fleetHerdrSessionState(ctx context.Context, h *runtime.Herdr) (string, string) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		return "unavailable", "unsupported-platform"
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		return "needs-server", "herdr-not-installed"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := h.CheckSession(ctx); err != nil {
		return "needs-server", "session-unavailable"
	}
	return "ready", ""
}

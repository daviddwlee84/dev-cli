package localfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

var (
	ErrUnsafe     = errors.New("portable file path is unsafe")
	ErrIneligible = errors.New("portable file is not untracked and ignored")
	ErrDrift      = errors.New("portable file checkout state changed")
)

type observation struct {
	exists bool
	info   fs.FileInfo
	data   []byte
	digest string
}

func observePath(ctx context.Context, checkout, slashPath string, limits safefile.Limits, read bool) (observation, error) {
	if err := pathx.ValidatePortableSlashPath(slashPath, limits.PathLimits()); err != nil {
		return observation{}, fmt.Errorf("path %q: %w: %w", slashPath, err, ErrUnsafe)
	}
	if hasDependencyComponent(slashPath) {
		return observation{}, fmt.Errorf("path %q enters a dependency directory: %w", slashPath, ErrUnsafe)
	}
	held, err := openHeldParent(checkout, slashPath, false)
	if errors.Is(err, fs.ErrNotExist) {
		return observation{exists: false}, nil
	}
	if err != nil {
		return observation{}, fmt.Errorf("open path %q: %w: %w", slashPath, err, ErrUnsafe)
	}
	finish := func(result observation, operationErr error) (observation, error) {
		closeErr := held.close()
		if operationErr != nil {
			return observation{}, errors.Join(operationErr, closeErr)
		}
		if closeErr != nil {
			return observation{}, fmt.Errorf("verify held path %q: %w", slashPath, closeErr)
		}
		return result, nil
	}
	name := pathBase(slashPath)
	info, err := held.parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return finish(observation{exists: false}, nil)
	}
	if err != nil {
		return finish(observation{}, fmt.Errorf("inspect path %q: %w", slashPath, err))
	}
	if !info.Mode().IsRegular() {
		return finish(observation{}, fmt.Errorf("path %q is not a regular file: %w", slashPath, ErrUnsafe))
	}
	result := observation{exists: true, info: info}
	if read {
		data, stable, err := safefile.ReadStableRegular(ctx, held.parent, name, info, limits.MaxFileBytes)
		if err != nil {
			return finish(observation{}, fmt.Errorf("read path %q: %w", slashPath, err))
		}
		result.info, result.data, result.digest = stable, data, digestBytes(data)
	}
	return finish(result, nil)
}

func proveGitEligibility(ctx context.Context, checkout, slashPath string) error {
	if err := pathx.ValidatePortableSlashPath(slashPath, safefile.DefaultLimits().PathLimits()); err != nil {
		return fmt.Errorf("path %q: %w", slashPath, ErrUnsafe)
	}
	literal := ":(literal)" + slashPath
	tracked, err := gitx.Run(ctx, checkout, "ls-files", "-z", "--", literal)
	if err != nil {
		return fmt.Errorf("inspect Git tracking for %q: %w", slashPath, err)
	}
	if tracked != "" {
		return fmt.Errorf("path %q is tracked: %w", slashPath, ErrIneligible)
	}
	components := strings.Split(slashPath, "/")
	for index := range components {
		prefix := strings.Join(components[:index+1], "/")
		staged, err := gitx.Run(ctx, checkout, "ls-files", "--stage", "-z", "--", ":(literal)"+prefix)
		if err != nil {
			return fmt.Errorf("inspect Git mode for %q: %w", slashPath, err)
		}
		for _, record := range strings.Split(staged, "\x00") {
			if strings.HasPrefix(record, "160000 ") {
				return fmt.Errorf("path %q crosses a submodule: %w", slashPath, ErrUnsafe)
			}
		}
	}
	_, err = gitx.Run(ctx, checkout, "check-ignore", "--no-index", "--quiet", "--", slashPath)
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return fmt.Errorf("path %q is not ignored on this host: %w", slashPath, ErrIneligible)
	}
	return fmt.Errorf("inspect Git ignore status for %q: %w", slashPath, err)
}

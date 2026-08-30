package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type repoCheckIn string

const (
	repoCheckInAuto   repoCheckIn = "auto"
	repoCheckInCommit repoCheckIn = "commit"
	repoCheckInStage  repoCheckIn = "stage"
	repoCheckInNone   repoCheckIn = "none"
)

func parseRepoCheckIn(value string) (repoCheckIn, error) {
	mode := repoCheckIn(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case "", repoCheckInAuto:
		return repoCheckInAuto, nil
	case repoCheckInCommit, repoCheckInStage, repoCheckInNone:
		return mode, nil
	default:
		return "", fmt.Errorf("check-in %q: want auto, commit, stage, or none", value)
	}
}

func resolveRepoCheckIn(flags repoBootstrapFlags, prepared preparedRepoScaffold, kind repo.AcquireKind) (repoCheckIn, error) {
	mode, err := parseRepoCheckIn(flags.checkIn)
	if err != nil {
		return "", err
	}
	if flags.commit {
		if mode != repoCheckInAuto && mode != repoCheckInCommit {
			return "", fmt.Errorf("--commit conflicts with --check-in=%s", mode)
		}
		return repoCheckInCommit, nil
	}
	if mode != repoCheckInAuto {
		return mode, nil
	}
	if kind != repo.AcquireNew {
		return repoCheckInNone, nil
	}
	if configured := strings.TrimSpace(prepared.Plan.Settings.InitialCheckIn); configured != "" {
		return parseRepoCheckIn(configured)
	}
	if prepared.Plan.Settings.InitialCommit == nil || *prepared.Plan.Settings.InitialCommit {
		return repoCheckInCommit, nil
	}
	return repoCheckInNone, nil
}

func repoCommitMessage(request repoWorkflowRequest) string {
	message := strings.TrimSpace(request.CommitMessage)
	if message == "" {
		message = strings.TrimSpace(request.Prepared.Plan.Settings.CommitMessage)
	}
	if message == "" {
		message = "chore: initial commit"
	}
	return message
}

func stageRepoForReview(ctx context.Context, root, message string) (int, string, string, error) {
	if _, err := gitx.Run(ctx, root, "add", "-A"); err != nil {
		return 0, "", "", err
	}
	status, err := gitx.StatusOf(ctx, root)
	if err != nil {
		return 0, "", "", err
	}
	if status.Staged == 0 {
		return 0, "", "", nil
	}
	provider, warning, err := seedLazygitPendingCommit(ctx, root, message)
	if err != nil {
		return status.Staged, "", "changes were staged, but the lazygit commit draft could not be prefilled; enter the suggested message manually: " + err.Error(), nil
	}
	return status.Staged, provider, warning, nil
}

func seedLazygitPendingCommit(ctx context.Context, root, message string) (string, string, error) {
	repository, err := gitx.Discover(ctx, root)
	if err != nil {
		return "", "", err
	}
	gitDir, err := pathx.Canonical(repository.GitDir)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(gitDir, "LAZYGIT_PENDING_COMMIT")
	if _, err := pathx.CanonicalChild(gitDir, path); err != nil {
		return "", "", err
	}
	body := strings.TrimSpace(message) + "\n"
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("lazygit pending commit path is not a regular file")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", "", readErr
		}
		if string(existing) == body {
			return "lazygit", "", nil
		}
		return "", "existing lazygit commit draft was preserved; enter the suggested message manually", nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", "", statErr
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(body); err != nil {
		return "", "", err
	}
	if err := file.Sync(); err != nil {
		return "", "", err
	}
	if err := file.Close(); err != nil {
		return "", "", err
	}
	remove = false
	return "lazygit", "", nil
}

func repoCheckInCompletions() []string {
	return []string{string(repoCheckInAuto), string(repoCheckInCommit), string(repoCheckInStage), string(repoCheckInNone)}
}

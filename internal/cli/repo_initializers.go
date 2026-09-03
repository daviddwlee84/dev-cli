package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/claudeplan"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/ignore"
	"github.com/daviddwlee84/dev-cli/internal/licenses"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

type repoInitSelection struct {
	Name          string
	Description   string
	README        bool
	Gitignore     []string
	License       string
	LicenseHolder string
	ClaudePlans   bool
	ImportOrphans bool
	AgentContract bool
}

type repoInitResult struct {
	Touched  []string          `json:"touched,omitempty"`
	Skipped  []string          `json:"skipped,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Plans    claudeplan.Result `json:"plans,omitempty"`
}

func applyRepoInitializers(ctx context.Context, root string, selection repoInitSelection) (repoInitResult, error) {
	var result repoInitResult
	touch := func(path string) { result.Touched = append(result.Touched, relativeDisplay(root, path)) }
	skip := func(path string) { result.Skipped = append(result.Skipped, relativeDisplay(root, path)) }

	if selection.README {
		path := filepath.Join(root, "README.md")
		if _, err := os.Lstat(path); err == nil {
			skip(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		} else {
			body := "# " + selection.Name + "\n"
			if strings.TrimSpace(selection.Description) != "" {
				body += "\n" + strings.TrimSpace(selection.Description) + "\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return result, err
			}
			touch(path)
		}
	}

	if selection.AgentContract {
		path := filepath.Join(root, "AGENTS.md")
		if _, err := os.Lstat(path); err == nil {
			skip(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		} else {
			body := scaffold.StarterAgentContract()
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return result, err
			}
			touch(path)
		}
	}

	if selection.Gitignore != nil {
		changed, warning, err := applyRepoGitignore(ctx, root, selection.Gitignore)
		if err != nil {
			return result, err
		}
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		if changed {
			touch(filepath.Join(root, ".gitignore"))
		} else {
			skip(filepath.Join(root, ".gitignore"))
		}
	}

	if key := strings.TrimSpace(selection.License); key != "" && !strings.EqualFold(key, "none") {
		path := filepath.Join(root, "LICENSE")
		if _, err := os.Lstat(path); err == nil {
			skip(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		} else {
			fetcher := licenses.NewFetcher(filepath.Join(config.CacheHome(), "dev", "licenses"))
			license, err := fetcher.Get(ctx, key)
			if err != nil {
				return result, err
			}
			body := licenses.Render(license.Body, selection.LicenseHolder, time.Now().Year())
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return result, err
			}
			touch(path)
		}
	}

	if selection.ClaudePlans {
		plans, err := claudeplan.Setup(ctx, root, claudeplan.Options{ImportOrphans: selection.ImportOrphans})
		if err != nil {
			return result, err
		}
		result.Plans = plans
		if plans.SettingsMade || plans.SettingsEdit {
			touch(plans.SettingsPath)
		} else {
			skip(plans.SettingsPath)
		}
		for _, imported := range plans.Imported {
			touch(imported)
		}
	}
	return result, nil
}

func applyRepoGitignore(ctx context.Context, root string, names []string) (bool, string, error) {
	fetcher := ignore.NewFetcher(filepath.Join(config.CacheHome(), "dev", "gitignore"))
	var sections []ignore.Section
	var missing []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.EqualFold(name, "common") {
			continue
		}
		section, err := fetcher.Get(ctx, name)
		if err != nil {
			missing = append(missing, err.Error())
			continue
		}
		sections = append(sections, section)
	}
	block := ignore.Compose(sections, ignore.DefaultExtras())
	path, existing, mode, err := readRepoGitignore(root)
	if err != nil {
		return false, "", err
	}
	updated := ignore.Merge(string(existing), block)
	if updated == string(existing) {
		return false, strings.Join(missing, "; "), nil
	}
	_, err = scaffold.ApplyFiles(scaffold.Plan{
		Root: root,
		Files: []scaffold.FilePlan{{
			ID: "gitignore", RelativePath: ".gitignore", Path: path,
			Content: updated, Mode: mode,
		}},
	}, scaffold.ExistingOverwrite)
	if err != nil {
		return false, "", err
	}
	return true, strings.Join(missing, "; "), nil
}

func readRepoGitignore(root string) (string, []byte, fs.FileMode, error) {
	canonicalRoot, err := pathx.Canonical(root)
	if err != nil {
		return "", nil, 0, fmt.Errorf("canonicalize repository root: %w", err)
	}
	candidate := filepath.Join(canonicalRoot, ".gitignore")
	path, err := pathx.CanonicalChild(canonicalRoot, candidate)
	if err != nil {
		return "", nil, 0, fmt.Errorf("validate .gitignore destination: %w", err)
	}
	info, err := os.Lstat(candidate)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil, 0o644, nil
	}
	if err != nil {
		return "", nil, 0, fmt.Errorf("inspect .gitignore destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, 0, fmt.Errorf(".gitignore destination is a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", nil, 0, fmt.Errorf(".gitignore destination is not a regular file")
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return "", nil, 0, fmt.Errorf("read .gitignore destination: %w", err)
	}
	return path, existing, info.Mode().Perm(), nil
}

func relativeDisplay(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return config.Contract(path)
	}
	return filepath.ToSlash(relative)
}

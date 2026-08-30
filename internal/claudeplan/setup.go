// Package claudeplan configures repository-local Claude Code plans and can
// recover plan files previously written to the user-global plans directory.
package claudeplan

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const PlansDirectory = "./.claude/plans"

type Result struct {
	SettingsPath string   `json:"settings_path"`
	PlansPath    string   `json:"plans_path"`
	SettingsMade bool     `json:"settings_created"`
	SettingsEdit bool     `json:"settings_updated"`
	Imported     []string `json:"imported,omitempty"`
}

type Options struct {
	Home          string
	ImportOrphans bool
}

func Setup(ctx context.Context, repoRoot string, options Options) (Result, error) {
	_ = ctx
	root, err := pathx.Canonical(repoRoot)
	if err != nil {
		return Result{}, err
	}
	plansPath, err := pathx.CanonicalChild(root, filepath.Join(root, ".claude", "plans"))
	if err != nil {
		return Result{}, err
	}
	settingsPath, err := pathx.CanonicalChild(root, filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		return Result{}, err
	}
	result := Result{SettingsPath: settingsPath, PlansPath: plansPath}
	if err := os.MkdirAll(plansPath, 0o755); err != nil {
		return result, fmt.Errorf("create Claude plans directory: %w", err)
	}

	settings := map[string]any{}
	existing, readErr := os.ReadFile(settingsPath)
	switch {
	case readErr == nil:
		if err := json.Unmarshal(existing, &settings); err != nil {
			return result, fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	case errors.Is(readErr, fs.ErrNotExist):
		result.SettingsMade = true
	default:
		return result, readErr
	}
	if current, _ := settings["plansDirectory"].(string); current != PlansDirectory {
		settings["plansDirectory"] = PlansDirectory
		result.SettingsEdit = !result.SettingsMade
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, err
	}
	encoded = append(encoded, '\n')
	if readErr != nil || string(existing) != string(encoded) {
		if err := atomicWrite(settingsPath, encoded, 0o644); err != nil {
			return result, err
		}
	}

	if options.ImportOrphans {
		home := options.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		result.Imported, err = importOrphans(root, home, plansPath)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func atomicWrite(path string, body []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	// Windows does not replace an existing destination with Rename. Preserve
	// the old file until the new one is safely in place, and roll back if the
	// second rename fails.
	if _, err := os.Lstat(path); err != nil {
		return os.Rename(temporaryPath, path)
	}
	backup, err := os.CreateTemp(filepath.Dir(path), ".settings-backup-*.tmp")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(path, backupPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		return err
	}
	return os.Remove(backupPath)
}

func importOrphans(repoRoot, home, localPlans string) ([]string, error) {
	globalPlans := filepath.Join(home, ".claude", "plans")
	if info, err := os.Stat(globalPlans); err != nil || !info.IsDir() {
		return nil, nil
	}
	projectDir := filepath.Join(home, ".claude", "projects", encodeProjectPath(repoRoot))
	entries, err := os.ReadDir(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	candidates := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if err := scanJSONL(filepath.Join(projectDir, entry.Name()), globalPlans, candidates); err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var imported []string
	for _, source := range paths {
		inside, err := pathx.Contains(globalPlans, source)
		if err != nil || !inside {
			continue
		}
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		destination := filepath.Join(localPlans, filepath.Base(source))
		if _, err := os.Lstat(destination); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return imported, err
		}
		if err := copyExclusive(source, destination, info.Mode().Perm()); err != nil {
			return imported, err
		}
		imported = append(imported, destination)
	}
	return imported, nil
}

func encodeProjectPath(path string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(path)
}

func scanJSONL(path, globalPlans string, found map[string]bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var value any
		if json.Unmarshal(scanner.Bytes(), &value) != nil {
			continue
		}
		collectAuthoritativePaths(value, globalPlans, found)
	}
	return scanner.Err()
}

func collectAuthoritativePaths(value any, globalPlans string, found map[string]bool) {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			collectAuthoritativePaths(child, globalPlans, found)
		}
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "file_path", "filePath", "planFilePath":
				if path, ok := child.(string); ok && filepath.IsAbs(path) {
					if inside, _ := pathx.Contains(globalPlans, path); inside {
						found[filepath.Clean(path)] = true
					}
				}
			default:
				collectAuthoritativePaths(child, globalPlans, found)
			}
		}
	}
}

func copyExclusive(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

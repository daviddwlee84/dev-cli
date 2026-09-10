package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/skill"
)

func bundledSkillDoctorCheck() check {
	status, err := skill.Check(skill.DefaultDir())
	switch {
	case err != nil:
		return check{"bundled skill", checkWarn, "cannot inspect installed dev-cli skill: " + err.Error()}
	case !status.Installed:
		return check{"bundled skill", checkOK, "not installed — optional; dev skill install installs it"}
	case len(status.Modified) > 0:
		return check{"bundled skill", checkWarn, "local edits detected; preserve them before dev skill install"}
	case !status.Current:
		return check{"bundled skill", checkWarn, "differs from this binary — run dev skill install to refresh"}
	default:
		return check{"bundled skill", checkOK, "installed content matches this binary"}
	}
}

// Refresh must run in the new process: this process still embeds the old skill
// after its executable was replaced. Never select an unrelated dev from PATH.
func refreshSkillAfterUpgrade(ctx context.Context, app *App, install detectedInstall) error {
	status, err := skill.Check(skill.DefaultDir())
	if err != nil {
		return fmt.Errorf("binary update completed, but bundled skill inspection failed: %w", err)
	}
	if !status.Installed {
		return nil
	}
	executable, err := upgradedExecutable(install)
	if err != nil {
		return fmt.Errorf("binary update completed, but bundled skill refresh needs dev skill install: %w", err)
	}
	process := exec.CommandContext(ctx, executable, "skill", "install", "--if-installed")
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	if err := process.Run(); err != nil {
		return fmt.Errorf("binary update completed, but bundled skill refresh failed; run %s skill install after preserving local edits: %w", executable, err)
	}
	return nil
}

func upgradedExecutable(install detectedInstall) (string, error) {
	path := filepath.ToSlash(install.Resolved)
	switch install.Method {
	case methodHomebrew:
		before, _, ok := strings.Cut(path, "/Cellar/dev-cli/")
		if !ok {
			return "", fmt.Errorf("cannot locate stable Homebrew path for %s", install.Resolved)
		}
		return filepath.FromSlash(before + "/opt/dev-cli/bin/dev"), nil
	case methodScoop:
		index := strings.Index(strings.ToLower(path), "/scoop/apps/dev-cli/")
		if index < 0 {
			return "", fmt.Errorf("cannot locate stable Scoop path for %s", install.Resolved)
		}
		return filepath.FromSlash(path[:index] + "/scoop/apps/dev-cli/current/dev.exe"), nil
	case methodStandalone, methodGo:
		if !filepath.IsAbs(install.Resolved) {
			return "", fmt.Errorf("updated executable path is not absolute")
		}
		if info, err := os.Stat(install.Resolved); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("updated executable is unavailable at %s", install.Resolved)
		}
		return install.Resolved, nil
	default:
		return "", fmt.Errorf("unknown installation method")
	}
}

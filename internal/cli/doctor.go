package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/spf13/cobra"
)

type checkStatus int

const (
	checkOK checkStatus = iota
	checkWarn
	checkFail
)

func (s checkStatus) icon() string {
	switch s {
	case checkOK:
		return "✓"
	case checkWarn:
		return "!"
	}
	return "✗"
}

type check struct {
	name   string
	status checkStatus
	detail string
}

func newDoctorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check dependencies, paths and runtime backends",
		Long: `Report what dev can and cannot do on this machine.

Only git is required. Everything else — a multiplexer, a forge CLI — enables
extra behaviour and degrades cleanly when missing, so a warning here is not a
problem to fix unless you want that capability.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(app)
		},
	}
}

func runDoctor(app *App) error {
	ctx := ctxOf()
	var checks []check

	// Required.
	if v, err := exec.CommandContext(ctx, "git", "--version").Output(); err == nil {
		checks = append(checks, check{"git", checkOK, strings.TrimSpace(string(v))})
	} else {
		checks = append(checks, check{"git", checkFail, "not found — dev cannot work without it"})
	}

	// Runtime backends.
	active := app.Runtime().Name()
	for _, rt := range runtime.All() {
		name := rt.Name()
		detail := "not available"
		st := checkWarn
		if rt.Available() {
			st, detail = checkOK, "available"
			if sessions, err := rt.List(ctx); err == nil && len(sessions) > 0 {
				detail = fmt.Sprintf("available, %d live session(s)", len(sessions))
			}
		}
		if name == "none" {
			st, detail = checkOK, "always available (prints a cd directive)"
		}
		if name == active {
			detail += "  ← selected"
		}
		checks = append(checks, check{"runtime/" + name, st, detail})
	}

	// Optional forge CLIs.
	for _, f := range []struct{ bin, purpose string }{
		{"gh", "GitHub: clone, create, PRs"},
		{"glab", "GitLab: clone, create, MRs"},
	} {
		if path, err := exec.LookPath(f.bin); err == nil {
			checks = append(checks, check{f.bin, checkOK, path + " — " + f.purpose})
		} else {
			checks = append(checks, check{f.bin, checkWarn, "not found — " + f.purpose + " unavailable"})
		}
	}
	azureTargets := len(app.Cfg.Forge.AzureDevOps)
	if path, err := exec.LookPath("az"); err != nil {
		detail := "not found — Azure DevOps PRs unavailable"
		if azureTargets > 0 {
			detail += fmt.Sprintf("; %d configured inventory target(s) skipped", azureTargets)
		}
		checks = append(checks, check{"az", checkWarn, detail})
	} else if err := forge.CheckAzureDevOps(ctx); err != nil {
		checks = append(checks, check{"az", checkWarn, path + " — " + err.Error()})
	} else {
		detail := path + " — Azure DevOps: PRs"
		if azureTargets == 0 {
			detail += "; inventory disabled (no forge.azure_devops targets)"
		} else {
			detail += fmt.Sprintf("; %d inventory target(s)", azureTargets)
		}
		checks = append(checks, check{"az", checkOK, detail})
	}

	// Shell integration: without the wrapper, `dev resume` prints a path
	// instead of moving the user, which looks broken if unexplained.
	if os.Getenv("DEV_SHELL_INIT") == "1" {
		checks = append(checks, check{"shell wrapper", checkOK, "active"})
	} else {
		checks = append(checks, check{"shell wrapper", checkWarn,
			`not detected — add: eval "$(dev shell-init zsh)"`})
	}

	// Paths.
	cfgSrc := app.Cfg.Source
	if cfgSrc == "" {
		cfgSrc = "built-in defaults (no config.toml)"
	}
	checks = append(checks, check{"config", checkOK, cfgSrc})
	stateStatus, stateDetail := pathCheck(app.Cfg.StateDir())
	checks = append(checks, check{"state dir", stateStatus, stateDetail})

	for _, root := range app.Cfg.ScanRoots() {
		st, detail := pathCheck(root)
		if st != checkOK {
			detail = "missing — no repos will be discovered here"
			st = checkWarn
		}
		checks = append(checks, check{"scan root", st, config.Contract(root) + " — " + detail})
	}

	wtRoot := config.Expand(app.Cfg.Paths.WorktreeRoot)
	sample, err := app.Cfg.WorktreePathFor("example", "/repo/example", "feat/demo", "")
	if err != nil {
		checks = append(checks, check{"worktree path", checkFail, err.Error()})
	} else {
		checks = append(checks, check{"worktree path", checkOK,
			config.Contract(sample) + fmt.Sprintf("  (root %s, %s)", config.Contract(wtRoot), writableNote(wtRoot))})
	}

	// Render.
	failures := 0
	style := app.outStyle()
	for _, c := range checks {
		if c.status == checkFail {
			failures++
		}
		icon := c.status.icon()
		detail := c.detail
		switch c.status {
		case checkOK:
			icon = style.success(icon)
		case checkWarn:
			icon, detail = style.warning(icon), style.warning(detail)
		case checkFail:
			icon, detail = style.danger(icon), style.danger(detail)
		}
		fmt.Fprintf(app.Out, "%s %s %s\n", icon, style.label(fmt.Sprintf("%-16s", c.name)), detail)
	}
	if failures > 0 {
		return fmt.Errorf("%d required check(s) failed", failures)
	}
	return nil
}

func pathCheck(p string) (checkStatus, string) {
	info, err := os.Stat(p)
	switch {
	case err == nil && info.IsDir():
		return checkOK, config.Contract(p)
	case os.IsNotExist(err):
		return checkWarn, config.Contract(p) + " (will be created on first use)"
	case err != nil:
		return checkFail, err.Error()
	default:
		return checkFail, config.Contract(p) + " is not a directory"
	}
}

// writableNote reports whether dev could actually create a worktree under the
// configured root, walking up to the nearest existing ancestor. A root on an
// unmounted volume or a read-only mount is the failure this catches early,
// before a `dev wt create` gets halfway through.
func writableNote(root string) string {
	dir := root
	for {
		if info, err := os.Stat(dir); err == nil {
			if !info.IsDir() {
				return "not a directory"
			}
			if dirWritable(dir) {
				return "writable"
			}
			return "NOT WRITABLE"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "unreachable"
		}
		dir = parent
	}
}

// dirWritable probes by creating and removing a temp file. There is no
// portable stdlib permission check, and the mode bits alone would lie about
// read-only mounts and ACLs.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".dev-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

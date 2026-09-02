package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
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

Only git is required. Everything else — OpenSSH, a multiplexer, a forge CLI —
enables extra behaviour and degrades cleanly when missing, so a warning here is
not a problem to fix unless you want that capability.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(app)
		},
	}
}

func runDoctor(app *App) error {
	ctx := ctxOf()
	var checks []check

	// What am I running? A build several commits past its last release behaves
	// differently from that release, and `dev --version` alone does not say so.
	checks = append(checks, check{"dev", checkOK, versionSummary()})
	if install, err := runningInstall(); err == nil {
		detail := install.Method.label() + " — " + install.Path
		if install.Resolved != install.Path {
			detail += " → " + install.Resolved
		}
		checks = append(checks, check{"install", checkOK, detail})
		if others := otherDevInstalls(install, devInstallsOnPath()); len(others) > 0 {
			var copies []string
			for _, other := range others {
				copies = append(copies, other.Method.label()+" at "+other.Path)
			}
			checks = append(checks, check{"dev copies", checkWarn,
				"other executable(s) on PATH may shadow this install: " + strings.Join(copies, "; ")})
		}
	} else {
		checks = append(checks, check{"install", checkWarn, "cannot locate the running executable: " + err.Error()})
	}

	// Windows has no tmux/Zellij/Herdr, so a warning against those backends
	// below is expected rather than something to fix.
	if goruntime.GOOS == "windows" {
		checks = append(checks, check{"platform", checkWarn,
			"windows — tmux, Zellij and Herdr are unavailable; dev uses the no-multiplexer backend"})
	}

	// Required.
	if v, err := exec.CommandContext(ctx, "git", "--version").Output(); err == nil {
		checks = append(checks, check{"git", checkOK, strings.TrimSpace(string(v))})
	} else {
		checks = append(checks, check{"git", checkFail, "not found — dev cannot work without it"})
	}

	// Optional OpenSSH capabilities and static local ownership checks. These use
	// LookPath, bounded file reads, and source-bound planning only: doctor never
	// runs ssh -G, contacts a host, or repairs configuration.
	checks = append(checks, sshDoctorChecks(app)...)

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
	if cwd, err := os.Getwd(); err == nil {
		if version, err := agentskill.ProviderVersion(ctx, cwd); err == nil {
			checks = append(checks, check{"agent skills", checkOK, version})
		} else {
			checks = append(checks, check{"agent skills", checkWarn,
				"provider unavailable without downloading — `dev skill add` bootstraps it explicitly"})
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
		hint := `not detected — add: eval "$(dev shell-init zsh)"`
		if goruntime.GOOS == "windows" {
			hint = "not detected — add to your PowerShell profile: " +
				"Invoke-Expression (& dev shell-init powershell | Out-String)"
		}
		checks = append(checks, check{"shell wrapper", checkWarn, hint})
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
	for _, path := range app.Cfg.RepoPaths() {
		st, detail := pathCheck(path)
		if st != checkOK {
			detail = "missing — this exact repository will be skipped"
			st = checkWarn
		}
		checks = append(checks, check{"repo path", st, config.Contract(path) + " — " + detail})
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

func sshDoctorChecks(app *App) []check {
	checks := make([]check, 0, 5)
	for _, binary := range []struct {
		name    string
		purpose string
	}{
		{name: "ssh", purpose: "SSH host evaluation and login unavailable"},
		{name: "ssh-keygen", purpose: "SSH key generation and derivation unavailable"},
	} {
		if path, err := exec.LookPath(binary.name); err == nil {
			checks = append(checks, check{binary.name, checkOK, path})
		} else {
			checks = append(checks, check{binary.name, checkWarn, "not found — " + binary.purpose})
		}
	}

	service, err := app.sshHosts()
	if err != nil {
		checks = append(checks, check{"ssh config", checkWarn, "cannot derive ~/.ssh paths: " + err.Error()})
		return checks
	}
	inventory, discoverErr := service.Discover(ctxOf())
	if discoverErr != nil {
		checks = append(checks, check{"ssh config", checkWarn, "static discovery failed: " + discoverErr.Error()})
	} else {
		status := checkOK
		detail := fmt.Sprintf("%d exact alias(es); static Include closure complete", len(inventory.Aliases))
		switch {
		case inventory.RootMissing:
			status = checkWarn
			detail = config.Contract(inventory.Root) + " is absent; run dev ssh init --apply before managing aliases"
		case !inventory.Complete:
			status = checkWarn
			detail = fmt.Sprintf("%d exact alias(es); Include closure incomplete", len(inventory.Aliases))
		}
		checks = append(checks, check{"ssh config", status, detail})
		if inventory.ManagedIncludeActive {
			checks = append(checks, check{"ssh managed", checkOK, "Include " + sshhost.ManagedInclude + " is statically active"})
		} else {
			checks = append(checks, check{"ssh managed", checkWarn, "managed Include is inactive; dev ssh init reports the exact change"})
		}
	}
	if plan, planErr := service.PlanInit(ctxOf()); planErr != nil {
		checks = append(checks, check{"ssh security", checkWarn, "read-only security check failed: " + planErr.Error()})
	} else if plan.Action == sshhost.ActionBlocked {
		detail := "SSH root or managed namespace needs manual security remediation"
		if len(plan.Diagnostics) > 0 {
			detail += " (" + plan.Diagnostics[0].Code + ")"
		}
		checks = append(checks, check{"ssh security", checkWarn, detail})
	} else {
		checks = append(checks, check{"ssh security", checkOK, "root and managed namespace pass read-only ownership/permission checks"})
	}

	managedDir := fleet.ManagedFragmentDir(fleetConfigPath(app))
	if _, statErr := os.Lstat(managedDir); statErr == nil {
		cfg, loadErr := loadFleetConfig(app)
		if loadErr != nil {
			checks = append(checks, check{"fleet fragments", checkWarn, loadErr.Error()})
		} else {
			managed := 0
			for _, host := range cfg.Hosts {
				if host.Managed() {
					managed++
				}
			}
			checks = append(checks, check{"fleet fragments", checkOK,
				fmt.Sprintf("%d generated host fragment(s) validated under %s", managed, config.Contract(managedDir))})
		}
	} else if !os.IsNotExist(statErr) {
		checks = append(checks, check{"fleet fragments", checkWarn, "cannot inspect " + config.Contract(managedDir) + ": " + statErr.Error()})
	}
	return checks
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

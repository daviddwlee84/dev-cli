package tuiissue

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Tool struct {
	Guide       string
	Brew        string
	Apt         string
	WinGet      string
	VersionArgs []string
}

// Recipes use native package identities; absent recipes deliberately lead to
// official guidance instead of adding repositories or installing a manager.
var Tools = map[string]Tool{
	"ssh-keygen": {Guide: "https://www.openssh.com/portable.html", Brew: "openssh", Apt: "openssh-client"},
	"ping":       {Guide: "https://github.com/iputils/iputils", Apt: "iputils-ping", VersionArgs: []string{"-V"}},
	"npm":        {Guide: "https://docs.npmjs.com/downloading-and-installing-node-js-and-npm", Brew: "node", Apt: "npm", WinGet: "OpenJS.NodeJS.LTS", VersionArgs: []string{"--version"}},
	"npx":        {Guide: "https://docs.npmjs.com/downloading-and-installing-node-js-and-npm", Brew: "node", Apt: "npm", WinGet: "OpenJS.NodeJS.LTS", VersionArgs: []string{"--version"}},
	"ssh":        {Guide: "https://www.openssh.com/portable.html", Brew: "openssh", Apt: "openssh-client", VersionArgs: []string{"-V"}},
	"bw":         {Guide: "https://bitwarden.com/help/cli/", Brew: "bitwarden-cli", WinGet: "Bitwarden.CLI", VersionArgs: []string{"--version"}},
	"op":         {Guide: "https://developer.1password.com/docs/cli/get-started/", VersionArgs: []string{"--version"}},
	"ykman":      {Guide: "https://docs.yubico.com/software/yubikey/tools/ykman/", Brew: "ykman", Apt: "yubikey-manager", VersionArgs: []string{"--version"}},
	"git":        {Guide: "https://git-scm.com/install/", Brew: "git", Apt: "git", WinGet: "Git.Git", VersionArgs: []string{"--version"}},
	"tailscale":  {Guide: "https://tailscale.com/download", Brew: "tailscale", WinGet: "Tailscale.Tailscale", VersionArgs: []string{"version"}},
	"gh":         {Guide: "https://cli.github.com/", Brew: "gh", Apt: "gh", WinGet: "GitHub.cli", VersionArgs: []string{"--version"}},
	"glab":       {Guide: "https://gitlab.com/gitlab-org/cli#installation", Brew: "glab", VersionArgs: []string{"--version"}},
	"az":         {Guide: "https://learn.microsoft.com/cli/azure/install-azure-cli", Brew: "azure-cli", WinGet: "Microsoft.AzureCLI", VersionArgs: []string{"version"}},
	"tmux":       {Guide: "https://github.com/tmux/tmux/wiki/Installing", Brew: "tmux", Apt: "tmux", VersionArgs: []string{"-V"}},
	"zellij":     {Guide: "https://zellij.dev/documentation/installation", Brew: "zellij", VersionArgs: []string{"--version"}},
	"fzf":        {Guide: "https://github.com/junegunn/fzf#installation", Brew: "fzf", Apt: "fzf", WinGet: "junegunn.fzf", VersionArgs: []string{"--version"}},
	"node":       {Guide: "https://nodejs.org/en/download", Brew: "node", Apt: "nodejs", WinGet: "OpenJS.NodeJS.LTS", VersionArgs: []string{"--version"}},
	"herdr":      {Guide: "Use Herdr's official installation instructions and verify `herdr --version` in this terminal.", VersionArgs: []string{"--version"}},
	"codex":      {Guide: "https://developers.openai.com/codex/cli", VersionArgs: []string{"--version"}},
	"claude":     {Guide: "https://code.claude.com/docs/en/setup", VersionArgs: []string{"--version"}},
}

type Environment struct {
	OS           string
	Distribution string
	Root         bool
	LookPath     func(string) (string, error)
}
type InstallPlan struct {
	Tool         string
	Package      string
	Manager      string
	Argv         []string
	Guide        string
	Reason       string
	managerPath  string
	launcherPath string
	environment  Environment
}

func (p InstallPlan) Ready() bool { return len(p.Argv) > 0 && p.managerPath != "" }
func PlanInstall(tool string, env Environment) (InstallPlan, error) {
	spec, ok := Tools[tool]
	if !ok {
		return InstallPlan{}, fmt.Errorf("no reviewed installation recipe for %q", tool)
	}
	p := InstallPlan{Tool: tool, Guide: spec.Guide, environment: env}
	if env.LookPath == nil {
		return p, errors.New("executable inspection unavailable")
	}
	if _, err := env.LookPath(tool); err == nil {
		p.Reason = "Tool is already available; recheck its capabilities."
		return p, nil
	}
	switch env.OS {
	case "darwin":
		p.Manager = "brew"
		p.Package = spec.Brew
		p.Argv = []string{"brew", "install", spec.Brew}
	case "linux":
		if env.Distribution == "debian" || env.Distribution == "ubuntu" {
			p.Manager = "apt-get"
			p.Package = spec.Apt
			p.Argv = []string{"apt-get", "install", spec.Apt}
			if !env.Root {
				p.Argv = append([]string{"sudo"}, p.Argv...)
			}
		}
	case "windows":
		p.Manager = "winget"
		p.Package = spec.WinGet
		p.Argv = []string{"winget", "install", "--id", spec.WinGet, "--exact", "--source", "winget"}
	}
	if p.Package == "" {
		p.Argv = nil
		p.Reason = "No reviewed native package is available for this platform. Follow the official instructions."
		return p, nil
	}
	manager, err := env.LookPath(p.Manager)
	if err != nil {
		p.Argv = nil
		p.Reason = p.Manager + " is missing. Install it separately using its official instructions."
		return p, nil
	}
	p.managerPath = manager
	if p.Argv[0] == "sudo" {
		p.Argv[1] = manager
	}
	p.launcherPath, err = env.LookPath(p.Argv[0])
	if err != nil {
		p.Argv = nil
		p.Reason = "Privilege helper is unavailable. Install the package manually using the official instructions."
	}
	return p, nil
}

// Revalidate refuses an altered preview, a changed manager identity, or an
// executable that appeared while the user reviewed the plan.
func (p InstallPlan) Revalidate() error {
	if !p.Ready() {
		return errors.New("installation plan is not executable")
	}
	fresh, err := PlanInstall(p.Tool, p.environment)
	if err != nil {
		return err
	}
	if !fresh.Ready() || fresh.managerPath != p.managerPath || fresh.launcherPath != p.launcherPath || !slices.Equal(fresh.Argv, p.Argv) || fresh.Package != p.Package {
		return errors.New("installation prerequisites changed; review a fresh plan")
	}
	return nil
}
func (p InstallPlan) Executable() string { return p.launcherPath }

func (p InstallPlan) Preview() string {
	if !p.Ready() {
		return p.Reason + "\n\n" + p.Guide
	}
	displayArgv := append([]string(nil), p.Argv...)
	displayArgv[0] = p.launcherPath
	return fmt.Sprintf("Tool: %s\nPackage: %s\nManager: %s\nCommand: %s\n\nInstalls this package and its package-manager dependencies. Native prompts remain interactive. Service startup and login are separate.\n\n%s", p.Tool, p.Package, p.Manager, strings.Join(displayArgv, " "), p.Guide)
}

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/dotfile"
	"github.com/spf13/cobra"
)

// The dotfile entrypoints need no dev registry, runtime, release check or
// startup cleanup. In particular, their passive status must only observe.
func dotfilePreRun(app *App) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		if app.In == nil {
			app.In = os.Stdin
		}
		if app.Out == nil {
			app.Out = os.Stdout
		}
		if app.Err == nil {
			app.Err = os.Stderr
		}
		return validateColorMode(app.colorMode)
	}
}

func newDotfileCmd(app *App) *cobra.Command {
	var configPath string
	var jsonOutput bool
	show := func(cmd *cobra.Command, _ []string) error {
		return renderDotfileStatus(app.Out, dotfile.Observe(cmd.Context(), dotfile.Options{ConfigPath: configPath}), jsonOutput)
	}
	cmd := &cobra.Command{
		Use: "dotfile", Short: "Inspect and manage local dotfiles through native chezmoi",
		Long: "Inspect local chezmoi setup without running hooks or templates. Native diff, apply and update remain explicit operations owned by chezmoi.",
		Args: cobra.NoArgs, RunE: show, PersistentPreRunE: dotfilePreRun(app),
	}
	cmd.PersistentFlags().StringVar(&configPath, "chezmoi-config", "", "local native chezmoi configuration file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit static status as JSON")
	status := &cobra.Command{Use: "status", Short: "Read configuration and source revision without running chezmoi", Args: cobra.NoArgs, RunE: show}
	status.Flags().BoolVar(&jsonOutput, "json", false, "emit static status as JSON")
	cmd.AddCommand(status, newDotfileSetupCmd(app, &configPath))
	for _, action := range []string{"diff", "apply", "update"} {
		action := action
		use := action + " [targets...]"
		if action == "update" {
			use = "update"
		}
		native := &cobra.Command{
			Use: use, Short: "Run native chezmoi " + action,
			Long: "Run native chezmoi with your terminal streams and configuration. Arguments after -- pass directly to chezmoi; native hooks and scripts may run.",
			RunE: func(cmd *cobra.Command, args []string) error {
				return dotfile.RunNative(cmd.Context(), configPath, action, args, app.In, app.Out, app.Err)
			},
		}
		cmd.AddCommand(native)
	}
	return cmd
}

func renderDotfileStatus(out io.Writer, status dotfile.Status, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}
	installed := "not found"
	if status.Installed {
		installed = "found"
	}
	fmt.Fprintf(out, "Platform: %s\nChezmoi: %s\nConfiguration: %s", status.Platform, installed, status.ConfigState)
	if status.ConfigPath != "" {
		fmt.Fprintf(out, " (%s)", status.ConfigPath)
	}
	fmt.Fprintf(out, "\nSource: %s", status.SourceState)
	if status.SourceDir != "" {
		fmt.Fprintf(out, " (%s)", status.SourceDir)
	}
	fmt.Fprintln(out)
	if status.SourceStateDir != "" && status.SourceStateDir != status.SourceDir {
		fmt.Fprintf(out, "Source state: %s\n", status.SourceStateDir)
	}
	if status.WorkingTree != "" {
		fmt.Fprintf(out, "Working tree: %s\n", status.WorkingTree)
	}
	if status.GitRevision != "" {
		fmt.Fprintf(out, "Revision: %s %s\n", status.GitRevision, status.GitBranch)
	}
	fmt.Fprintln(out, "Deployment drift: unknown (not evaluated)")
	if status.Reason != "" {
		fmt.Fprintf(out, "Observation: %s\n", status.Reason)
	}
	if status.SourceState == "absent" {
		fmt.Fprintln(out, "Use dev dotfile setup to review initialization options.")
		if status.Recommendation != nil {
			fmt.Fprintf(out, "Optional david preset: %s\n", status.Recommendation.RepoURL)
		}
	}
	return nil
}

func newDotfileSetupCmd(app *App, configPath *string) *cobra.Command {
	var repoURL, preset string
	var yes, apply bool
	cmd := &cobra.Command{
		Use: "setup", Short: "Review or initialize a native chezmoi source",
		Long: "Preserve existing sources. Review a custom repository or the optional david platform preset, then run native chezmoi init. Non-interactive invocation reports the plan unless --yes is given. --apply separately opts into applying after initialization.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := dotfile.Options{ConfigPath: *configPath}
			request := dotfile.SetupRequest{RepoURL: repoURL, Preset: preset, Apply: apply}
			plan, err := dotfile.PlanSetup(cmd.Context(), options, request)
			if err != nil {
				if plan.Status.SchemaVersion != 0 {
					_ = renderDotfileStatus(app.Out, plan.Status, false)
				}
				return err
			}
			status := plan.Status
			switch plan.Mode {
			case "existing-source":
				fmt.Fprintf(app.Out, "Existing source preserved: %s\nUse dev dotfile diff, apply or update for native operations.\n", status.SourceDir)
				if !status.Installed {
					fmt.Fprintln(app.Out, "Install chezmoi first: https://www.chezmoi.io/install/")
				}
				return nil
			case "existing-config":
				fmt.Fprintf(app.Out, "Existing configuration preserved: %s\nIts source is missing: %s\nReview the native configuration and restore its original source before using setup.\n", status.ConfigPath, status.SourceDir)
				return nil
			}
			if status.Recommendation != nil && request.RepoURL == "" {
				fmt.Fprintf(app.Out, "Optional david preset for %s: %s\n", status.Platform, status.Recommendation.RepoURL)
			}
			switch plan.Mode {
			case "bootstrap":
				fmt.Fprintf(app.Out, "This experimental platform uses its repository bootstrap instructions: %s\n", status.Recommendation.BootstrapURL)
				fmt.Fprintf(app.Out, "After reviewing and obtaining the repository bootstrap: %s\n", status.Recommendation.Bootstrap)
				fmt.Fprintln(app.Out, "An explicit --repo uses native chezmoi initialization for your own repository.")
				return nil
			case "install-chezmoi":
				fmt.Fprintln(app.Out, "Install chezmoi first: https://www.chezmoi.io/install/")
				if status.Recommendation != nil && request.RepoURL == "" {
					fmt.Fprintf(app.Out, "Platform bootstrap instructions: %s\n", status.Recommendation.BootstrapURL)
				}
				return nil
			}
			if plan.Mode == "choose-repository" && app.interactive() && !yes {
				p := newPrompter(app)
				answer, err := p.line("Repository URL (or david for the optional platform preset)", "")
				if err != nil {
					return err
				}
				request.RepoURL = answer
				if answer == "david" {
					request.RepoURL, request.Preset = "", "david"
				}
				plan, err = dotfile.PlanSetup(cmd.Context(), options, request)
				if err != nil {
					return err
				}
			}
			if plan.Mode == "choose-repository" {
				fmt.Fprintln(app.Out, "Choose dev dotfile setup --repo URL or --preset david; --yes initializes, and --apply also applies.")
				if yes {
					return errors.New("--yes requires an explicit --repo or --preset")
				}
				return nil
			}
			if plan.Mode != "initialize" {
				return errors.New("dotfile configuration changed during setup; review setup again")
			}
			fmt.Fprintf(app.Out, "Initialize with native chezmoi: %s\n", dotfileRepositoryLabel(plan.RepoURL))
			if apply {
				fmt.Fprintln(app.Out, "Then apply dotfiles; repository scripts and package installation may run.")
			}
			if !yes {
				if !app.interactive() {
					fmt.Fprintln(app.Out, "Report only. Pass --yes to initialize this source.")
					return nil
				}
				ok, err := newPrompter(app).confirm("Run this native initialization", false)
				if err != nil || !ok {
					return err
				}
			}
			return dotfile.ApplySetup(cmd.Context(), plan, app.In, app.Out, app.Err)
		},
	}
	cmd.Flags().StringVar(&repoURL, "repo", "", "explicit dotfiles repository URL")
	cmd.Flags().StringVar(&preset, "preset", "", "optional platform preset (david)")
	cmd.Flags().BoolVar(&yes, "yes", false, "run the reviewed native initialization without a dev confirmation")
	cmd.Flags().BoolVar(&apply, "apply", false, "also run native apply after initialization")
	return cmd
}

func dotfileRepositoryLabel(value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		return parsed.String()
	}
	return value
}

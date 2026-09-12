package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Repository setup delegates its explicit artifact path to the same command
// adapter and domain. Scaffold execution and artifact apply are separate plans.
func bindRepoArtifactSetup(app *App, cmd *cobra.Command, flags *repoBootstrapFlags) func(string) (bool, error) {
	history := newArtifactSetupCmd(app)
	var enabled bool
	cmd.Flags().BoolVar(&enabled, "artifacts", false, "preview/apply agent-history policy instead of a scaffold")
	owned := map[string]bool{}
	history.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "repo" || f.Name == "json" || f.Name == "yes" {
			return
		}
		copy := *f
		copy.Shorthand = ""
		copy.Usage = "with --artifacts: " + f.Usage
		cmd.Flags().AddFlag(&copy)
		owned[f.Name] = true
	})
	return func(root string) (bool, error) {
		if !enabled {
			var invalid string
			cmd.Flags().Visit(func(f *pflag.Flag) {
				if owned[f.Name] {
					invalid = f.Name
				}
			})
			if invalid != "" {
				return true, fmt.Errorf("--%s requires --artifacts", invalid)
			}
			return false, nil
		}
		var invalid string
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if !owned[f.Name] && f.Name != "artifacts" && f.Name != "yes" && f.Name != "json" && f.Name != "dry-run" && cmd.InheritedFlags().Lookup(f.Name) == nil {
				invalid = f.Name
			}
		})
		if invalid != "" {
			return true, fmt.Errorf("--artifacts has its own plan; run scaffold option --%s separately", invalid)
		}
		if flags.dryRun && cmd.Flags().Changed("apply") {
			return true, errors.New("--dry-run cannot apply an artifact plan")
		}
		if e := history.Flags().Set("repo", root); e != nil {
			return true, e
		}
		if e := history.Flags().Set("json", strconv.FormatBool(flags.json)); e != nil {
			return true, e
		}
		if e := history.Flags().Set("yes", strconv.FormatBool(flags.yes)); e != nil {
			return true, e
		}
		history.SetContext(cmd.Context())
		return true, history.RunE(history, nil)
	}
}

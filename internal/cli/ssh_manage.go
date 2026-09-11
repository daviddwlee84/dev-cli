package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/spf13/cobra"
)

func (a *App) sshManagement() (sshflow.Service, error) {
	ssh, err := a.sshHosts()
	if err != nil {
		return sshflow.Service{}, err
	}
	return sshflow.Service{SSH: ssh, Herdr: herdrremote.Service{Runner: a.sshHostRunner}, FleetPath: fleetConfigPath(a), RecoveryPath: sshflow.RecoveryPath(config.DataHome())}, nil
}
func newSSHManageCmd(app *App) *cobra.Command {
	var r sshflow.Request
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{Use: "manage", Short: "Compare SSH, fleet and Herdr machines and plan selected operations", Args: cobra.NoArgs,
		Long: "Without an action, show the local joint inventory or open a multi-select wizard in a TTY.\nExplicit actions are plan-only until --apply. Herdr installation approvals remain native.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && !apply {
				return asUsageError(errors.New("--yes requires --apply"))
			}
			service, err := app.sshManagement()
			if err != nil {
				return err
			}
			if r.Action == "" {
				if apply || len(r.Aliases)+len(r.FleetHosts)+len(r.HerdrProfiles) > 0 {
					return asUsageError(errors.New("selectors and --apply require --action"))
				}
				inv, err := service.List(cmd.Context())
				if err != nil {
					return err
				}
				if jsonOut {
					return writeSSHJSON(app, inv)
				}
				renderSSHManagementInventory(app, inv)
				if !app.interactive() {
					return nil
				}
				r, err = sshManagementWizard(cmd.Context(), app, inv)
				if err != nil {
					return err
				}
				apply = true
			}
			plan, err := service.Plan(cmd.Context(), r)
			if err != nil {
				return asUsageError(err)
			}
			if !apply {
				if jsonOut {
					return writeSSHJSON(app, plan)
				}
				renderSSHManagementPlan(app, plan)
				return nil
			}
			if !yes {
				if jsonOut || !app.interactive() {
					return errors.New("--yes is required to apply outside an interactive terminal")
				}
				renderSSHManagementPlan(app, plan)
				confirmed, err := newPrompter(app).confirm("Apply these selected operations?", false)
				if err != nil {
					return err
				}
				if !confirmed {
					return errPromptCanceled
				}
			}
			result, err := service.Apply(cmd.Context(), plan, !jsonOut && app.interactive())
			if jsonOut {
				if e := writeSSHJSON(app, result); e != nil {
					return e
				}
			} else {
				for _, o := range result.Outcomes {
					fmt.Fprintf(app.Out, "%s %s %s: %s\n", o.Target, o.Identity, o.Action, o.Status)
					if o.Receipt != "" {
						fmt.Fprintf(app.Out, "  recovery: dev ssh restore %s\n", o.Receipt)
					}
					if o.Error != "" {
						fmt.Fprintf(app.Err, "  %s\n", o.Error)
					}
				}
			}
			return err
		}}
	f := cmd.Flags()
	f.StringVar(&r.Action, "action", "", "register, probe, rename, remove, enable or disable")
	f.StringVar(&r.To, "to", "", "registration destination: fleet, herdr or both")
	f.StringArrayVar(&r.Aliases, "alias", nil, "exact SSH alias (repeatable)")
	f.StringArrayVar(&r.FleetHosts, "fleet-host", nil, "exact fleet profile name (repeatable)")
	f.StringArrayVar(&r.HerdrProfiles, "herdr-profile", nil, "exact Herdr profile ID (repeatable)")
	f.StringVar(&r.Name, "name", "", "new display name for one selected record")
	f.StringVar(&r.FleetName, "fleet-name", "", "fleet name for one new alias (default: SSH alias)")
	f.StringVar(&r.HerdrLabel, "herdr-label", "", "Herdr label for one new alias (default: SSH alias)")
	f.StringVar(&r.Session, "herdr-session", "default", "Herdr session for registration")
	f.StringVar(&r.RemoteOS, "target-os", "", "fleet target OS: posix or windows")
	f.BoolVar(&apply, "apply", false, "apply the displayed plan")
	f.BoolVar(&yes, "yes", false, "confirm the dev plan; never answer native Herdr approvals")
	f.BoolVar(&jsonOut, "json", false, "emit one versioned inventory, plan or result")
	registerFlagCompletion(cmd, "alias", completeSSHFlagAliases(app))
	registerFlagCompletion(cmd, "action", fixedCompletions("register", "probe", "rename", "remove", "enable", "disable"))
	registerFlagCompletion(cmd, "to", fixedCompletions("fleet", "herdr", "both"))
	registerFlagCompletion(cmd, "target-os", fixedCompletions("posix", "windows"))
	return cmd
}
func renderSSHManagementInventory(app *App, inv sshflow.Inventory) {
	table := app.newTable("KIND", "NAME", "SSH TARGET", "SESSION / OS", "STATE")
	for _, a := range inv.SSH.Aliases {
		table.Add("ssh", a.Name, a.Name, "", aliasStatus(a))
	}
	for _, h := range inv.Fleet {
		table.Add("fleet", h.Name, h.Alias, h.OS, "registered")
	}
	for _, p := range inv.Herdr.Profiles {
		state := "enabled"
		if !p.Enabled {
			state = "disabled"
		}
		table.Add("herdr", p.Label, p.Target, p.Session, state)
	}
	table.Render(app.Out)
	if !inv.SSH.Complete {
		fmt.Fprintln(app.Err, "SSH discovery is incomplete; registration may be blocked.")
	}
	if inv.FleetStatus != "ready" {
		fmt.Fprintln(app.Err, "Fleet configuration is unavailable.")
	}
	if inv.Herdr.Status != "ready" {
		fmt.Fprintln(app.Err, "Herdr machine inventory: "+inv.Herdr.Status)
	}
}
func renderSSHManagementPlan(app *App, p sshflow.Plan) {
	for _, o := range p.Operations {
		fmt.Fprintf(app.Out, "%s %s — %s [%s]\n", o.Target, o.Identity, o.Action, o.Status)
		if o.Reason != "" {
			fmt.Fprintln(app.Out, "  "+o.Reason)
		}
		for _, effect := range o.Effects {
			fmt.Fprintln(app.Out, "  "+effect)
		}
		if o.FleetRegistration != nil {
			h := o.FleetRegistration
			fmt.Fprintf(app.Out, "  fleet name: %s; SSH alias: %s; target OS: %s\n", h.Name, h.SSHAlias, h.RemoteOS)
		}
		if o.Herdr != nil {
			r := o.Herdr.Request
			if r.Label != "" {
				fmt.Fprintf(app.Out, "  label: %s; session: %s\n", r.Label, r.Session)
			}
		}
		if o.Fleet != nil {
			if o.Fleet.NewName != "" {
				fmt.Fprintln(app.Out, "  new name: "+o.Fleet.NewName)
			}
			for _, c := range o.Fleet.Files.Preview() {
				fmt.Fprintf(app.Out, "  %s %s\n", c.Action, config.Contract(c.Path))
			}
		}
	}
}
func sshPick(ctx context.Context, app *App, prompt string, items []picker.Item, multi bool) ([]picker.Item, error) {
	result, attempted, err := app.pick(ctx, picker.Request{Prompt: prompt, Items: items, Multi: multi})
	if err != nil {
		return nil, err
	}
	if !attempted {
		return nil, errors.New("this wizard requires an interactive terminal; use explicit command flags")
	}
	if multi {
		if len(result.Items) == 0 {
			return nil, errPromptCanceled
		}
		return result.Items, nil
	}
	if result.Item.Value == "" {
		return nil, errPromptCanceled
	}
	return []picker.Item{result.Item}, nil
}
func sshManagementWizard(ctx context.Context, app *App, inv sshflow.Inventory) (sshflow.Request, error) {
	var r sshflow.Request
	choices := []picker.Item{{Value: "register", Label: "Register SSH aliases in fleet / Herdr"}, {Value: "probe", Label: "Check fresh SSH login"}, {Value: "rename", Label: "Rename a fleet or Herdr profile"}, {Value: "remove", Label: "Remove fleet / Herdr registrations"}, {Value: "enable", Label: "Enable Herdr machines"}, {Value: "disable", Label: "Disable Herdr machines"}}
	action, err := sshPick(ctx, app, "Machine action", choices, false)
	if err != nil {
		return r, err
	}
	r.Action = action[0].Value
	var items []picker.Item
	if r.Action == "register" || r.Action == "probe" {
		for _, a := range inv.SSH.Aliases {
			description := aliasStatus(a)
			if len(a.Definitions) > 0 {
				d := a.Definitions[0]
				description += " · " + strings.Join(d.Patterns, " ") + " · " + config.Contract(d.Source.Path) + ":" + fmt.Sprint(d.Source.Line)
			}
			for _, h := range inv.Fleet {
				if strings.EqualFold(h.Alias, a.Name) {
					description += " · fleet:" + h.Name
				}
			}
			for _, h := range inv.Herdr.Profiles {
				if h.Target == a.Name {
					description += " · herdr:" + h.Label + "/" + h.Session
				}
			}
			items = append(items, picker.Item{Value: a.Name, Label: a.Name, Description: description})
		}
	} else {
		if r.Action == "rename" || r.Action == "remove" {
			for _, h := range inv.Fleet {
				items = append(items, picker.Item{Value: "fleet:" + h.Name, Label: "fleet · " + h.Name, Description: h.Alias + " · " + config.Contract(h.Source)})
			}
		}
		for _, h := range inv.Herdr.Profiles {
			items = append(items, picker.Item{Value: "herdr:" + h.ID, Label: "herdr · " + h.Label, Description: h.Target + " · " + h.Session + " · " + h.ID})
		}
	}
	selected, err := sshPick(ctx, app, "Select records", items, r.Action != "rename")
	if err != nil {
		return r, err
	}
	for _, x := range selected {
		if strings.HasPrefix(x.Value, "fleet:") {
			r.FleetHosts = append(r.FleetHosts, strings.TrimPrefix(x.Value, "fleet:"))
		} else if strings.HasPrefix(x.Value, "herdr:") {
			r.HerdrProfiles = append(r.HerdrProfiles, strings.TrimPrefix(x.Value, "herdr:"))
		} else {
			r.Aliases = append(r.Aliases, x.Value)
		}
	}
	prompt := newPrompter(app)
	if r.Action == "rename" {
		r.Name, err = prompt.line("New display name", "")
		return r, err
	}
	if r.Action == "register" {
		target, e := sshPick(ctx, app, "Register with", []picker.Item{{Value: "both", Label: "Fleet and Herdr"}, {Value: "fleet", Label: "Fleet"}, {Value: "herdr", Label: "Herdr"}}, false)
		if e != nil {
			return r, e
		}
		r.To = target[0].Value
		r.RemoteOS, e = prompt.choice("Target OS for selected aliases", "posix", "posix, windows", map[string]string{"posix": "posix", "windows": "windows"})
		if e != nil {
			return r, e
		}
		if r.To != "fleet" {
			r.Session, e = prompt.line("Herdr session", "default")
			if e != nil {
				return r, e
			}
		}
		if len(r.Aliases) == 1 {
			if r.To != "herdr" {
				r.FleetName, e = prompt.line("New fleet profile name", r.Aliases[0])
				if e != nil {
					return r, e
				}
			}
			if r.To != "fleet" {
				r.HerdrLabel, e = prompt.line("New Herdr label", r.Aliases[0])
				if e != nil {
					return r, e
				}
			}
		}
	}
	return r, nil
}
func runSSHEntry(cmd *cobra.Command, app *App) error {
	if !app.interactive() {
		return cmd.Help()
	}
	selected, err := sshPick(cmd.Context(), app, "SSH", []picker.Item{{Value: "manage", Label: "Manage SSH / fleet / Herdr machines"}, {Value: "format", Label: "Format SSH configuration", Description: "Four spaces; preview first"}, {Value: "organize", Label: "Organize Host blocks into groups", Description: "Optional; preserve Include order"}}, false)
	if err != nil {
		return err
	}
	sub, _, err := cmd.Find([]string{selected[0].Value})
	if err != nil {
		return err
	}
	if selected[0].Value == "format" {
		return runSSHFormat(cmd.Context(), app, nil, "4", true, false, false)
	}
	// Find does not inherit the execution context when dispatching RunE directly.
	sub.SetContext(cmd.Context())
	return sub.RunE(sub, nil)
}
func expandSSHFile(path string) string { return filepath.Clean(config.Expand(path)) }

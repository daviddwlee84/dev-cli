package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentinterop"
	"github.com/spf13/cobra"
)

func interopService(app *App) agentinterop.Service {
	return agentinterop.Service{StateDir: filepath.Join(app.Cfg.StateDir(), "agent-interop")}
}

func newAgentTransferCmd(app *App, kind string) *cobra.Command {
	cmd := &cobra.Command{Use: "transfer", Short: "Plan and apply explicit agent artifact transfers", Long: "Transfer selected artifacts with a local preview, exact filesystem revalidation, and private recovery records. Planning and applying local transfers never start MCP servers or installers."}
	cmd.AddCommand(newAgentTransferPlanCmd(app, kind))
	if kind == "skill" {
		prepare := newAgentTransferPlanCmd(app, kind)
		prepare.Use = "prepare <skill>"
		prepare.Short = "Fetch and verify one skill in private staging"
		cmd.AddCommand(prepare)
	}
	var id string
	var jsonOut bool
	apply := &cobra.Command{Use: "apply", Short: "Apply one exact reviewed transfer plan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s := interopService(app)
		info, err := s.Inspect(cmd.Context(), id)
		if err != nil {
			return err
		}
		if info.Kind != kind {
			return errors.New("plan belongs to another artifact family")
		}
		r, err := s.Apply(cmd.Context(), id)
		if outputErr := renderTransfer(app, r.Plan, &r, jsonOut); outputErr != nil {
			return outputErr
		}
		return err
	}}
	apply.Flags().StringVar(&id, "plan", "", "exact transfer plan ID")
	_ = apply.MarkFlagRequired("plan")
	apply.Flags().BoolVar(&jsonOut, "json", false, "emit a sanitized transfer result")
	cmd.AddCommand(apply)
	var statusJSON bool
	status := &cobra.Command{Use: "status", Short: "List local transfer plans and operation ledgers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := interopService(app).Status(cmd.Context(), kind)
		if err != nil {
			return err
		}
		if statusJSON {
			return writeTransferJSON(app, rows)
		}
		for _, r := range rows {
			fmt.Fprintf(app.Out, "%s  %s  %s  %d/%d\n", r.ID, r.Mode, r.Status, r.Completed, r.Total)
		}
		return nil
	}}
	status.Flags().BoolVar(&statusJSON, "json", false, "emit sanitized operation ledgers")
	cmd.AddCommand(status)
	var exportEntry string
	export := &cobra.Command{Use: "export <id>", Short: "Print an optional credential-free reconstruction recipe", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service := interopService(app)
		info, err := service.Inspect(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if info.Kind != kind {
			return errors.New("ID belongs to another artifact family")
		}
		data, err := service.ExportRecipe(cmd.Context(), args[0], exportEntry)
		if err != nil {
			return err
		}
		_, err = app.Out.Write(data)
		return err
	}}
	export.Flags().StringVar(&exportEntry, "entry", "", "portable recipe entry name")
	cmd.AddCommand(export)
	var recipeEntry, recipeRepo string
	var recipeJSON bool
	recipe := &cobra.Command{Use: "recipe <file>", Short: "Plan one entry from an optional reconstruction recipe", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		targets, err := resolveAgentTargets(cmd.Context(), app, cwd, false, recipeRepo, true)
		if err != nil {
			return err
		}
		if len(targets) != 1 {
			return errors.New("select one recipe checkout")
		}
		plan, err := interopService(app).PlanRecipe(cmd.Context(), targets[0].CheckoutRoot, args[0], recipeEntry, kind)
		if err != nil {
			return err
		}
		return renderTransfer(app, plan, nil, recipeJSON)
	}}
	recipe.Flags().StringVar(&recipeEntry, "entry", "", "one recipe entry")
	recipe.Flags().StringVar(&recipeRepo, "repo", "", "repository or exact checkout containing the recipe")
	recipe.Flags().BoolVar(&recipeJSON, "json", false, "emit a sanitized plan")
	registerFlagCompletion(recipe, "repo", completeRepoFlag(app))
	cmd.AddCommand(recipe)
	if kind == "mcp" {
		var jsonOut bool
		check := &cobra.Command{Use: "check <id>", Short: "Explicitly initialize an applied MCP server", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			result, err := interopService(app).Check(cmd.Context(), args[0])
			if jsonOut {
				if e := writeTransferJSON(app, result); e != nil {
					return e
				}
			} else {
				fmt.Fprintf(app.Out, "%s: %s; authentication %s; native client loading %s\n", result.ID, result.Status, result.Authentication, result.ClientLoaded)
			}
			return err
		}}
		check.Flags().BoolVar(&jsonOut, "json", false, "emit connection evidence without server payloads")
		cmd.AddCommand(check)
	}
	for _, action := range []string{"refresh", "undo"} {
		var jsonOut bool
		c := &cobra.Command{Use: action + " <id>", Short: "Create a reviewed " + action + " plan", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			s := interopService(app)
			info, err := s.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if info.Kind != kind {
				return errors.New("ID belongs to another artifact family")
			}
			var p agentinterop.Plan
			if action == "undo" {
				p, err = s.Undo(cmd.Context(), args[0])
			} else {
				p, err = s.Refresh(cmd.Context(), args[0])
			}
			if err != nil {
				return err
			}
			return renderTransfer(app, p, nil, jsonOut)
		}}
		c.Flags().BoolVar(&jsonOut, "json", false, "emit the sanitized plan")
		cmd.AddCommand(c)
	}
	return cmd
}

func newAgentTransferPlanCmd(app *App, kind string) *cobra.Command {
	var req agentinterop.TransferRequest
	var fromRepo, toRepo string
	var jsonOut bool
	var bindings []string
	var bridge bool
	req.Kind = kind
	use := "plan"
	if kind == "skill" {
		use += " <skill>"
	}
	cmd := &cobra.Command{Use: use, Short: "Preview a selected transfer without changing agent files", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			if kind != "skill" {
				return errors.New("this artifact is selected by flags")
			}
			req.Name = args[0]
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		resolve := func(scope, repo string) (string, error) {
			if scope == "user" || scope == "global" {
				if repo != "" {
					return "", errors.New("--repo selectors only apply to project scope")
				}
				return os.UserHomeDir()
			}
			targets, err := resolveAgentTargets(cmd.Context(), app, cwd, false, repo, true)
			if err != nil {
				return "", err
			}
			if len(targets) != 1 {
				return "", errors.New("select exactly one checkout")
			}
			return targets[0].CheckoutRoot, nil
		}
		req.From.Root, err = resolve(req.From.Scope, fromRepo)
		if err != nil {
			return err
		}
		if toRepo == "" && req.To.Scope == "project" && req.From.Scope == "project" {
			req.To.Root = req.From.Root
		} else {
			req.To.Root, err = resolve(req.To.Scope, toRepo)
			if err != nil {
				return err
			}
		}
		for _, ref := range []*agentinterop.ArtifactRef{&req.From, &req.To} {
			if filepath.IsAbs(ref.Path) {
				ref.Path, err = filepath.Rel(ref.Root, ref.Path)
				if err != nil {
					return err
				}
			}
		}
		req.Bindings = nil
		for _, binding := range bindings {
			parts := strings.SplitN(binding, "=", 2)
			if len(parts) != 2 {
				return errors.New("--bind expects SERVER_ENV=PROCESS_ENV, never a credential value")
			}
			req.Bindings = append(req.Bindings, agentinterop.SecretRef{Name: parts[0], Variable: parts[1]})
		}
		if req.EnvFile != "" || bridge {
			req.Launcher, err = os.Executable()
			if err != nil {
				return err
			}
		}
		var p agentinterop.Plan
		if cmd.Name() == "prepare" {
			p, err = interopService(app).Prepare(cmd.Context(), req)
		} else {
			p, err = interopService(app).Plan(cmd.Context(), req)
		}
		if err != nil {
			return err
		}
		return renderTransfer(app, p, nil, jsonOut)
	}}
	f := cmd.Flags()
	f.StringVar(&fromRepo, "from-repo", "", "source repository or exact checkout")
	f.StringVar(&toRepo, "to-repo", "", "destination repository or exact checkout")
	f.StringVar(&req.From.Scope, "from-scope", "project", "source scope: project or user/global")
	f.StringVar(&req.To.Scope, "to-scope", "project", "destination scope: project or user/global")
	f.StringVar(&req.From.Agent, "from-agent", "", "source agent format or skill installation")
	f.StringVar(&req.To.Agent, "to-agent", "", "destination agent format or skill installation")
	f.StringVar(&req.Mode, "mode", "", "copy, move, mirror, or skill install")
	f.StringVar(&req.From.Path, "from", "", "explicit source path within selected scope")
	f.StringVar(&req.To.Path, "to", "", "explicit destination path within selected scope")
	if kind == "mcp" {
		f.StringVar(&req.Name, "server", "", "one source server name")
		f.StringVar(&req.TargetName, "as", "", "destination server name")
		f.StringVar(&req.Transport, "transport", "", "explicit transport for ambiguous remote declarations")
		f.StringSliceVar(&bindings, "bind", nil, "destination environment binding SERVER_ENV=PROCESS_ENV")
		f.StringVar(&req.EnvFile, "secret-env-file", "", "explicit local JSON env source for the optional launcher")
		f.BoolVar(&bridge, "bridge", false, "use the installed dev stdio launcher with a host-local binding")
		f.BoolVar(&req.Adopt, "adopt", false, "adopt an equivalent MCP stanza or explicitly change its managed source")
	}
	if kind == "instructions" {
		f.StringVar(&req.Style, "style", "symlink", "mirror style: symlink or import")
		f.BoolVar(&req.Adopt, "adopt", false, "adopt an equivalent instruction file with private recovery")
	}
	if kind == "skill" {
		f.StringVar(&req.Prepared, "prepared", "", "verified upstream preparation ID")
		f.StringVar(&req.TargetName, "as", "", "destination skill directory name")
	}
	f.BoolVar(&jsonOut, "json", false, "emit a sanitized transfer plan")
	registerFlagCompletion(cmd, "from-repo", completeRepoFlag(app))
	registerFlagCompletion(cmd, "to-repo", completeRepoFlag(app))
	return cmd
}

func writeTransferJSON(app *App, value any) error {
	e := json.NewEncoder(app.Out)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
func renderTransfer(app *App, p agentinterop.Plan, r *agentinterop.ApplyResult, jsonOut bool) error {
	if jsonOut {
		if r != nil {
			return writeTransferJSON(app, r)
		}
		return writeTransferJSON(app, p)
	}
	fmt.Fprintf(app.Out, "%s %s: %s\nPlan %s\n", p.Kind, p.Mode, p.Status, p.ID)
	for _, c := range p.Changes {
		fmt.Fprintf(app.Out, "  %-8s %s\n", c.Action, filepath.Join(c.Root, c.Path))
	}
	if len(p.Changes) == 0 {
		fmt.Fprintln(app.Out, "  no file changes")
	}
	for _, note := range p.Notes {
		fmt.Fprintf(app.Out, "  %s\n", note)
	}
	if r != nil {
		fmt.Fprintf(app.Out, "Confirmed effects: %d/%d\n", r.Completed, r.Total)
	}
	return nil
}

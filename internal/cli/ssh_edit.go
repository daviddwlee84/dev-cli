package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

func newSSHFormatCmd(app *App) *cobra.Command {
	var files []string
	var indent string
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{Use: "format", Short: "Preview or apply SSH indentation changes with recovery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runSSHFormat(cmd.Context(), app, files, indent, apply, yes, jsonOut)
	}}
	f := cmd.Flags()
	f.StringArrayVar(&files, "file", nil, "SSH root or user config.d file (repeatable; default: ~/.ssh/config)")
	f.StringVar(&indent, "indent", "4", "indentation: 2, 4 or tab")
	editFlags(cmd, &apply, &yes, &jsonOut)
	registerFlagCompletion(cmd, "indent", fixedCompletions("2", "4", "tab"))
	return cmd
}
func runSSHFormat(ctx context.Context, app *App, files []string, indent string, apply, yes, jsonOut bool) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	whitespace, ok := map[string]string{"2": "  ", "4": "    ", "tab": "\t"}[indent]
	if !ok {
		return asUsageError(errors.New("--indent must be 2, 4 or tab"))
	}
	for i := range files {
		files[i] = expandSSHFile(files[i])
		if !filepath.IsAbs(files[i]) {
			files[i], err = filepath.Abs(files[i])
			if err != nil {
				return err
			}
		}
	}
	p, err := service.PlanFormat(ctx, files, whitespace)
	if err != nil {
		return err
	}
	return runSSHFilePlan(ctx, app, "ssh_format", p, apply, yes, jsonOut)
}
func editFlags(cmd *cobra.Command, apply, yes, jsonOut *bool) {
	f := cmd.Flags()
	f.BoolVar(apply, "apply", false, "apply after reviewing the plan")
	f.BoolVar(yes, "yes", false, "confirm local file changes without prompting")
	f.BoolVar(jsonOut, "json", false, "emit one versioned plan or result")
}
func newSSHOrganizeCmd(app *App) *cobra.Command {
	var groups []string
	var numbered, apply, yes, jsonOut bool
	cmd := &cobra.Command{Use: "organize", Short: "Move complete Host blocks into group directories without reordering", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if yes && !apply {
			return asUsageError(errors.New("--yes requires --apply"))
		}
		service, err := app.sshHosts()
		if err != nil {
			return err
		}
		layout, err := service.Organization(cmd.Context())
		if err != nil {
			return err
		}
		assignments := map[string]string{}
		for _, value := range groups {
			key, group, ok := strings.Cut(value, "=")
			if !ok || group == "" {
				return asUsageError(errors.New("--group expects alias=group or @block-id=group"))
			}
			found := false
			for _, b := range layout.Blocks {
				matches := key == "@"+b.ID
				for _, a := range b.Aliases {
					matches = matches || strings.EqualFold(a, key)
				}
				if matches {
					if prior, ok := assignments[b.ID]; ok && prior != group {
						return errors.New("aliases in one Host block cannot have different groups")
					}
					assignments[b.ID] = group
					found = true
				}
			}
			if !found {
				return fmt.Errorf("no movable block matches %q", key)
			}
		}
		wizard := len(groups) == 0 && !jsonOut && app.interactive() && !apply
		if wizard {
			fmt.Fprintln(app.Out, "Grouping preserves original priority. Unassigned blocks keep their group (or ungrouped).")
			for {
				var items []picker.Item
				for _, b := range layout.Blocks {
					group := b.Group
					if v, ok := assignments[b.ID]; ok {
						group = v
					}
					items = append(items, picker.Item{Value: b.ID, Label: strings.Join(b.Patterns, " "), Description: group + " · " + config.Contract(b.Source.Path) + ":" + fmt.Sprint(b.Source.Line)})
				}
				selected, e := sshPick(cmd.Context(), app, "Select blocks to assign a group", items, true)
				if e != nil {
					return e
				}
				group, e := newPrompter(app).line("Destination group", "ungrouped")
				if e != nil {
					return e
				}
				for _, item := range selected {
					assignments[item.Value] = group
				}
				more, e := newPrompter(app).confirm("Assign another group?", false)
				if e != nil {
					return e
				}
				if !more {
					break
				}
			}
			apply = true
		}
		p, err := service.PlanOrganize(cmd.Context(), layout, assignments, numbered)
		if err != nil {
			return err
		}
		return runSSHFilePlan(cmd.Context(), app, "ssh_organize", p, apply, yes, jsonOut, layout.Blocks...)
	}}
	cmd.Flags().StringArrayVar(&groups, "group", nil, "assign a complete block: alias=group or @block-id=group (repeatable)")
	cmd.Flags().BoolVar(&numbered, "numbered", false, "prefix fragment names with original ordinal numbers; Include order remains authoritative")
	editFlags(cmd, &apply, &yes, &jsonOut)
	return cmd
}
func newSSHRestoreCmd(app *App) *cobra.Command {
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{Use: "restore <receipt>", Short: "Preview or restore an unchanged local configuration transaction", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, err := configedit.RestorePlan(cmd.Context(), sshflow.RecoveryPath(config.DataHome()), args[0])
		if err != nil {
			return err
		}
		return runSSHFilePlan(cmd.Context(), app, "ssh_restore", p, apply, yes, jsonOut)
	}}
	editFlags(cmd, &apply, &yes, &jsonOut)
	return cmd
}

type sshFileDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	Status        string                `json:"status"`
	Plan          configedit.Plan       `json:"plan"`
	Diff          string                `json:"diff,omitempty"`
	Result        *configedit.Result    `json:"result,omitempty"`
	Error         string                `json:"error,omitempty"`
	Blocks        []sshhost.ConfigBlock `json:"blocks,omitempty"`
}

func runSSHFilePlan(ctx context.Context, app *App, kind string, p configedit.Plan, apply, yes, jsonOut bool, blocks ...sshhost.ConfigBlock) error {
	if yes && !apply {
		return asUsageError(errors.New("--yes requires --apply"))
	}
	document := sshFileDocument{SchemaVersion: 1, Kind: kind + "_plan", Status: "planned", Plan: p, Diff: p.Diff(redactSSHPreview), Blocks: blocks}
	if p.Empty() {
		document.Status = "noop"
	}
	render := func() {
		if p.Empty() {
			fmt.Fprintln(app.Out, "No configuration changes.")
			return
		}
		fmt.Fprint(app.Out, document.Diff)
	}
	if !apply {
		if jsonOut {
			return writeSSHJSON(app, document)
		}
		render()
		return nil
	}
	if !yes && !p.Empty() {
		if jsonOut || !app.interactive() {
			return errors.New("--yes is required to apply outside an interactive terminal")
		}
		render()
		ok, err := newPrompter(app).confirm("Apply these local configuration changes with recovery?", false)
		if err != nil {
			return err
		}
		if !ok {
			return errPromptCanceled
		}
	}
	result, err := configedit.Apply(ctx, p, sshflow.RecoveryPath(config.DataHome()))
	document.Kind = kind + "_result"
	document.Result = &result
	document.Status = result.Status
	if err != nil {
		document.Error = err.Error()
	}
	if jsonOut {
		if e := writeSSHJSON(app, document); e != nil {
			return e
		}
	} else {
		fmt.Fprintln(app.Out, "Configuration:", result.Status)
		if result.Receipt != "" {
			fmt.Fprintf(app.Out, "Recovery: dev ssh restore %s\n", result.Receipt)
		}
	}
	return err
}
func redactSSHPreview(line string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return -1
		}
		return r
	}, line)
	lower := strings.ToLower(clean)
	fields := strings.Fields(lower)
	if len(fields) > 1 && fields[0] == "match" {
		for _, f := range fields[1:] {
			if f == "exec" {
				return "Match exec [redacted]"
			}
		}
	}
	for _, word := range []string{"password", "token", "secret", "private key", "credential", "authorization", "proxycommand", "localcommand", "remotecommand", "match exec", "setenv"} {
		if strings.Contains(lower, word) {
			prefix := clean[:len(clean)-len(strings.TrimLeft(clean, " \t"))]
			return prefix + "[redacted configuration content]"
		}
	}
	return clean
}

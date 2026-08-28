package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/spf13/cobra"
)

func newTriesCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tries",
		Short: "Manage experiment lifecycle and metadata",
		Long: `List and manage durable experiment identities.

The singular "dev try [name]" keeps its create-or-open grammar. Lifecycle
operations live under this plural command so names such as "archive" remain
valid experiment names.`,
	}
	cmd.AddCommand(
		newTriesListCmd(app),
		newTriesOpenCmd(app),
		newTriesTouchCmd(app),
		newTriesAttachCmd(app),
		newTriesMarkCmd(app),
		newTriesDeprecateCmd(app),
		newTriesReactivateCmd(app),
		newTriesArchiveCmd(app),
		newTriesRestoreCmd(app),
		newGraduateCmdWithUse(app, "graduate [try]"),
	)
	return cmd
}

func newTriesListCmd(app *App) *cobra.Command {
	var all, jsonOut, sizes, refreshSizes bool
	cmd := &cobra.Command{
		Use:   "list [query]",
		Short: "List active Tries or retained history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			items, diagnostics, err := service.List(ctxOf(), experiment.ListOptions{
				All: all, Query: query,
			})
			warnExperimentDiagnostics(app, diagnostics)
			if err != nil {
				return err
			}
			measurements := map[string]sizeMeasurement(nil)
			if sizes || refreshSizes {
				measurements = measureTryItems(ctxOf(), app, items, refreshSizes)
			}
			if jsonOut {
				rows := make([]tryJSONRow, 0, len(items))
				for _, item := range items {
					rows = append(rows, makeTryJSONRow(item, config.Hostname(), measurements[item.ID]))
				}
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(rows)
			}
			if len(items) == 0 {
				fmt.Fprintln(app.Out, "No tries match that query.")
				return nil
			}
			headings := []string{"TRY", "PHASE", "LOCATION", "AGE", "GIT"}
			if sizes || refreshSizes {
				headings = append(headings, "SIZE")
			}
			headings = append(headings, "TAGS")
			table := app.newTable(headings...)
			style := app.outStyle()
			for _, item := range items {
				state := "unknown"
				if item.Entry != nil {
					if location, ok := item.Entry.LocationFor(config.Hostname()); ok {
						state = string(location.State)
					}
				}
				gitColumn := "—"
				switch {
				case item.Live.Repo != nil && item.Live.Status != nil && item.Live.Status.Dirty():
					gitColumn = "repo ●"
				case item.Live.Repo != nil && item.Live.StatusError != nil:
					gitColumn = "repo ?"
				case item.Live.Repo != nil:
					gitColumn = "repo"
				case item.Live.DiscoverError != nil:
					gitColumn = "error"
				}
				age := "unknown"
				if activity := item.Activity(); !activity.IsZero() {
					age = humanAge(time.Since(activity))
				}
				values := []string{truncate(item.DisplayName(), 40), string(item.Phase), style.taskState(state), age, style.git(gitColumn)}
				if sizes || refreshSizes {
					measurement := measurements[item.ID]
					values = append(values, sizeColumn(measurement.Usage, measurement.Err))
				}
				values = append(values, strings.Join(item.Tags, ","))
				table.Add(values...)
			}
			table.Render(app.Out)
			return nil
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&all, "all", "a", false, "include deprecated, archived, evicted, and graduated history")
	flags.BoolVar(&jsonOut, "json", false, "emit stable nested JSON")
	flags.BoolVar(&sizes, "sizes", false, "measure logical checkout/private/shared Git bytes")
	flags.BoolVar(&refreshSizes, "refresh-sizes", false, "ignore the size cache and measure again")
	return cmd
}

func newTriesOpenCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "open <ref>",
		Short: "Open a present Try",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			item, diagnostics, err := service.ResolveWithOptions(ctx, args[0], experiment.ResolveOptions{
				IncludeDeprecated: true,
			})
			warnExperimentDiagnostics(app, diagnostics)
			if err != nil {
				return err
			}
			if !item.Live.Present || item.Live.CurrentPath == "" {
				return fmt.Errorf("try %s is not present on this host", item.DisplayName())
			}
			target := item.OpenTarget()
			fmt.Fprintln(app.Out, config.Contract(target.Path))
			if err := openOrCD(app, ctx, target.Path, target.Label); err != nil {
				return err
			}
			_, err = service.Touch(ctx, target.CatalogID)
			return err
		},
	}
}

func newTriesTouchCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "touch <ref>",
		Short: "Record explicit activity for a present Try",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			item, diagnostics, err := service.ResolveWithOptions(ctxOf(), args[0], experiment.ResolveOptions{
				IncludeDeprecated: true,
			})
			warnExperimentDiagnostics(app, diagnostics)
			if err != nil {
				return err
			}
			if !item.Live.Present {
				return fmt.Errorf("try %s is not present on this host", item.DisplayName())
			}
			result, err := service.Touch(ctxOf(), item.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s\n", result.Item.DisplayName(), result.TouchedAt.Format(time.RFC3339))
			return nil
		},
	}
}

func newTriesAttachCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id> <path>",
		Short: "Attach a stable Try ID to a visible local path",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			item, err := service.Attach(ctxOf(), args[0], config.Expand(args[1]))
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s  %s\n", item.ID, item.DisplayName(), config.Contract(item.Live.CurrentPath))
			return nil
		},
	}
}

func newTriesMarkCmd(app *App) *cobra.Command {
	var add, remove []string
	var note string
	cmd := &cobra.Command{
		Use:   "mark <ref>",
		Short: "Add tags or update a Try note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(add) == 0 && len(remove) == 0 && !cmd.Flags().Changed("note") {
				return fmt.Errorf("mark requires --add, --remove, or --note")
			}
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			var noteValue *string
			if cmd.Flags().Changed("note") {
				noteValue = &note
			}
			item, diagnostics, err := service.Patch(ctxOf(), experiment.PatchRequest{
				Ref: args[0], AddTags: add, RemoveTags: remove, Note: noteValue,
			})
			warnExperimentDiagnostics(app, diagnostics)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s\n  tags  %s\n  note  %s\n",
				item.DisplayName(), dash(strings.Join(item.Tags, ", ")), dash(item.Note))
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&add, "add", nil, "tag to add (repeatable)")
	flags.StringArrayVar(&remove, "remove", nil, "tag to remove (repeatable)")
	flags.StringVar(&note, "note", "", "replace the note; pass an empty value to clear it")
	return cmd
}

func newTriesDeprecateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "deprecate <ref>",
		Aliases: []string{"abandon"},
		Short:   "Mark a Try deprecated without moving it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			result, err := service.Deprecate(ctxOf(), experiment.TransitionRequest{Ref: args[0]})
			warnExperimentDiagnostics(app, result.Diagnostics)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "deprecated  %s  (%s)\n", result.Item.DisplayName(), result.Item.ID)
			return nil
		},
	}
}

func newTriesReactivateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "reactivate <ref>",
		Short: "Mark a deprecated Try active again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			result, err := service.Reactivate(ctxOf(), experiment.TransitionRequest{Ref: args[0]})
			warnExperimentDiagnostics(app, result.Diagnostics)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "active  %s  (%s)\n", result.Item.DisplayName(), result.Item.ID)
			return nil
		},
	}
}

func newTriesArchiveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "archive <ref>",
		Short: "Move a present Try into hidden local archive storage",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			result, err := service.Archive(ctxOf(), experiment.TransitionRequest{Ref: args[0]})
			warnExperimentDiagnostics(app, result.Diagnostics)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "archive  %s\n       →  %s\n",
				config.Contract(result.Plan.Source), config.Contract(result.Plan.Destination))
			return nil
		},
	}
}

func newTriesRestoreCmd(app *App) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "restore <ref>",
		Short: "Restore an archived Try to a visible path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			if to != "" && (strings.HasPrefix(to, "~") || filepath.IsAbs(to)) {
				to = config.Expand(to)
			}
			result, err := service.Restore(ctxOf(), experiment.TransitionRequest{Ref: args[0], To: to})
			warnExperimentDiagnostics(app, result.Diagnostics)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "restore  %s\n       →  %s\n",
				config.Contract(result.Plan.Source), config.Contract(result.Plan.Destination))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "restore to this safe path under tries_root")
	return cmd
}

type tryJSONRow struct {
	Identity   tryJSONIdentity   `json:"identity"`
	Experiment tryJSONExperiment `json:"experiment"`
	Location   *tryJSONLocation  `json:"location"`
	Live       tryJSONLive       `json:"live"`
	Metadata   tryJSONMetadata   `json:"metadata"`
	Size       *diskusage.Usage  `json:"size"`
	SizeError  string            `json:"size_error,omitempty"`
}

type tryJSONIdentity struct {
	ID       string       `json:"id"`
	Kind     catalog.Kind `json:"kind"`
	Name     string       `json:"name"`
	Slug     string       `json:"slug,omitempty"`
	Basename string       `json:"basename,omitempty"`
}

type tryJSONExperiment struct {
	Phase          catalog.ExperimentPhase `json:"phase"`
	OriginURL      string                  `json:"origin_url,omitempty"`
	Started        *string                 `json:"started"`
	OriginalPath   string                  `json:"original_path,omitempty"`
	DeprecatedAt   *string                 `json:"deprecated_at,omitempty"`
	DeprecatedPath string                  `json:"deprecated_path,omitempty"`
	GraduatedAt    *string                 `json:"graduated_at,omitempty"`
	GraduatedPath  string                  `json:"graduated_path,omitempty"`
}

type tryJSONLocation struct {
	Host         string                `json:"host"`
	State        catalog.LocationState `json:"state"`
	CurrentPath  string                `json:"current_path,omitempty"`
	RestorePath  string                `json:"restore_path,omitempty"`
	RealPath     string                `json:"real_path,omitempty"`
	GitCommonDir string                `json:"git_common_dir,omitempty"`
	Updated      *string               `json:"updated"`
}

type tryJSONLive struct {
	Present    bool        `json:"present"`
	Path       string      `json:"path,omitempty"`
	RealPath   string      `json:"real_path,omitempty"`
	ActivityAt *string     `json:"activity_at"`
	Git        *tryJSONGit `json:"git,omitempty"`
	Errors     []string    `json:"errors,omitempty"`
}

type tryJSONGit struct {
	LinkedWorktree bool    `json:"linked_worktree"`
	Branch         string  `json:"branch,omitempty"`
	Dirty          *bool   `json:"dirty"`
	LastCommitAt   *string `json:"last_commit_at"`
	LastSubject    string  `json:"last_subject,omitempty"`
}

type tryJSONMetadata struct {
	Tags       []string `json:"tags"`
	Note       string   `json:"note"`
	Created    *string  `json:"created"`
	LastOpened *string  `json:"last_opened"`
}

func makeTryJSONRow(item experiment.Item, host string, measurement sizeMeasurement) tryJSONRow {
	row := tryJSONRow{
		Identity: tryJSONIdentity{
			ID: item.ID, Kind: item.Kind, Name: item.Name, Slug: item.Slug, Basename: item.Basename,
		},
		Experiment: tryJSONExperiment{
			Phase: item.Phase, OriginURL: item.OriginURL, Started: rfc3339Value(item.Started),
		},
		Live: tryJSONLive{
			Present: item.Live.Present, Path: item.Live.CurrentPath, RealPath: item.Live.RealPath,
			ActivityAt: rfc3339Value(item.Activity()),
		},
		Metadata: tryJSONMetadata{
			Tags: append([]string{}, item.Tags...), Note: item.Note,
			Created: rfc3339Value(item.Created), LastOpened: rfc3339Value(item.LastOpened),
		},
	}
	if measurement.Usage != nil {
		usage := *measurement.Usage
		row.Size = &usage
	}
	if measurement.Err != nil {
		row.SizeError = measurement.Err.Error()
	}
	if item.Entry != nil {
		if experimentMetadata := item.Entry.Experiment; experimentMetadata != nil {
			row.Experiment.OriginalPath = experimentMetadata.OriginalPath
			row.Experiment.DeprecatedAt = rfc3339Value(experimentMetadata.DeprecatedAt)
			row.Experiment.DeprecatedPath = experimentMetadata.DeprecatedPath
			row.Experiment.GraduatedAt = rfc3339Value(experimentMetadata.GraduatedAt)
			row.Experiment.GraduatedPath = experimentMetadata.GraduatedPath
		}
		if location, ok := item.Entry.LocationFor(host); ok {
			row.Location = &tryJSONLocation{
				Host: host, State: location.State, CurrentPath: location.CurrentPath,
				RestorePath: location.RestorePath, RealPath: location.RealPath,
				GitCommonDir: location.GitCommonDir, Updated: rfc3339Value(location.Updated),
			}
		}
	}
	if item.Live.Repo != nil {
		git := &tryJSONGit{
			LinkedWorktree: item.Live.Repo.IsLinkedWorktree,
			LastCommitAt:   rfc3339Value(item.Live.LastCommit), LastSubject: item.Live.LastCommitSubject,
		}
		if item.Live.Status != nil {
			dirty := item.Live.Status.Dirty()
			git.Dirty = &dirty
			git.Branch = item.Live.Status.Branch
		}
		row.Live.Git = git
	}
	for _, liveErr := range []error{item.CatalogError, item.Live.DiscoverError, item.Live.StatusError, item.Live.LastCommitError} {
		if liveErr != nil {
			row.Live.Errors = append(row.Live.Errors, liveErr.Error())
		}
	}
	return row
}

func rfc3339Value(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

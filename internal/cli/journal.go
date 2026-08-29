package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/journal"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/spf13/cobra"
)

func newJournalCmd(app *App) *cobra.Command {
	var (
		since, until  string
		repoRefs      []string
		authors       []string
		allAuthors    bool
		granularity   string
		maxCommits    int
		metrics       bool
		includeMerges bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Generate a development journal from Git and current context",
		Long: `Generate Markdown or JSON describing development activity in a calendar-day range.

Git commits are the durable historical source. For the current Git user, dev
also adds source-separated session/WakaTime observations, matching task intent,
and dirty linked-worktree snapshots whose latest file mtime falls in the range.
The output is designed for direct reading or piping to an AI agent; dev does not
invoke an agent or persist the generated journal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allAuthors && len(authors) > 0 {
				return fmt.Errorf("--all-authors cannot be combined with --author")
			}
			g := journal.Granularity(strings.ToLower(granularity))
			switch g {
			case journal.GranularityAuto, journal.GranularityRepo, journal.GranularityBranch, journal.GranularityCommit:
			default:
				return fmt.Errorf("unknown --granularity %q: want auto, repo, branch or commit", granularity)
			}
			if maxCommits < 0 {
				return fmt.Errorf("--max-commits must be zero (unlimited) or positive")
			}
			start, end, err := journal.ParseWindow(since, until, time.Now())
			if err != nil {
				return err
			}
			ctx := ctxOf()
			var reposToScan []repo.Repo
			if len(repoRefs) == 0 {
				reposToScan, err = repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
				if err != nil {
					return err
				}
			} else {
				seen := map[string]bool{}
				for _, ref := range repoRefs {
					r, _, resolveErr := resolveRepoRef(app, ref)
					if resolveErr != nil {
						return resolveErr
					}
					key := r.CommonDir
					if key == "" {
						key = r.Path
					}
					if !seen[key] {
						seen[key] = true
						reposToScan = append(reposToScan, r)
					}
				}
			}
			tasks, err := app.Tasks.List()
			if err != nil {
				return err
			}
			limit := maxCommits
			if g == journal.GranularityCommit && !cmd.Flags().Changed("max-commits") {
				limit = 0
			}
			report, err := journal.Collect(ctx, reposToScan, journal.Options{
				Since: start, Until: end, Authors: authors, AllAuthors: allAuthors,
				Granularity: g, MaxCommits: limit, Metrics: metrics,
				IncludeMerges: includeMerges, LocalContext: !allAuthors && len(authors) == 0,
				StatsPath: stats.Path(app.Cfg.StateDir()), Tasks: tasks,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(app.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			var doc bytes.Buffer
			journal.RenderMarkdown(&doc, report, metrics)
			fmt.Fprint(app.Out, renderMarkdown(doc.String(), app.outStyle()))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "today", "start: today, yesterday, 7d, 4w, 3mo, 1y or YYYY-MM-DD")
	f.StringVar(&until, "until", "today", "inclusive end: today, yesterday or YYYY-MM-DD")
	f.StringArrayVarP(&repoRefs, "repo", "r", nil, "limit to this repository (repeatable)")
	f.StringArrayVar(&authors, "author", nil, "exact author email (repeatable; default: effective Git user)")
	f.BoolVar(&allAuthors, "all-authors", false, "include commits by every author")
	f.StringVar(&granularity, "granularity", "auto", "detail level: auto, repo, branch or commit")
	f.IntVar(&maxCommits, "max-commits", 100, "maximum commit details in auto/commit output (0 for all)")
	f.BoolVar(&metrics, "metrics", false, "include files, additions, deletions and churn")
	f.BoolVar(&includeMerges, "include-merges", false, "include merge commits")
	f.BoolVar(&jsonOut, "json", false, "emit stable JSON")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	registerFlagCompletion(cmd, "granularity", fixedCompletions("auto", "repo", "branch", "commit"))
	return cmd
}

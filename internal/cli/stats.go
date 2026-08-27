package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/spf13/cobra"
)

func newStatsCmd(app *App) *cobra.Command {
	var (
		heatmap bool
		since   string
		repoRef string
		sources []string
		byRepo  bool
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show where development time actually went",
		Long: `Report recorded activity per repository, with a contribution-style heatmap.

Two sources feed this, because neither alone is honest:

  session   dev's sampler, watching live agent and terminal sessions. The only
            source that counts time spent reading and debugging.
  git       commit timestamps, which backfill the days before dev existed and
            survive losing the database.
  wakatime  optional editor time dev cannot observe from a terminal.

The sampler needs to be run periodically — see "dev stats sample --help".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()

			until := time.Now()
			start, err := parseSince(since, until)
			if err != nil {
				return err
			}
			q := stats.Query{Since: start, Until: until, Repo: repoRef}
			for _, s := range sources {
				q.Sources = append(q.Sources, stats.Source(s))
			}

			if store.Empty() {
				fmt.Fprintln(app.Out, "No activity recorded yet.")
				fmt.Fprintln(app.Out, "\nBackfill from git history:   dev stats backfill")
				fmt.Fprintln(app.Out, "Start sampling live work:    dev stats sample   (from cron, every 5m)")
				if app.Cfg.Stats.WakaTime {
					fmt.Fprintln(app.Out, "Import editor time:          dev stats import-wakatime")
				}
				return nil
			}

			if heatmap || !byRepo {
				totals, err := store.DayTotals(q)
				if err != nil {
					return err
				}
				title := "all repos"
				if repoRef != "" {
					title = repoRef
				}
				fmt.Fprintf(app.Out, "Activity — %s, %s to %s\n\n",
					title, start.Format("2006-01-02"), until.Format("2006-01-02"))
				fmt.Fprint(app.Out, stats.Heatmap(totals, stats.HeatmapOptions{
					Since: start, Until: until, Legend: true, WeekdayLabels: true,
				}))
			}

			repoTotals, err := store.RepoTotals(q)
			if err != nil {
				return err
			}
			if len(repoTotals) == 0 {
				fmt.Fprintln(app.Out, "\nNothing recorded in that window.")
				return nil
			}
			max := repoTotals[0].Seconds
			fmt.Fprintln(app.Out)
			t := NewTable("REPO", "TIME", "DAYS", "LAST", "")
			for i, r := range repoTotals {
				if limit > 0 && i >= limit {
					break
				}
				t.Add(truncate(r.Repo, 30), stats.HumanDuration(r.Seconds),
					fmt.Sprintf("%d", r.Days), r.Last, stats.Sparkline(r.Seconds, max, 24))
			}
			t.Render(app.Out)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&heatmap, "heatmap", false, "show the day grid (shown by default unless --by-repo)")
	f.StringVar(&since, "since", "12mo", "window: 30d, 6mo, 1y, or a YYYY-MM-DD date")
	f.StringVarP(&repoRef, "repo", "r", "", "limit to repositories matching this")
	f.StringSliceVar(&sources, "source", nil, "limit to these sources (session, git, wakatime)")
	f.BoolVar(&byRepo, "by-repo", false, "show only the per-repo breakdown")
	f.IntVar(&limit, "limit", 20, "maximum repositories in the breakdown (0 for all)")

	cmd.AddCommand(newStatsSampleCmd(app), newStatsBackfillCmd(app), newStatsImportCmd(app))
	return cmd
}

func newStatsSampleCmd(app *App) *cobra.Command {
	var (
		interval    time.Duration
		includeIdle bool
	)
	cmd := &cobra.Command{
		Use:   "sample",
		Short: "Record one interval of live session activity",
		Long: `Attribute one interval of time to every live session working in a known repo.

Run this periodically — the interval you pass must match how often it runs, or
the totals will be wrong. With cron, every five minutes:

    */5 * * * * /usr/local/bin/dev stats sample --interval 5m

Idle sessions are skipped by default: a workspace left open for three weeks is
not three weeks of work, and counting it that way makes the chart meaningless.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.Cfg.Stats.Sampler {
				return fmt.Errorf("sampling is disabled (set stats.sampler = true in %s)",
					config.Contract(config.ConfigFile()))
			}
			ctx := ctxOf()
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()

			repos, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
			if err != nil {
				return err
			}
			n, err := stats.Sample(ctx, store, app.Runtime(), repos, interval, includeIdle)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "recorded %s across %d active session(s)\n", interval, n)
			return nil
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", stats.SampleInterval, "time to attribute, matching how often this runs")
	f.BoolVar(&includeIdle, "include-idle", false, "also count sessions whose agent is idle")
	return cmd
}

func newStatsBackfillCmd(app *App) *cobra.Command {
	var (
		since  string
		author string
	)
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Seed the database from git commit history",
		Long: `Derive activity from commit timestamps across every discovered repository.

Commits show that work happened on a day, not how long it took, so each is
credited a flat 20 minutes. That is enough to shade a heatmap cell honestly
and not enough to mistake for a timesheet.

Re-running over the same window replaces those numbers rather than adding to
them, so this is safe to repeat.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()

			start, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}
			repos, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "scanning %d repositories since %s…\n", len(repos), start.Format("2006-01-02"))
			n, err := stats.BackfillGit(ctx, store, repos, start, author)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "recorded %d repo-days\n", n)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "12mo", "how far back to scan")
	f.StringVar(&author, "author", "", "only commits from this author email")
	return cmd
}

func newStatsImportCmd(app *App) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "import-wakatime",
		Short: "Import per-project daily totals from WakaTime",
		Long: `Pull daily per-project totals from the WakaTime API.

The API key is read from wakatime_config (~/.wakatime.cfg by default) — the
same file the editor plugins already use, so there is nothing new to configure.
Imported time is stored under its own source, because it overlaps with what
dev's sampler sees rather than replacing it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			key, err := stats.APIKeyFromConfig(config.Expand(app.Cfg.Stats.WakaTimeConfig))
			if err != nil {
				return err
			}
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()

			until := time.Now()
			start, err := parseSince(since, until)
			if err != nil {
				return err
			}
			w := &stats.WakaTime{APIKey: key}
			n, err := w.Import(ctx, store, start, until)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "imported %d project-days from WakaTime\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "6mo", "how far back to import")
	return cmd
}

// parseSince accepts a relative window ("30d", "6mo", "1y") or an absolute
// date. Relative is what people actually type; absolute is what scripts want.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.AddDate(-1, 0, 0), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return d, nil
	}
	var n int
	var unit string
	if _, err := fmt.Sscanf(s, "%d%s", &n, &unit); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("cannot parse --since %q: want 30d, 6mo, 1y or YYYY-MM-DD", s)
	}
	switch strings.ToLower(unit) {
	case "d", "day", "days":
		return now.AddDate(0, 0, -n), nil
	case "w", "week", "weeks":
		return now.AddDate(0, 0, -n*7), nil
	case "mo", "month", "months":
		return now.AddDate(0, -n, 0), nil
	case "y", "yr", "year", "years":
		return now.AddDate(-n, 0, 0), nil
	}
	return time.Time{}, fmt.Errorf("unknown unit %q in --since: want d, w, mo or y", unit)
}

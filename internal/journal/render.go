package journal

import (
	"fmt"
	"io"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/stats"
)

func RenderMarkdown(w io.Writer, report Report, metrics bool) {
	until := report.Window.Until.AddDate(0, 0, -1)
	fmt.Fprintf(w, "# Development journal\n\n%s to %s · %s\n\n",
		report.Window.Since.Format("2006-01-02"), until.Format("2006-01-02"), report.Window.Timezone)
	author := strings.Join(report.Authors, ", ")
	if report.AllAuthors {
		author = "all authors"
	} else if author == "" {
		author = "effective Git user"
	}
	fmt.Fprintf(w, "%d repositories · %d branches · %d commits · %s\n",
		report.Summary.Repositories, report.Summary.Branches, report.Summary.Commits, author)
	if report.Summary.OmittedCommits > 0 {
		fmt.Fprintf(w, "\n> Showing %d commits; %d omitted. Use `--max-commits 0` for all.\n",
			report.Summary.ShownCommits, report.Summary.OmittedCommits)
	}
	if len(report.Repositories) == 0 {
		fmt.Fprintln(w, "\nNo matching development activity.")
		return
	}
	for _, r := range report.Repositories {
		fmt.Fprintf(w, "\n## %s\n\n%d commits", r.DisplayName, r.CommitCount)
		if metrics {
			fmt.Fprintf(w, " · %d files · +%d/-%d", r.Metrics.Files, r.Metrics.Additions, r.Metrics.Deletions)
		}
		fmt.Fprintln(w)
		for _, a := range r.Activity {
			label := string(a.Source)
			if a.Branch != "" {
				label += " on " + a.Branch
			}
			fmt.Fprintf(w, "- Activity evidence: %s %s\n", label, stats.HumanDuration(a.Seconds))
		}
		for _, b := range r.Branches {
			fmt.Fprintf(w, "\n### %s\n\n%d commits", b.Name, b.CommitCount)
			if metrics {
				fmt.Fprintf(w, " · %d files · +%d/-%d", b.Metrics.Files, b.Metrics.Additions, b.Metrics.Deletions)
			}
			fmt.Fprintln(w)
			if b.Task != nil {
				fmt.Fprintf(w, "- Task: %s (%s)", b.Task.Title, b.Task.State)
				if b.Task.Next != "" {
					fmt.Fprintf(w, "; next: %s", b.Task.Next)
				}
				if b.Task.Note != "" {
					fmt.Fprintf(w, "; note: %s", b.Task.Note)
				}
				fmt.Fprintln(w)
			}
			for _, c := range b.Current {
				fmt.Fprintf(w, "- Current snapshot: %d changed paths (+%d staged, !%d unstaged, ?%d untracked) at %s\n",
					c.Changed, c.Staged, c.Unstaged, c.Untracked, c.Path)
			}
			for _, c := range b.Commits {
				fmt.Fprintf(w, "- `%s` %s — %s <%s> · %s", c.ShortOID,
					c.AuthoredAt.Format("2006-01-02 15:04"), c.AuthorName, c.AuthorEmail, c.Subject)
				if metrics {
					fmt.Fprintf(w, " (+%d/-%d, %d files)", c.Metrics.Additions, c.Metrics.Deletions, c.Metrics.Files)
				}
				fmt.Fprintln(w)
			}
			if b.OmittedCommits > 0 {
				fmt.Fprintf(w, "- … %d older commits omitted\n", b.OmittedCommits)
			}
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\n## Collection notes")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "\n- %s", warning)
		}
		fmt.Fprintln(w)
	}
}

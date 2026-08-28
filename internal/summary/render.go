package summary

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func RenderMarkdown(w io.Writer, report Report, detail Detail, attentionOnly bool) {
	fmt.Fprintf(w, "# dev machine summary: %s\n\nGenerated %s\n\n", report.Host,
		report.GeneratedAt.Format("2006-01-02 15:04 MST"))
	t := report.Totals
	fmt.Fprintf(w, "%d projects · %d repositories · %d Tries · %d dirty · %d live · %d attention\n",
		t.Projects, t.Repositories, t.Tries, t.Dirty, t.Live, t.Attention)
	fmt.Fprintf(w, "Recovery: %d without remote · %d with local-only branches\n", t.NoRemote, t.LocalOnly)
	if !report.Capabilities.RuntimeCollected {
		fmt.Fprintln(w, "\n> Runtime data was not collected (`--no-runtime`).")
	}
	if len(report.Projects) == 0 {
		fmt.Fprintln(w, "\nNo projects match that selection.")
		return
	}

	var detailed, compact []Project
	for _, project := range report.Projects {
		expand := detail == DetailFull || (detail == DetailAuto && (attentionOnly || project.Active))
		if expand {
			detailed = append(detailed, project)
		} else {
			compact = append(compact, project)
		}
	}
	if len(detailed) > 0 {
		title := "Active work"
		if attentionOnly {
			title = "Attention"
		} else if detail == DetailFull {
			title = "Projects"
		}
		fmt.Fprintf(w, "\n## %s\n", title)
		for _, project := range detailed {
			renderProject(w, project)
		}
	}
	if len(compact) > 0 {
		fmt.Fprintln(w, "\n## Project index")
		for _, project := range compact {
			renderCompact(w, project)
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

func renderProject(w io.Writer, p Project) {
	label := p.Name
	if p.Kind == "try" {
		label += " (Try"
		if p.Phase != "" {
			label += ": " + p.Phase
		}
		label += ")"
	}
	fmt.Fprintf(w, "\n### %s\n\n", label)
	if p.DisplayPath != "" {
		fmt.Fprintf(w, "- Path: `%s`\n", p.DisplayPath)
	}
	if p.Category != "" {
		fmt.Fprintf(w, "- Category: %s\n", p.Category)
	}
	if p.Note != "" {
		fmt.Fprintf(w, "- Note: %s\n", p.Note)
	}
	if len(p.Tags) > 0 {
		fmt.Fprintf(w, "- Tags: %s\n", strings.Join(p.Tags, ", "))
	}
	if len(p.AttentionReasons) > 0 {
		fmt.Fprintf(w, "- Attention: %s\n", strings.Join(p.AttentionReasons, ", "))
	}
	if p.Git != nil {
		fmt.Fprintf(w, "- Git: `%s` — %s\n", branchName(p.Git), p.Git.Summary)
	}
	if p.Recovery != nil {
		recovery := recoverySummary(*p.Recovery)
		if recovery != "" {
			fmt.Fprintf(w, "- Recovery: %s\n", recovery)
		}
	}
	if p.Size != nil {
		fmt.Fprintf(w, "- Size: %s", p.Size.HumanOwned())
		if p.Size.SharedGitBytes != nil {
			fmt.Fprintf(w, " + %s", p.Size.HumanShared())
		}
		fmt.Fprintln(w)
	} else if p.SizeError != "" {
		fmt.Fprintf(w, "- Size: unavailable (%s)\n", p.SizeError)
	}
	for _, task := range p.Tasks {
		fmt.Fprintf(w, "- Task: `%s` — %s", task.ID, task.State)
		if task.Next != "" {
			fmt.Fprintf(w, "; next: %s", task.Next)
		}
		fmt.Fprintln(w)
	}
	for _, session := range p.Sessions {
		status := session.Status
		if status == "" {
			status = "unknown"
		}
		fmt.Fprintf(w, "- Runtime: `%s %s` (%s)\n", session.Runtime, session.Handle, status)
	}
	if len(p.Checkouts) > 1 {
		fmt.Fprintln(w, "- Checkouts:")
		for _, checkout := range p.Checkouts {
			status := "unavailable"
			if checkout.Git != nil {
				status = checkout.Git.Summary
			}
			branch := checkout.Branch
			if branch == "" {
				branch = "(unknown branch)"
			}
			fmt.Fprintf(w, "  - `%s` — %s, %s, %s\n", checkout.DisplayPath, branch, checkout.Ownership, status)
		}
	}
	for _, commit := range p.RecentCommits {
		prefix := ""
		if commit.ShortOID != "" {
			prefix = "`" + commit.ShortOID + "` "
		}
		fmt.Fprintf(w, "- Latest: %s%s", prefix, commit.Subject)
		if !commit.AuthoredAt.IsZero() {
			fmt.Fprintf(w, " (%s)", humanAge(time.Since(commit.AuthoredAt)))
		}
		fmt.Fprintln(w)
	}
}

func renderCompact(w io.Writer, p Project) {
	parts := []string{p.Kind}
	if p.Git != nil {
		parts = append(parts, branchName(p.Git), p.Git.Summary)
	} else if p.Phase != "" {
		parts = append(parts, p.Phase)
	}
	if !p.LatestActivity.IsZero() {
		parts = append(parts, "last "+humanAge(time.Since(p.LatestActivity)))
	}
	if len(p.AttentionReasons) > 0 {
		parts = append(parts, "attention:"+strings.Join(p.AttentionReasons, ","))
	}
	if len(p.RecentCommits) > 0 {
		parts = append(parts, p.RecentCommits[0].Subject)
	}
	fmt.Fprintf(w, "\n- **%s** — %s", p.Name, strings.Join(parts, " · "))
}

func branchName(g *Git) string {
	if g.Branch != "" {
		return g.Branch
	}
	if g.Detached {
		return "detached HEAD"
	}
	return "unknown branch"
}

func recoverySummary(r Recovery) string {
	var parts []string
	if r.Error != "" {
		parts = append(parts, "unavailable: "+r.Error)
	} else if r.NoRemote {
		parts = append(parts, "no remote")
	} else if len(r.Remotes) > 0 {
		parts = append(parts, strings.Join(r.Remotes, ","))
	}
	if len(r.LocalOnlyBranches) > 0 {
		parts = append(parts, fmt.Sprintf("%d local-only branches", len(r.LocalOnlyBranches)))
	}
	return strings.Join(parts, " · ")
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	}
}

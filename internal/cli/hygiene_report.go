package cli

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	"github.com/spf13/cobra"
)

func (h *hygieneCLI) reportCmd() *cobra.Command {
	var (
		reportID, scope, historyRange, by   string
		dispositions, rules, categories     []string
		paths, scanFiles                    []string
		top                                 int
		timeout                             time.Duration
		rescan, audit, findings, showValues bool
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize a scan by severity, rule and file",
		Long: `Summarize one hygiene scan without printing every finding.

By default the newest stored scan for this checkout is summarized without
scanning again; the pre-commit hook's staged scan is therefore available right
after a blocked commit. --scope selects the newest scan of that scope,
--report an exact report, and --rescan runs a fresh scan first (accepting
--range, --file, --timeout and --audit like dev hygiene scan).

Groups are ordered by severity (block, warn, accepted), then occurrences and
findings. --top limits each group (0 shows all). Filters narrow the findings
before aggregation. --findings lists individual findings; --json emits the
hygiene_summary schema-v1 document for automation.

--values shows masked distinct values per rule and implies --rescan unless
--report names a scan captured with values. Raw values and their context are
written only to a private review file; print its location with
dev hygiene review-path <report-id> and never paste it into chat, Git or CI.`,
		Example: `  dev hygiene report
  dev hygiene report --scope staged --by rule --top 5 --findings
  dev hygiene report --rule privacy-email --values
  dev hygiene --json report --disposition block,warn`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if showValues && reportID == "" {
				rescan = true
			}
			if reportID != "" && rescan {
				return errors.New("--report summarizes a stored scan; it cannot be combined with --rescan")
			}
			if !rescan {
				for _, name := range []string{"range", "file", "timeout", "audit"} {
					if c.Flags().Changed(name) {
						return fmt.Errorf("--%s requires --rescan", name)
					}
				}
			}
			s, err := h.service(ctx)
			if err != nil {
				return err
			}
			request := hygiene.SummaryRequest{
				ReportID: reportID, Scope: scope, By: splitFlagList([]string{by}), Top: top,
				Dispositions: splitFlagList(dispositions), Rules: splitFlagList(rules),
				Categories: splitFlagList(categories), Paths: append([]string(nil), paths...),
				Findings: findings, Values: showValues,
			}
			if err := request.Validate(); err != nil {
				return err
			}
			var scanErr error
			if rescan {
				report, e := s.Scan(ctx, hygiene.ScanOptions{Scope: scope, Range: historyRange, Timeout: timeout, Files: scanFiles, Audit: audit, CaptureValues: showValues})
				if report.ID == "" {
					return h.output(report, e)
				}
				scanErr = e
				request.ReportID, request.Rescanned = report.ID, true
			}
			summary, err := s.Summarize(ctx, request)
			if err != nil {
				return errors.New(feedback.Sanitize(err.Error()))
			}
			return h.output(summary, scanErr)
		},
	}
	f := cmd.Flags()
	f.StringVar(&reportID, "report", "", "summarize this exact stored scan report ID")
	f.StringVar(&scope, "scope", "", "newest stored scan of this scope, or the --rescan scope: staged, worktree or history")
	f.BoolVar(&rescan, "rescan", false, "run a fresh scan before summarizing")
	f.StringVar(&historyRange, "range", "", "with --rescan: history FROM..TO commit OIDs")
	f.StringArrayVar(&scanFiles, "file", nil, "with --rescan: select an in-scope relative file (repeatable)")
	f.DurationVar(&timeout, "timeout", 20*time.Minute, "with --rescan: maximum scan duration")
	f.BoolVar(&audit, "audit", false, "with --rescan: include findings suppressed by local exceptions")
	f.StringVar(&by, "by", "severity,rule,file", "groups to show: severity, rule, file and/or category")
	f.IntVar(&top, "top", 10, "maximum rows per group (0 shows all)")
	f.StringSliceVar(&dispositions, "disposition", nil, "only these dispositions: block, warn, accepted")
	f.StringSliceVar(&rules, "rule", nil, "only these rule IDs")
	f.StringSliceVar(&categories, "category", nil, "only these categories: secret, known, generic")
	f.StringArrayVar(&paths, "path", nil, "only findings whose file matches this glob (repeatable)")
	f.BoolVar(&findings, "findings", false, "list individual findings (location, occurrences, finding ID)")
	f.BoolVar(&showValues, "values", false, "show masked distinct values per rule; raw values stay in a private review file")
	return cmd
}

func splitFlagList(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func pluralNoun(n int, noun string) string {
	if n == 1 {
		return noun
	}
	if strings.HasSuffix(noun, "y") {
		return strings.TrimSuffix(noun, "y") + "ies"
	}
	return noun + "s"
}

func groupDigits(n int) string {
	text := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return sign + text
}

func (h *hygieneCLI) dispositionCell(disposition string) string {
	style := h.app.outStyle()
	switch disposition {
	case "block":
		return style.danger(disposition)
	case "warn":
		return style.warning(disposition)
	case "accepted":
		return style.dim(disposition)
	default:
		return disposition
	}
}

func (h *hygieneCLI) renderSummary(v hygiene.Summary) {
	out, style := h.app.Out, h.app.outStyle()
	shortID := v.ReportID
	if len(shortID) > 8 {
		shortID = shortID[:8] + "…"
	}
	fmt.Fprintf(out, "Hygiene %s · %s · report %s\n", v.Scope, v.Status, shortID)
	fmt.Fprintf(out, "  findings %s (occ %s) · block %s · warn %s · accepted %s · gaps %s\n",
		groupDigits(v.Totals.Findings), groupDigits(v.Totals.Occurrences), groupDigits(v.Totals.Blocked),
		groupDigits(v.Totals.Warnings), groupDigits(v.Totals.Accepted), groupDigits(v.Totals.Gaps))
	if !v.Rescanned {
		fmt.Fprintf(out, "  %s\n", style.dim("stored scan from "+v.Created.Format(time.RFC3339)+"; source bytes were not rechecked"))
	}
	if !v.FileCountsComplete {
		fmt.Fprintf(out, "  %s\n", style.warning("legacy report lacks exact file identities; file counts are lower bounds"))
	}
	var filters []string
	for label, values := range map[string][]string{"disposition": v.Filters.Dispositions, "rule": v.Filters.Rules, "category": v.Filters.Categories, "path": v.Filters.Paths} {
		if len(values) > 0 {
			filters = append(filters, label+" "+feedback.Sanitize(strings.Join(values, ",")))
		}
	}
	if len(filters) > 0 {
		sort.Strings(filters)
		fmt.Fprintf(out, "  %s\n", style.dim("filters: "+strings.Join(filters, " · ")))
	}
	if !v.PolicyCurrent {
		fmt.Fprintf(out, "  %s\n", style.warning("policy changed since this scan; rescan before relying on dispositions"))
	}
	if !v.CheckoutCurrent {
		fmt.Fprintf(out, "  %s\n", style.warning("this report was scanned from another checkout"))
	}
	more := func(omitted int, noun string) {
		if omitted > 0 {
			fmt.Fprintf(out, "  %s\n", style.dim(fmt.Sprintf("… %s more %s (--top 0 shows all)", groupDigits(omitted), pluralNoun(omitted, noun))))
		}
	}
	heading := func(title string, ordered bool) {
		if ordered && v.Top > 0 {
			title = "TOP " + title
		}
		if ordered {
			title += " (occurrences desc)"
		}
		fmt.Fprintf(out, "\n%s\n", style.header(title))
	}

	if len(v.Severities) > 0 {
		heading("BY SEVERITY", false)
		for _, severity := range v.Severities {
			detail := "0"
			if severity.Findings > 0 {
				detail = fmt.Sprintf("%s %s · %s occ", groupDigits(severity.Findings), pluralNoun(severity.Findings, "finding"), groupDigits(severity.Occurrences))
			}
			fmt.Fprintf(out, "  %s%s%s\n", h.dispositionCell(severity.Disposition), strings.Repeat(" ", max(1, 10-len(severity.Disposition))), detail)
		}
	}
	if len(v.Rules) > 0 {
		heading("RULES", true)
		table := h.app.newTable("  DISP", "RULE", "CAT", "FIND", "OCC", "FILES", "VALUES")
		for _, rule := range v.Rules {
			values := "—"
			if rule.DistinctValues != nil {
				values = groupDigits(*rule.DistinctValues)
			}
			table.Add("  "+h.dispositionCell(rule.Disposition), feedback.Sanitize(rule.Rule), rule.Category,
				groupDigits(rule.Findings), groupDigits(rule.Occurrences), groupDigits(rule.Files), values)
		}
		table.Render(out)
		more(v.Omitted.Rules, "rule")
	}
	if len(v.Files) > 0 {
		heading("FILES", true)
		table := h.app.newTable("  DISP", "FILE", "FIND", "OCC", "RULES")
		for _, file := range v.Files {
			table.Add("  "+h.dispositionCell(file.Disposition), feedback.Sanitize(file.File),
				groupDigits(file.Findings), groupDigits(file.Occurrences), truncate(feedback.Sanitize(strings.Join(file.Rules, ",")), 60))
		}
		table.Render(out)
		more(v.Omitted.Files, "file")
	}
	if len(v.Categories) > 0 {
		heading("CATEGORIES", true)
		table := h.app.newTable("  DISP", "CATEGORY", "FIND", "OCC", "RULES")
		for _, category := range v.Categories {
			table.Add("  "+h.dispositionCell(category.Disposition), category.Category,
				groupDigits(category.Findings), groupDigits(category.Occurrences), groupDigits(category.Rules))
		}
		table.Render(out)
		more(v.Omitted.Categories, "category")
	}
	if len(v.Findings) > 0 {
		heading("FINDINGS", true)
		table := h.app.newTable("  DISP", "LOCATION", "RULE", "OCC", "FINDING")
		for _, finding := range v.Findings {
			location := fmt.Sprintf("%s:%d", feedback.Sanitize(finding.File), finding.Line)
			if finding.Commit != "" {
				location += " @" + feedback.Sanitize(finding.Commit)
			}
			table.Add("  "+h.dispositionCell(finding.Disposition), location, feedback.Sanitize(finding.Rule), groupDigits(finding.Occurrences), finding.ID)
		}
		table.Render(out)
		more(v.Omitted.Findings, "finding")
	}
	if v.ValuesShown {
		for _, rule := range v.Rules {
			if len(rule.Values) == 0 {
				continue
			}
			fmt.Fprintf(out, "\n%s\n", style.header("VALUES · "+feedback.Sanitize(rule.Rule)+" (masked)"))
			table := h.app.newTable("  VALUE", "OCC", "FILES")
			for _, value := range rule.Values {
				table.Add("  "+feedback.Sanitize(value.Masked), groupDigits(value.Occurrences), groupDigits(value.Files))
			}
			table.Render(out)
			more(rule.OmittedValues, "value")
		}
		if v.ValuesTruncated {
			fmt.Fprintf(out, "  %s\n", style.warning("value capture reached its limit; some values or samples were omitted; finding totals are unchanged"))
		}
		if v.ValuesStatus == "failed" {
			fmt.Fprintf(out, "\n%s\n", style.warning("value review could not be saved; the scan receipt is retained without raw values"))
		} else {
			fmt.Fprintf(out, "\nPrivate full values: dev hygiene review-path %s\n", v.ReportID)
			fmt.Fprintf(out, "%s\n", style.dim("(never paste that file into chat, Git or CI logs)"))
		}
	}

	fmt.Fprintf(out, "\n%s\n", style.dim("Report "+v.ReportID))
	for _, gap := range v.Skipped {
		fmt.Fprintf(out, "  excluded: %s %s\n", gap.Code, feedback.Sanitize(gap.File))
	}
	for _, gap := range v.Gaps {
		fmt.Fprintf(out, "  coverage: %s %s\n", gap.Code, feedback.Sanitize(gap.File))
		if gap.Encoding != nil {
			h.encodingDetail(gap.Encoding)
		}
		if gap.Commit != "" {
			fmt.Fprintf(out, "    commit: %s\n", feedback.Sanitize(gap.Commit))
		}
	}
	if len(v.Gaps) > 0 {
		fmt.Fprintln(out, style.warning("Scan incomplete: coverage gaps must be resolved; a partial report is never clean."))
	}
}

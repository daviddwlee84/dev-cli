package hygiene

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrValuesNotCaptured means a report was produced without value capture.
var ErrValuesNotCaptured = errors.New("values were not captured for this report; rerun with --rescan --values")

// Summary sections selectable with SummaryRequest.By.
const (
	SummaryBySeverity = "severity"
	SummaryByRule     = "rule"
	SummaryByFile     = "file"
	SummaryByCategory = "category"
)

// SummaryRequest selects one stored scan report and how to aggregate it.
type SummaryRequest struct {
	// ReportID selects an exact report; otherwise the checkout's latest scan
	// for Scope (or any scope) is used.
	ReportID     string
	Scope        string
	By           []string
	Top          int
	Dispositions []string
	Rules        []string
	Categories   []string
	Paths        []string
	Findings     bool
	Values       bool
	Rescanned    bool
}

type SummaryFilters struct {
	Dispositions []string `json:"dispositions,omitempty"`
	Rules        []string `json:"rules,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	Paths        []string `json:"paths,omitempty"`
}

type SummaryTotals struct {
	Findings     int `json:"findings"`
	Occurrences  int `json:"occurrences"`
	Blocked      int `json:"blocked"`
	Warnings     int `json:"warnings"`
	Accepted     int `json:"accepted"`
	Gaps         int `json:"gaps"`
	Files        int `json:"files"`
	Rules        int `json:"rules"`
	ScannedFiles int `json:"scanned_files"`
	Skipped      int `json:"skipped"`
}

type SeveritySummary struct {
	Disposition string `json:"disposition"`
	Findings    int    `json:"findings"`
	Occurrences int    `json:"occurrences"`
}

type RuleSummary struct {
	Rule           string         `json:"rule"`
	Category       string         `json:"category"`
	Disposition    string         `json:"disposition"`
	Findings       int            `json:"findings"`
	Occurrences    int            `json:"occurrences"`
	Files          int            `json:"files"`
	DistinctValues *int           `json:"distinct_values,omitempty"`
	Values         []ValueSummary `json:"values,omitempty"`
	OmittedValues  int            `json:"omitted_values,omitempty"`
}

type FileSummary struct {
	File        string   `json:"file"`
	Disposition string   `json:"disposition"`
	Findings    int      `json:"findings"`
	Occurrences int      `json:"occurrences"`
	Rules       []string `json:"rules"`
}

type CategorySummary struct {
	Category    string `json:"category"`
	Disposition string `json:"disposition"`
	Findings    int    `json:"findings"`
	Occurrences int    `json:"occurrences"`
	Rules       int    `json:"rules"`
}

type SummaryOmitted struct {
	Rules      int `json:"rules,omitempty"`
	Files      int `json:"files,omitempty"`
	Categories int `json:"categories,omitempty"`
	Findings   int `json:"findings,omitempty"`
}

// Summary is the public `hygiene_summary` schema-v1 aggregation of one scan
// report. It never contains raw matched values or private file locations.
type Summary struct {
	SchemaVersion   int               `json:"schema_version"`
	Kind            string            `json:"kind"`
	ReportID        string            `json:"report_id"`
	ReportKind      string            `json:"report_kind"`
	Scope           string            `json:"scope"`
	Status          string            `json:"status"`
	Created         time.Time         `json:"created"`
	PolicyCurrent   bool              `json:"policy_current"`
	CheckoutCurrent bool              `json:"checkout_current"`
	Audit           bool              `json:"audit"`
	PublicOnly      bool              `json:"public_only"`
	Rescanned       bool              `json:"rescanned"`
	Sections        []string          `json:"sections"`
	Top             int               `json:"top"`
	Filters         SummaryFilters    `json:"filters"`
	Totals          SummaryTotals     `json:"totals"`
	Severities      []SeveritySummary `json:"severities,omitempty"`
	Rules           []RuleSummary     `json:"rules,omitempty"`
	Files           []FileSummary     `json:"files,omitempty"`
	Categories      []CategorySummary `json:"categories,omitempty"`
	Findings        []Finding         `json:"findings,omitempty"`
	Gaps            []Gap             `json:"gaps,omitempty"`
	Skipped         []Gap             `json:"skipped,omitempty"`
	Omitted         SummaryOmitted    `json:"omitted"`
	ValuesShown     bool              `json:"values_shown"`
	ValuesTruncated bool              `json:"values_truncated,omitempty"`
}

var (
	summarySections     = map[string]bool{SummaryBySeverity: true, SummaryByRule: true, SummaryByFile: true, SummaryByCategory: true}
	summaryDispositions = map[string]bool{string(Block): true, string(Warn): true, "accepted": true}
	summaryCategories   = map[string]bool{"secret": true, "known": true, "generic": true}
)

func dispositionRank(disposition string) int {
	switch disposition {
	case string(Block):
		return 0
	case string(Warn):
		return 1
	case "accepted":
		return 2
	default:
		return 3
	}
}

func worseDisposition(current, next string) string {
	if current == "" || dispositionRank(next) < dispositionRank(current) {
		return next
	}
	return current
}

// Validate checks request flags before any scan or report load.
func (r SummaryRequest) Validate() error {
	if r.Top < 0 {
		return errors.New("--top must be zero (all) or positive")
	}
	for _, section := range r.By {
		if !summarySections[section] {
			return fmt.Errorf("unknown summary grouping %q (use severity, rule, file or category)", section)
		}
	}
	for _, disposition := range r.Dispositions {
		if !summaryDispositions[disposition] {
			return fmt.Errorf("unknown disposition %q (use block, warn or accepted)", disposition)
		}
	}
	for _, category := range r.Categories {
		if !summaryCategories[category] {
			return fmt.Errorf("unknown category %q (use secret, known or generic)", category)
		}
	}
	if r.ReportID != "" && !safeRecordID(r.ReportID) {
		return errors.New("invalid hygiene report ID")
	}
	return nil
}

// Summarize aggregates one stored scan report for this repository.
func (s *Service) Summarize(ctx context.Context, request SummaryRequest) (Summary, error) {
	if err := request.Validate(); err != nil {
		return Summary{}, err
	}
	if err := s.prepare(ctx); err != nil {
		return Summary{}, err
	}
	id := request.ReportID
	if id == "" {
		latest, err := s.LatestReport(ctx, request.Scope)
		if err != nil {
			return Summary{}, err
		}
		id = latest
	}
	record, err := s.loadReport(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	if request.Scope != "" && request.ReportID != "" && record.Report.Scope != request.Scope {
		return Summary{}, fmt.Errorf("report %s has scope %s, not %s", id, record.Report.Scope, request.Scope)
	}
	if request.Values && record.ValuesReview == "" {
		return Summary{}, ErrValuesNotCaptured
	}
	sections := request.By
	if len(sections) == 0 {
		sections = []string{SummaryBySeverity, SummaryByRule, SummaryByFile}
	}
	report := record.Report
	summary := Summary{
		SchemaVersion: 1, Kind: "hygiene_summary",
		ReportID: report.ID, ReportKind: report.Kind, Scope: report.Scope, Status: report.Status, Created: report.Created,
		PolicyCurrent:   report.PolicyDigest == keyedID(s.key, policyDigest(s.Policy)),
		CheckoutCurrent: record.Root == s.Root,
		Audit:           report.Audit, PublicOnly: report.PublicOnly, Rescanned: request.Rescanned,
		Sections: sections, Top: request.Top,
		Filters: SummaryFilters{Dispositions: request.Dispositions, Rules: request.Rules, Categories: request.Categories, Paths: request.Paths},
		Gaps:    report.Gaps, Skipped: report.Skipped,
		ValuesShown: request.Values, ValuesTruncated: request.Values && record.ValuesTruncated,
	}

	findings := filterFindings(report.Findings, request)
	summary.Totals = SummaryTotals{Gaps: len(report.Gaps), Skipped: len(report.Skipped), ScannedFiles: report.Files}
	severities := map[string]*SeveritySummary{}
	for _, disposition := range []string{string(Block), string(Warn), "accepted"} {
		severities[disposition] = &SeveritySummary{Disposition: disposition}
	}
	rules := map[string]*RuleSummary{}
	ruleFiles := map[string]map[string]bool{}
	ruleValues := map[string]map[string]bool{}
	ruleMissingValue := map[string]bool{}
	files := map[string]*FileSummary{}
	fileRules := map[string]map[string]bool{}
	categories := map[string]*CategorySummary{}
	categoryRules := map[string]map[string]bool{}
	for _, finding := range findings {
		summary.Totals.Findings++
		summary.Totals.Occurrences += finding.Occurrences
		switch finding.Disposition {
		case string(Block):
			summary.Totals.Blocked++
		case string(Warn):
			summary.Totals.Warnings++
		case "accepted":
			summary.Totals.Accepted++
		}
		severity := severities[finding.Disposition]
		if severity == nil {
			severity = &SeveritySummary{Disposition: finding.Disposition}
			severities[finding.Disposition] = severity
		}
		severity.Findings++
		severity.Occurrences += finding.Occurrences

		ruleKey := finding.Category + "\x00" + finding.Rule
		rule := rules[ruleKey]
		if rule == nil {
			rule = &RuleSummary{Rule: finding.Rule, Category: finding.Category}
			rules[ruleKey], ruleFiles[ruleKey], ruleValues[ruleKey] = rule, map[string]bool{}, map[string]bool{}
		}
		rule.Disposition = worseDisposition(rule.Disposition, finding.Disposition)
		rule.Findings++
		rule.Occurrences += finding.Occurrences
		ruleFiles[ruleKey][finding.File] = true
		if finding.ValueID == "" {
			ruleMissingValue[ruleKey] = true
		} else {
			ruleValues[ruleKey][finding.ValueID] = true
		}

		file := files[finding.File]
		if file == nil {
			file = &FileSummary{File: finding.File}
			files[finding.File], fileRules[finding.File] = file, map[string]bool{}
		}
		file.Disposition = worseDisposition(file.Disposition, finding.Disposition)
		file.Findings++
		file.Occurrences += finding.Occurrences
		fileRules[finding.File][finding.Rule] = true

		category := categories[finding.Category]
		if category == nil {
			category = &CategorySummary{Category: finding.Category}
			categories[finding.Category], categoryRules[finding.Category] = category, map[string]bool{}
		}
		category.Disposition = worseDisposition(category.Disposition, finding.Disposition)
		category.Findings++
		category.Occurrences += finding.Occurrences
		categoryRules[finding.Category][finding.Rule] = true
	}
	summary.Totals.Files, summary.Totals.Rules = len(files), len(rules)

	selected := map[string]bool{}
	for _, section := range sections {
		selected[section] = true
	}
	if selected[SummaryBySeverity] {
		for _, severity := range severities {
			summary.Severities = append(summary.Severities, *severity)
		}
		sort.Slice(summary.Severities, func(i, j int) bool {
			a, b := summary.Severities[i], summary.Severities[j]
			if dispositionRank(a.Disposition) != dispositionRank(b.Disposition) {
				return dispositionRank(a.Disposition) < dispositionRank(b.Disposition)
			}
			return a.Disposition < b.Disposition
		})
	}
	if selected[SummaryByRule] {
		masked := map[string]ValueSummary{}
		for _, value := range record.Values {
			masked[value.ValueID] = value
		}
		for key, rule := range rules {
			rule.Files = len(ruleFiles[key])
			if !ruleMissingValue[key] {
				distinct := len(ruleValues[key])
				rule.DistinctValues = &distinct
			}
			if request.Values {
				rule.Values, rule.OmittedValues = ruleValueSummaries(findings, rule, masked, request.Top)
			}
			summary.Rules = append(summary.Rules, *rule)
		}
		sort.Slice(summary.Rules, func(i, j int) bool {
			a, b := summary.Rules[i], summary.Rules[j]
			return rankedBefore(a.Disposition, b.Disposition, a.Occurrences, b.Occurrences, a.Findings, b.Findings, a.Rule+"\x00"+a.Category, b.Rule+"\x00"+b.Category)
		})
		summary.Rules, summary.Omitted.Rules = truncateTop(summary.Rules, request.Top)
	}
	if selected[SummaryByFile] {
		for key, file := range files {
			for rule := range fileRules[key] {
				file.Rules = append(file.Rules, rule)
			}
			sort.Strings(file.Rules)
			summary.Files = append(summary.Files, *file)
		}
		sort.Slice(summary.Files, func(i, j int) bool {
			a, b := summary.Files[i], summary.Files[j]
			return rankedBefore(a.Disposition, b.Disposition, a.Occurrences, b.Occurrences, a.Findings, b.Findings, a.File, b.File)
		})
		summary.Files, summary.Omitted.Files = truncateTop(summary.Files, request.Top)
	}
	if selected[SummaryByCategory] {
		for key, category := range categories {
			category.Rules = len(categoryRules[key])
			summary.Categories = append(summary.Categories, *category)
		}
		sort.Slice(summary.Categories, func(i, j int) bool {
			a, b := summary.Categories[i], summary.Categories[j]
			return rankedBefore(a.Disposition, b.Disposition, a.Occurrences, b.Occurrences, a.Findings, b.Findings, a.Category, b.Category)
		})
		summary.Categories, summary.Omitted.Categories = truncateTop(summary.Categories, request.Top)
	}
	if request.Findings {
		summary.Findings = append([]Finding(nil), findings...)
		sort.SliceStable(summary.Findings, func(i, j int) bool {
			a, b := summary.Findings[i], summary.Findings[j]
			return rankedBefore(a.Disposition, b.Disposition, a.Occurrences, b.Occurrences, 0, 0, fmt.Sprintf("%s\x00%09d\x00%s", a.File, a.Line, a.Rule), fmt.Sprintf("%s\x00%09d\x00%s", b.File, b.Line, b.Rule))
		})
		summary.Findings, summary.Omitted.Findings = truncateTop(summary.Findings, request.Top)
	}
	return summary, nil
}

func (s *Service) loadReport(ctx context.Context, id string) (scanRecord, error) {
	var record scanRecord
	if err := s.load(ctx, id, &record); err != nil {
		return scanRecord{}, err
	}
	if record.Report.ID != id || (record.Report.Kind != "hygiene_scan" && record.Report.Kind != "hygiene_snapshot") {
		return scanRecord{}, errors.New("hygiene record is not a scan report")
	}
	if record.Report.RepoID != s.RepoID {
		return scanRecord{}, ErrStale
	}
	return record, nil
}

func filterFindings(findings []Finding, request SummaryRequest) []Finding {
	matches := func(values []string, value string) bool {
		if len(values) == 0 {
			return true
		}
		for _, candidate := range values {
			if candidate == value {
				return true
			}
		}
		return false
	}
	var out []Finding
	for _, finding := range findings {
		if !matches(request.Dispositions, finding.Disposition) || !matches(request.Rules, finding.Rule) || !matches(request.Categories, finding.Category) {
			continue
		}
		if len(request.Paths) > 0 {
			matched := false
			for _, pattern := range request.Paths {
				matched = matched || pathMatches(pattern, finding.File)
			}
			if !matched {
				continue
			}
		}
		out = append(out, finding)
	}
	return out
}

func ruleValueSummaries(findings []Finding, rule *RuleSummary, masked map[string]ValueSummary, top int) ([]ValueSummary, int) {
	byID := map[string]*ValueSummary{}
	files := map[string]map[string]bool{}
	for _, finding := range findings {
		if finding.Rule != rule.Rule || finding.Category != rule.Category || finding.ValueID == "" {
			continue
		}
		value := byID[finding.ValueID]
		if value == nil {
			known, ok := masked[finding.ValueID]
			if !ok {
				continue
			}
			value = &ValueSummary{ValueID: known.ValueID, Rule: known.Rule, Category: known.Category, Masked: known.Masked, Length: known.Length}
			byID[finding.ValueID], files[finding.ValueID] = value, map[string]bool{}
		}
		value.Findings++
		value.Occurrences += finding.Occurrences
		files[finding.ValueID][finding.File] = true
	}
	out := make([]ValueSummary, 0, len(byID))
	for id, value := range byID {
		value.Files = len(files[id])
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Occurrences != out[j].Occurrences {
			return out[i].Occurrences > out[j].Occurrences
		}
		if out[i].Findings != out[j].Findings {
			return out[i].Findings > out[j].Findings
		}
		return out[i].ValueID < out[j].ValueID
	})
	return truncateTop(out, top)
}

func rankedBefore(aDisposition, bDisposition string, aOccurrences, bOccurrences, aFindings, bFindings int, aName, bName string) bool {
	if dispositionRank(aDisposition) != dispositionRank(bDisposition) {
		return dispositionRank(aDisposition) < dispositionRank(bDisposition)
	}
	if aOccurrences != bOccurrences {
		return aOccurrences > bOccurrences
	}
	if aFindings != bFindings {
		return aFindings > bFindings
	}
	return strings.Compare(aName, bName) < 0
}

func truncateTop[T any](items []T, top int) ([]T, int) {
	if top <= 0 || len(items) <= top {
		return items, 0
	}
	return items[:top], len(items) - top
}

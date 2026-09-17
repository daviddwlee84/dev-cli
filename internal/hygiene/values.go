package hygiene

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
)

const (
	maxCapturedValues     = 10000
	maxCapturedValueBytes = 64 << 10
	maxCapturedBytes      = 16 << 20
	maxValueMetadataBytes = 4096
	maxValueSamples       = 20
	maxValueContext       = 120
	maxValueContextBytes  = maxCapturedValueBytes + 2*maxValueContext + 2*len("…")
	maxValuesReviewBytes  = 64 << 20
	valuesReviewSuffix    = ".values.review.txt"
)

// ValueSummary is one distinct matched value in masked form. Raw values are
// written only to the report's private values review file.
type ValueSummary struct {
	ValueID     string `json:"value_id"`
	Rule        string `json:"rule"`
	Category    string `json:"category"`
	Masked      string `json:"masked"`
	Length      int    `json:"length"`
	Occurrences int    `json:"occurrences"`
	Files       int    `json:"files"`
	Findings    int    `json:"findings"`
}

type valueCollector struct {
	entries   map[string]*collectedValue
	order     []string
	bytes     int // Retained raw values, sample context and location/rule metadata.
	truncated bool
}

type collectedValue struct {
	rule, category, raw string
	occurrences         int
	files, findings     map[string]bool
	samples             []valueSample
}

type valueSample struct {
	file, commit, context string
	line                  int
}

func newValueCollector() *valueCollector {
	return &valueCollector{entries: map[string]*collectedValue{}}
}

// captureValue records one occurrence of a finding's raw value when the scan
// was asked to capture values. Callers pass the finding ID returned by add.
func (b *scanBuilder) captureValue(findingID, rule, category, file, commit string, line int, value, context string) {
	b.captureValueContext(findingID, rule, category, file, commit, line, value, func() string { return context })
}

// Context is built only for a retained sample. Whole values that exceed a
// bound are skipped, never shortened into an apparently complete raw value.
func (b *scanBuilder) captureValueContext(findingID, rule, category, file, commit string, line int, value string, context func() string) {
	if b.values == nil || findingID == "" {
		return
	}
	if len(value) > maxCapturedValueBytes || len(rule) > maxValueMetadataBytes || len(category) > maxValueMetadataBytes {
		b.values.truncated = true
		return
	}
	valueID := keyedID(b.s.key, "value", category, rule, value)
	entry := b.values.entries[valueID]
	if entry == nil {
		size := len(value) + len(rule) + len(category)
		if len(b.values.entries) >= maxCapturedValues || size > maxCapturedBytes-b.values.bytes {
			b.values.truncated = true
			return
		}
		entry = &collectedValue{rule: strings.Clone(rule), category: strings.Clone(category), raw: strings.Clone(value), files: map[string]bool{}, findings: map[string]bool{}}
		b.values.entries[valueID] = entry
		b.values.order = append(b.values.order, valueID)
		b.values.bytes += size
	}
	// Counts remain exact after the sample/byte budget is exhausted. File keys
	// do not retain raw paths (or a much larger backing string) for counting.
	entry.occurrences++
	entry.files[keyedID(b.s.key, "file", file)] = true
	entry.findings[findingID] = true
	locationBytes := len(file) + len(commit)
	if len(entry.samples) >= maxValueSamples || len(file) > maxValueMetadataBytes || len(commit) > maxValueMetadataBytes || locationBytes > maxCapturedBytes-b.values.bytes {
		b.values.truncated = true
		return
	}
	text := ""
	if context != nil {
		text = context()
	}
	size := locationBytes + len(text)
	if len(text) > maxValueContextBytes || size > maxCapturedBytes-b.values.bytes {
		b.values.truncated = true
		return
	}
	entry.samples = append(entry.samples, valueSample{file: strings.Clone(file), commit: strings.Clone(commit), line: line, context: strings.Clone(text)})
	b.values.bytes += size
}

// lineContext searches only the bounded windows around a bounded match, not
// the entire containing line for every occurrence in a large single-line file.
func lineContext(data []byte, start, end int) string {
	if start < 0 || end > len(data) || start > end || end-start > maxCapturedValueBytes {
		return ""
	}
	from := max(0, start-maxValueContext)
	to := end + min(len(data)-end, maxValueContext)
	if i := bytes.LastIndexByte(data[from:start], '\n'); i >= 0 {
		from += i + 1
	}
	if i := bytes.IndexByte(data[end:to], '\n'); i >= 0 {
		to = end + i
	}
	prefix, suffix := "", ""
	if from > 0 && data[from-1] != '\n' {
		prefix = "…"
	}
	if to < len(data) && data[to] != '\n' {
		suffix = "…"
	}
	return prefix + string(data[from:to]) + suffix
}

// The raw capture budget does not include escaping expansion or formatted
// headings. Bound the rendered buffer too, before each append.
type valuesReviewBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *valuesReviewBuffer) Write(p []byte) (int, error) {
	if b.err == nil && len(p) > b.limit-b.Len() {
		b.err = errors.New("values review exceeds byte limit; narrow the scan")
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.Buffer.Write(p)
}

// writeValuesReview persists raw values and context only to a private review
// file and stores masked metadata plus a keyed digest in the scan record.
func (s *Service) writeValuesReview(ctx context.Context, record *scanRecord, values *valueCollector) error {
	review := valuesReviewBuffer{limit: min(maxValuesReviewBytes, MaxRecordBytes)}
	fmt.Fprintf(&review, "PRIVATE REVIEW: hygiene_values\nReport: %s\nNever paste this file into chat, Git or CI logs. It contains raw matched values.\n", record.Report.ID)
	ids := append([]string(nil), values.order...)
	sort.SliceStable(ids, func(i, j int) bool {
		a, c := values.entries[ids[i]], values.entries[ids[j]]
		if a.rule != c.rule {
			return a.rule < c.rule
		}
		return a.occurrences > c.occurrences
	})
	record.Values = make([]ValueSummary, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if review.err != nil {
			return review.err
		}
		entry := values.entries[id]
		record.Values = append(record.Values, ValueSummary{
			ValueID: id, Rule: s.displayPath(entry.rule), Category: entry.category,
			Masked: s.maskValue(entry.category, entry.rule, entry.raw), Length: utf8.RuneCountInString(entry.raw),
			Occurrences: entry.occurrences, Files: len(entry.files), Findings: len(entry.findings),
		})
		fmt.Fprintf(&review, "\n=== %s · %s · value %s · %d occurrences in %d files ===\n%s\n",
			entry.rule, entry.category, id[:12], entry.occurrences, len(entry.files), escapeReview(entry.raw))
		for _, sample := range entry.samples {
			location := fmt.Sprintf("%s:%d", sample.file, sample.line)
			if sample.commit != "" {
				location += " @" + sample.commit
			}
			fmt.Fprintf(&review, "  %s  %s\n", escapeReview(location), escapeReview(sample.context))
		}
	}
	if values.truncated {
		fmt.Fprint(&review, "\n(values or samples were skipped at the capture limit; retained values are complete)\n")
	}
	if review.err != nil {
		return review.err
	}
	name := record.Report.ID + valuesReviewSuffix
	if err := configedit.WritePrivate(ctx, filepath.Join(s.Dir, name), review.Bytes(), false); err != nil {
		return err
	}
	record.ValuesReview = name
	record.ValuesDigest = keyedID(s.key, review.String())
	record.ValuesTruncated = values.truncated
	record.ValuesStatus = "complete"
	if values.truncated {
		record.ValuesStatus = "truncated"
	}
	return nil
}

func escapeReview(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return '�'
		}
		return r
	}, text)
}

// maskValue renders a value so a reader can tell values apart without seeing
// them. Masks avoid the shapes public sanitization redacts, so they survive
// output sanitization unchanged.
func (s *Service) maskValue(category, rule, value string) string {
	var masked string
	switch {
	case category == "known":
		masked = fmt.Sprintf("[private:%d]", utf8.RuneCountInString(value))
	case category == "secret":
		masked = maskEnds(value, 12, 2)
	case rule == "privacy-email":
		masked = maskEmail(value)
	case rule == "privacy-ip" || rule == "privacy-ipv6":
		masked = maskIP(value)
	case rule == "privacy-home-path":
		masked = maskHomePath(value)
	default:
		masked = fmt.Sprintf("•••(%d)", utf8.RuneCountInString(value))
	}
	return s.displayPath(masked)
}

func maskEnds(value string, minimum, keep int) string {
	runes := []rune(value)
	if len(runes) < minimum {
		return fmt.Sprintf("•••(%d)", len(runes))
	}
	return fmt.Sprintf("%s•••%s(%d)", string(runes[:keep]), string(runes[len(runes)-keep:]), len(runes))
}

func maskEmail(value string) string {
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || domain == "" {
		return fmt.Sprintf("•••(%d)", utf8.RuneCountInString(value))
	}
	masked := firstRune(local) + "•••@"
	if dot := strings.LastIndexByte(domain, '.'); dot > 0 {
		return masked + firstRune(domain) + "•••" + domain[dot:]
	}
	return masked + "•••"
}

func maskIP(value string) string {
	address, err := netip.ParseAddr(strings.SplitN(value, "%", 2)[0])
	if err != nil {
		return fmt.Sprintf("•••(%d)", utf8.RuneCountInString(value))
	}
	if address.Is4() {
		octets := address.As4()
		return fmt.Sprintf("%d.%d.•.•", octets[0], octets[1])
	}
	first, _, _ := strings.Cut(address.String(), ":")
	return first + ":•••"
}

func maskHomePath(value string) string {
	normalized := strings.ReplaceAll(value, `\`, "/")
	for _, prefix := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(normalized, prefix) {
			return strings.Trim(prefix, "/") + "/" + firstRune(normalized[len(prefix):]) + "•••"
		}
	}
	if index := strings.Index(normalized, ":/Users/"); index == 1 {
		return "Users/" + firstRune(normalized[len(":/Users/")+1:]) + "•••"
	}
	return fmt.Sprintf("•••(%d)", utf8.RuneCountInString(value))
}

func firstRune(value string) string {
	r, size := utf8.DecodeRuneInString(value)
	if size == 0 || r == utf8.RuneError || !unicode.IsPrint(r) {
		return ""
	}
	return string(r)
}

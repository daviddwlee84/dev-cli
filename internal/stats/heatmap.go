package stats

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// shades run from "no activity" to "a lot", using block characters so the
// chart reads at a glance in any terminal font.
var shades = []rune{'·', '░', '▒', '▓', '█'}

// HeatmapOptions tunes the rendering.
type HeatmapOptions struct {
	Since time.Time
	Until time.Time
	// Legend appends the "Less ░ ▒ ▓ █ More" scale.
	Legend bool
	// WeekdayLabels prints Mon/Wed/Fri down the left edge.
	WeekdayLabels bool
}

// Heatmap renders a GitHub-style contribution grid: one column per week, one
// row per weekday, most recent week on the right.
//
// Scaling is by quartile of the *active* days rather than against the maximum.
// One 12-hour outlier would otherwise flatten every ordinary day to the
// lightest shade and hide the pattern the chart exists to show.
func Heatmap(totals map[string]int, opts HeatmapOptions) string {
	if opts.Until.IsZero() {
		opts.Until = time.Now()
	}
	if opts.Since.IsZero() {
		opts.Since = opts.Until.AddDate(-1, 0, 0)
	}

	start := startOfWeek(opts.Since)
	end := opts.Until
	thresholds := quartiles(totals)

	// Build the grid: grid[weekday][week].
	var weeks []time.Time
	for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
		weeks = append(weeks, d)
	}
	var b strings.Builder

	b.WriteString(monthHeader(weeks, opts.WeekdayLabels))
	for wd := 0; wd < 7; wd++ {
		label := "       "
		if opts.WeekdayLabels {
			switch wd {
			case 1:
				label = "   Mon "
			case 3:
				label = "   Wed "
			case 5:
				label = "   Fri "
			}
		} else {
			label = ""
		}
		var row strings.Builder
		for _, weekStart := range weeks {
			day := weekStart.AddDate(0, 0, wd)
			if day.Before(opts.Since) || day.After(end) {
				row.WriteRune(' ')
				continue
			}
			row.WriteRune(shadeFor(totals[day.Format(dayFormat)], thresholds))
		}
		b.WriteString(label)
		// Trailing blanks are days that have not happened yet; printing them
		// leaves invisible whitespace that breaks copy-paste and diffs.
		b.WriteString(strings.TrimRight(row.String(), " "))
		b.WriteByte('\n')
	}

	if opts.Legend {
		pad := ""
		if opts.WeekdayLabels {
			pad = "       "
		}
		fmt.Fprintf(&b, "\n%sLess %c %c %c %c More\n", pad, shades[1], shades[2], shades[3], shades[4])
	}
	return b.String()
}

// monthHeader labels the week columns where a new month begins.
//
// A week is attributed to the month containing its midpoint, so a week
// straddling a boundary is labelled once and on the right side. A month is
// only labelled when it owns enough columns to fit its name without colliding
// with the next one — a partial month at either edge is left unlabelled rather
// than shown in the wrong place.
func monthHeader(weeks []time.Time, indent bool) string {
	row := make([]byte, len(weeks))
	for i := range row {
		row[i] = ' '
	}
	// Run-length encode the months across the columns.
	type run struct {
		month time.Month
		start int
		width int
	}
	var runs []run
	for i, w := range weeks {
		m := w.AddDate(0, 0, 3).Month() // midpoint of the week
		if n := len(runs); n > 0 && runs[n-1].month == m {
			runs[n-1].width++
			continue
		}
		runs = append(runs, run{month: m, start: i, width: 1})
	}
	const label = 3
	for _, r := range runs {
		// Needs room for the name plus a separating space before the next run.
		if r.width < label+1 || r.start+label > len(row) {
			continue
		}
		copy(row[r.start:], r.month.String()[:label])
	}
	prefix := ""
	if indent {
		prefix = "       "
	}
	return prefix + strings.TrimRight(string(row), " ") + "\n"
}

func allSpace(b []byte) bool {
	for _, c := range b {
		if c != ' ' {
			return false
		}
	}
	return true
}

func shadeFor(seconds int, thresholds [3]int) rune {
	switch {
	case seconds <= 0:
		return shades[0]
	case seconds <= thresholds[0]:
		return shades[1]
	case seconds <= thresholds[1]:
		return shades[2]
	case seconds <= thresholds[2]:
		return shades[3]
	default:
		return shades[4]
	}
}

// quartiles returns the 25th, 50th and 75th percentile of the days that had
// any activity at all.
func quartiles(totals map[string]int) [3]int {
	var vals []int
	for _, v := range totals {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return [3]int{1, 2, 3}
	}
	sort.Ints(vals)
	at := func(p float64) int {
		i := int(float64(len(vals)-1) * p)
		return vals[i]
	}
	q := [3]int{at(0.25), at(0.50), at(0.75)}
	// Keep the thresholds strictly increasing so distinct values map to
	// distinct shades even on a tiny sample.
	for i := 1; i < 3; i++ {
		if q[i] <= q[i-1] {
			q[i] = q[i-1] + 1
		}
	}
	return q
}

func startOfWeek(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -int(d.Weekday()))
}

// HumanDuration renders seconds as the compact form a report wants: "4h 20m".
func HumanDuration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// Sparkline renders a compact per-repo bar using the same shade ramp.
func Sparkline(value, max int, width int) string {
	if max <= 0 || width <= 0 {
		return ""
	}
	filled := value * width / max
	if filled == 0 && value > 0 {
		filled = 1
	}
	return strings.Repeat(string(shades[4]), filled) + strings.Repeat(" ", width-filled)
}

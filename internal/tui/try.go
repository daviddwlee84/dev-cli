package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// TryRow is one durable experiment plus the live runtime facts the dashboard
// adds to experiment.Service's filesystem and Git inventory.
type TryRow struct {
	Item        experiment.Item
	Location    *catalog.Location
	Topology    gitx.RecoveryTopology
	TopologyErr error
	SizeTarget  diskusage.Target
	Usage       *diskusage.Usage
	SizeError   error

	Live          bool
	Runtime       string
	RuntimeHandle string
	RuntimeStatus string
}

// Present reports whether this host has an openable checkout for the Try.
func (r TryRow) Present() bool { return r.Item.Live.Present && r.Item.Live.CurrentPath != "" }

// LocationState returns this host's durable disposition, or an empty value when
// the catalog has no location for it.
func (r TryRow) LocationState() catalog.LocationState {
	if r.Location == nil {
		return ""
	}
	return r.Location.State
}

// Where renders the host-local lifecycle state used in rows and structured
// filters. Missing is distinct from evicted: it means metadata expected a path
// that the live probe could not find.
func (r TryRow) Where() string {
	if state := r.LocationState(); state != "" {
		if state == catalog.LocationPresent && !r.Item.Live.Present {
			return "missing"
		}
		return string(state)
	}
	if r.Item.Live.Present {
		return string(catalog.LocationPresent)
	}
	return "other-host"
}

// Mark is the compact intent column shared with the future reclaim view.
func (r TryRow) Mark() string {
	switch {
	case r.Item.Entry != nil && r.Item.Entry.HasTag("important"):
		return "!"
	case r.Item.Entry != nil && r.Item.Entry.HasTag("keep"):
		return "◆"
	default:
		return "·"
	}
}

func (r TryRow) searchText() string {
	values := []string{
		r.Item.SearchText(), r.Where(), r.Runtime, r.RuntimeStatus, r.Topology.Summary(),
		strings.Join(r.Topology.LocalOnlyBranches, " "), strings.Join(r.Topology.UpstreamRemotes, " "),
	}
	return strings.ToLower(strings.Join(values, " "))
}

func (r TryRow) matches(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, term := range strings.Fields(query) {
		key, value, structured := strings.Cut(term, ":")
		if structured {
			switch key {
			case "tag":
				if r.Item.Entry == nil || !r.Item.Entry.HasTag(value) {
					return false
				}
				continue
			case "phase":
				if string(r.Item.Phase) != value {
					return false
				}
				continue
			case "where":
				if r.Where() != value {
					return false
				}
				continue
			case "git":
				if !strings.Contains(strings.ToLower(tryGitSummary(r)), value) {
					return false
				}
				continue
			case "remote":
				if !strings.Contains(strings.ToLower(r.Topology.Summary()), value) {
					return false
				}
				continue
			case "size":
				if !matchesSize(r.Usage, value) {
					return false
				}
				continue
			}
		}
		if !strings.Contains(r.searchText(), term) {
			return false
		}
	}
	return true
}

func tryGitSummary(row TryRow) string {
	switch {
	case row.Item.Live.DiscoverError != nil:
		return "error"
	case row.Item.Live.Repo == nil:
		return "—"
	case row.Item.Live.StatusError != nil || row.Item.Live.Status == nil:
		return "repo ?"
	default:
		return row.Item.Live.Status.Summary()
	}
}

func tryActivityAge(row TryRow) string {
	return latestAge(row.Item.Activity())
}

func (m Model) renderTries() string {
	rows := m.visibleTries()
	if len(rows) == 0 {
		if m.loadingLocal {
			return "  " + styleDim.Render("Loading experiments…") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No Try matches /"+m.filter) + "\n"
		}
		if m.showAllTries {
			return "  " + styleDim.Render("No experiment history recorded.") + "\n"
		}
		return "  " + styleDim.Render("No active Tries. Press n to create one, or a to include history.") + "\n"
	}
	contentWidth := m.width - 2
	if contentWidth < 40 {
		contentWidth = 40
	}
	var header string
	var formatRow func(TryRow) string
	switch {
	case contentWidth >= 96:
		nameWidth := clamp(contentWidth-78, 18, 36)
		tagWidth := contentWidth - nameWidth - 68
		header = fmt.Sprintf("  %-2s  %-*s  %-10s  %-10s  %-16s  %-7s  %-9s  %s",
			"", nameWidth, "TRY", "PHASE", "WHERE", "GIT", "LAST", "SIZE", "TAGS")
		formatRow = func(row TryRow) string {
			return fmt.Sprintf("%-2s  %-*s  %-10s  %-10s  %-16s  %-7s  %-9s  %s",
				row.Mark(), nameWidth, pad(row.Item.DisplayName(), nameWidth),
				pad(string(row.Item.Phase), 10), pad(row.Where(), 10),
				pad(tryGitSummary(row), 16), pad(tryActivityAge(row), 7),
				pad(sizeCell(row.Usage, row.SizeError, row.SizeTarget), 9),
				pad(strings.Join(row.Item.Tags, ","), tagWidth))
		}
	case contentWidth >= 72:
		nameWidth := clamp(contentWidth-52, 16, 34)
		header = fmt.Sprintf("  %-2s  %-*s  %-10s  %-14s  %-7s  %s",
			"", nameWidth, "TRY", "WHERE", "GIT", "LAST", "SIZE")
		formatRow = func(row TryRow) string {
			return fmt.Sprintf("%-2s  %-*s  %-10s  %-14s  %-7s  %s",
				row.Mark(), nameWidth, pad(row.Item.DisplayName(), nameWidth),
				pad(row.Where(), 10), pad(tryGitSummary(row), 14),
				pad(tryActivityAge(row), 7), sizeCell(row.Usage, row.SizeError, row.SizeTarget))
		}
	default:
		nameWidth := clamp(contentWidth-25, 12, 30)
		header = fmt.Sprintf("  %-2s  %-*s  %-8s  %s",
			"", nameWidth, "TRY", "WHERE", "SIZE")
		formatRow = func(row TryRow) string {
			return fmt.Sprintf("%-2s  %-*s  %-8s  %s",
				row.Mark(), nameWidth, pad(row.Item.DisplayName(), nameWidth),
				pad(row.Where(), 8), sizeCell(row.Usage, row.SizeError, row.SizeTarget))
		}
	}

	var builder strings.Builder
	builder.WriteString(styleHeader.Render(header) + "\n")
	from, to := m.window(len(rows))
	for index := from; index < to; index++ {
		row := rows[index]
		line := formatRow(row)
		styled := line
		switch {
		case row.Where() != string(catalog.LocationPresent) || row.Item.Phase != catalog.PhaseActive:
			styled = styleDim.Render(line)
		case row.Item.Live.Status != nil && row.Item.Live.Status.Dirty():
			styled = styleDirty.Render(line)
		case row.Live:
			styled = styleLive.Render(line)
		}
		builder.WriteString(m.renderLine(index, line, styled))
	}
	builder.WriteString(m.scrollNote(len(rows), from, to))
	return builder.String()
}

// TryAction is an operation requested by a dashboard form. Domain validation
// remains in experiment.Service; these names only describe user intent.
type TryAction string

const (
	TryOpen       TryAction = "open"
	TryCreate     TryAction = "create"
	TryMark       TryAction = "mark"
	TryDeprecate  TryAction = "deprecate"
	TryReactivate TryAction = "reactivate"
	TryArchive    TryAction = "archive"
	TryRestore    TryAction = "restore"
	TryGraduate   TryAction = "graduate"
)

// TryRequest is the normalized payload produced by a TUI overlay.
type TryRequest struct {
	Action TryAction
	ID     string

	Name     string
	Clone    string
	NoGit    bool
	Tags     []string
	Note     string
	Category string
	To       string
}

// TryActionResult tells the model what local snapshots need refreshing. CD is
// honored after Bubble Tea leaves the alternate screen.
type TryActionResult struct {
	Status       string
	CD           string
	RefreshRepos bool
}

// TryActions groups Try-specific callbacks instead of continuously widening the
// dashboard's top-level Actions surface.
type TryActions struct {
	Reload func(ctx context.Context, includeAll bool) ([]TryRow, error)
	Apply  func(ctx context.Context, request TryRequest) (TryActionResult, error)
}

func nextSort(current string, orders []string) string {
	for index, order := range orders {
		if order == current {
			return orders[(index+1)%len(orders)]
		}
	}
	if len(orders) > 1 {
		return orders[1]
	}
	if len(orders) == 1 {
		return orders[0]
	}
	return ""
}

func sortTryRows(rows []TryRow, sortBy string, reverse bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		cmp := 0
		switch sortBy {
		case "name":
			cmp = strings.Compare(strings.ToLower(left.Item.DisplayName()), strings.ToLower(right.Item.DisplayName()))
		case "phase":
			leftKey := string(left.Item.Phase) + "/" + left.Where()
			rightKey := string(right.Item.Phase) + "/" + right.Where()
			cmp = strings.Compare(leftKey, rightKey)
		case "size":
			switch {
			case left.Usage != nil && right.Usage == nil:
				cmp = -1
			case left.Usage == nil && right.Usage != nil:
				cmp = 1
			case left.Usage != nil && right.Usage != nil && left.Usage.OwnedBytes > right.Usage.OwnedBytes:
				cmp = -1
			case left.Usage != nil && right.Usage != nil && left.Usage.OwnedBytes < right.Usage.OwnedBytes:
				cmp = 1
			}
		default: // activity
			leftActivity, rightActivity := left.Item.Activity(), right.Item.Activity()
			switch {
			case left.Item.Entry != nil && left.Item.Entry.HasTag("important") &&
				(right.Item.Entry == nil || !right.Item.Entry.HasTag("important")):
				cmp = -1
			case right.Item.Entry != nil && right.Item.Entry.HasTag("important") &&
				(left.Item.Entry == nil || !left.Item.Entry.HasTag("important")):
				cmp = 1
			case leftActivity.After(rightActivity):
				cmp = -1
			case rightActivity.After(leftActivity):
				cmp = 1
			}
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(left.Item.DisplayName()), strings.ToLower(right.Item.DisplayName()))
		}
		if cmp == 0 {
			cmp = strings.Compare(left.Item.ID, right.Item.ID)
		}
		if reverse {
			return cmp > 0
		}
		return cmp < 0
	})
}

func splitTagInput(value string) []string {
	return catalog.NormalizeTags(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	}))
}

func parseBoolInput(value string, fallback bool) (bool, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback, true
	}
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed, true
	}
	switch value {
	case "yes", "y", "on":
		return true, true
	case "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

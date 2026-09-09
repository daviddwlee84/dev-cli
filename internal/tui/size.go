package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/dustin/go-humanize"
)

// SizeActions is the asynchronous disk-usage boundary injected by cli.runTUI.
type SizeActions struct {
	Invalidate func(...diskusage.Target) error
	Start      func(ctx context.Context, targets []diskusage.Target, force bool) diskusage.Load
	Cancel     func(loadID uint64)
}

type sizeMsg struct {
	loadID uint64
	result diskusage.Result
	done   bool
}

type sizeOwner struct {
	key    string
	loadID uint64
}

func (m Model) updateSize(msg sizeMsg) (tea.Model, tea.Cmd) {
	if msg.loadID == 0 {
		return m, nil
	}
	load := m.sizeLoad
	index := -1
	if load.ID != msg.loadID {
		for n, other := range m.scopedSizeLoads {
			if other.ID == msg.loadID {
				load, index = other, n
				break
			}
		}
		if index < 0 {
			return m, nil
		}
	}
	if msg.done {
		if index < 0 {
			m.sizeLoad = diskusage.Load{}
		} else {
			m.scopedSizeLoads = slices.Delete(slices.Clone(m.scopedSizeLoads), index, index+1)
		}
		return m, nil
	}
	for _, owner := range m.sizeOwners {
		if owner.key == msg.result.Key && owner.loadID != msg.loadID {
			return m, waitForSize(load)
		}
	}
	m.applySizeResult(msg.result)
	return m, waitForSize(load)
}

func (m *Model) setSizeOwner(key string, id uint64) {
	if key == "" {
		return
	}
	m.sizeOwners = slices.Clone(m.sizeOwners)
	for n := range m.sizeOwners {
		if m.sizeOwners[n].key == key {
			m.sizeOwners[n].loadID = id
			return
		}
	}
	m.sizeOwners = append(m.sizeOwners, sizeOwner{key, id})
}

func (m Model) beginScopedSizes(targets, invalidated []diskusage.Target) (tea.Model, tea.Cmd) {
	for _, target := range invalidated {
		m.setSizeOwner(target.Key, 0)
	}
	if m.actions.Sizes.Start == nil || len(targets) == 0 {
		return m, nil
	}
	load := m.actions.Sizes.Start(m.baseContext(), targets, true)
	if load.ID == 0 || load.Results == nil {
		return m, nil
	}
	m.scopedSizeLoads = append(slices.Clone(m.scopedSizeLoads), load)
	for _, target := range targets {
		m.setSizeOwner(target.Key, load.ID)
	}
	return m, waitForSize(load)
}

func (m Model) beginSizeLoad(force bool) (Model, tea.Cmd) {
	if m.actions.Sizes.Start == nil {
		return m, nil
	}
	if m.sizeLoad.ID != 0 && m.actions.Sizes.Cancel != nil {
		m.actions.Sizes.Cancel(m.sizeLoad.ID)
	}
	for _, load := range m.scopedSizeLoads {
		if m.actions.Sizes.Cancel != nil {
			m.actions.Sizes.Cancel(load.ID)
		}
	}
	m.scopedSizeLoads, m.sizeOwners = nil, nil
	targets := make([]diskusage.Target, 0, len(m.repos)+len(m.tries))
	for _, row := range m.repos {
		if row.SizeTarget.Checkout != "" {
			targets = append(targets, row.SizeTarget)
		}
	}
	for _, row := range m.tries {
		if row.SizeTarget.Checkout != "" {
			targets = append(targets, row.SizeTarget)
		}
	}
	m.sizeLoad = m.actions.Sizes.Start(m.baseContext(), targets, force)
	if m.sizeLoad.ID == 0 || m.sizeLoad.Results == nil {
		return m, nil
	}
	return m, waitForSize(m.sizeLoad)
}

func waitForSize(load diskusage.Load) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-load.Results
		if !ok {
			return sizeMsg{loadID: load.ID, done: true}
		}
		return sizeMsg{loadID: load.ID, result: result}
	}
}

func (m *Model) applySizeResult(result diskusage.Result) {
	// Bubble Tea copies Model by value; clone slice headers before patching rows so
	// two candidate models never share a mutated backing array.
	m.repos = append([]RepoRow(nil), m.repos...)
	m.tries = append([]TryRow(nil), m.tries...)
	for index := range m.repos {
		if m.repos[index].SizeTarget.Key != result.Key {
			continue
		}
		if result.Err != nil {
			m.repos[index].Usage, m.repos[index].SizeError = nil, result.Err
		} else {
			usage := result.Usage
			m.repos[index].Usage, m.repos[index].SizeError = &usage, nil
		}
	}
	for index := range m.tries {
		if m.tries[index].SizeTarget.Key != result.Key {
			continue
		}
		if result.Err != nil {
			m.tries[index].Usage, m.tries[index].SizeError = nil, result.Err
		} else {
			usage := result.Usage
			m.tries[index].Usage, m.tries[index].SizeError = &usage, nil
		}
	}
}

func matchesSize(usage *diskusage.Usage, expression string) bool {
	if usage == nil {
		return false
	}
	expression = strings.TrimSpace(expression)
	operator := "="
	for _, candidate := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(expression, candidate) {
			operator = candidate
			expression = strings.TrimSpace(strings.TrimPrefix(expression, candidate))
			break
		}
	}
	bytes, err := humanize.ParseBytes(expression)
	if err != nil {
		return false
	}
	owned := uint64(max(usage.OwnedBytes, 0))
	switch operator {
	case ">=":
		return owned >= bytes
	case "<=":
		return owned <= bytes
	case ">":
		return owned > bytes
	case "<":
		return owned < bytes
	default:
		return owned == bytes
	}
}

func sizeDetailLines(usage *diskusage.Usage, err error, target diskusage.Target) []string {
	if target.Checkout == "" {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("  %s  %s", styleDim.Render("size"), styleErr.Render(err.Error()))}
	}
	if usage == nil {
		return []string{fmt.Sprintf("  %s  %s", styleDim.Render("size"), styleDim.Render("measuring…"))}
	}
	lines := []string{
		fmt.Sprintf("  %s  %s logical owned", styleDim.Render("size"), usage.HumanOwned()),
		fmt.Sprintf("  %s  %s", styleDim.Render("checkout"), humanize.IBytes(uint64(max(usage.CheckoutBytes, 0)))),
	}
	if usage.PrivateGitBytes != nil {
		lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("private .git"),
			humanize.IBytes(uint64(max(*usage.PrivateGitBytes, 0)))))
	}
	if usage.SharedGitBytes != nil {
		lines = append(lines, fmt.Sprintf("  %s  %s (not reclaimable with this checkout)",
			styleDim.Render("shared .git"), humanize.IBytes(uint64(max(*usage.SharedGitBytes, 0)))))
	}
	if !usage.Complete {
		lines = append(lines, fmt.Sprintf("  %s  partial; %d unreadable entries", styleDim.Render("measure"), usage.UnreadableEntries))
	}
	return lines
}

func sizeCell(usage *diskusage.Usage, err error, target diskusage.Target) string {
	switch {
	case err != nil:
		return "?"
	case target.Checkout == "":
		return "—"
	case usage == nil:
		return "…"
	default:
		value := usage.HumanOwned()
		if usage.SharedGitBytes != nil {
			value += "+S"
		}
		return value
	}
}

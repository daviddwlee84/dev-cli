package triage

import (
	"fmt"

	"github.com/dustin/go-humanize"
)

func SummarizeLedger(l Ledger) (string, string) {
	done, failed, skipped := 0, 0, 0
	var reclaimed int64
	for _, o := range l.Outcomes {
		reclaimed += o.ReclaimedBytes
		switch o.Status {
		case "completed":
			done++
		case "skipped", "canceled":
			skipped++
		default:
			failed++
		}
	}
	severity := "success"
	if failed > 0 {
		severity = "error"
		if done > 0 {
			severity = "warning"
		}
	} else if done == 0 {
		severity = "info"
	}
	summary := fmt.Sprintf("Triage: %d completed · %d failed · %d skipped", done, failed, skipped)
	if reclaimed > 0 {
		summary += fmt.Sprintf(" · reclaimed about %s", humanize.IBytes(uint64(reclaimed)))
	}
	return summary, severity
}

package triage

import "fmt"

func SummarizeLedger(l Ledger) (string, string) {
	done, failed, skipped := 0, 0, 0
	for _, o := range l.Outcomes {
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
	return fmt.Sprintf("Triage: %d completed · %d failed · %d skipped", done, failed, skipped), severity
}

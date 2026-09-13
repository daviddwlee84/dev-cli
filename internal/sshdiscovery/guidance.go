package sshdiscovery

import (
	"fmt"
	"slices"
)

// LANGuidance explains a completed LAN scan in which nothing was reachable. It
// names a likely cause without claiming to have observed operating-system policy.
func LANGuidance(report Report, goos string) string {
	if report.Source != SourceLAN || report.Probes == nil || !slices.Contains(report.Warnings, WarningNoReachableEndpoints) {
		return ""
	}
	p := *report.Probes
	text := fmt.Sprintf("no LAN endpoint accepted a connection (%d probed: %d refused, %d unreachable, %d timed out", p.Attempted, p.Refused, p.Unreachable, p.Timeout)
	if p.Other > 0 {
		text += fmt.Sprintf(", %d other", p.Other)
	}
	text += ")"
	if goos == "darwin" {
		return text + "; macOS may be blocking Local Network access for this terminal app: allow it in System Settings > Privacy & Security > Local Network, then retry"
	}
	return text + "; check the selected interface, range and local firewall, then retry"
}

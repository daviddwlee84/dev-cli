package sshdiscovery

import "sort"

// MergeReports keeps the newest observation of each exact source identity.
// A partial attempt retains unobserved candidates in their original reports so
// their observation time and completeness are never upgraded by a newer scan.
// A completed scan replaces previous observations of its exact source/scope.
func MergeReports(reports []Report) []Report {
	ordered := append([]Report(nil), reports...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ObservedAt.After(ordered[j].ObservedAt) })
	seen := map[string]bool{}
	complete := map[string]bool{}
	groups := map[string]bool{}
	result := []Report{}
	for _, report := range ordered {
		group := report.Source + "\x00" + report.Scope
		if complete[group] {
			continue
		}
		copy := report
		copy.Candidates = nil
		for _, candidate := range report.Candidates {
			native := candidate.NativeID
			if native == "" {
				native = candidate.ID
			}
			key := group + "\x00" + native
			if seen[key] {
				continue
			}
			seen[key] = true
			candidate.Addresses = append([]string(nil), candidate.Addresses...)
			copy.Candidates = append(copy.Candidates, candidate)
		}
		if len(copy.Candidates) > 0 || !groups[group] {
			result = append(result, copy)
			groups[group] = true
		}
		if report.Complete {
			complete[group] = true
		}
	}
	return result
}

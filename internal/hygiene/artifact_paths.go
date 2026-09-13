package hygiene

import "strings"

// IsArtifactPath conservatively identifies agent-artifact directories in a
// repository-relative spelling. Case and Win32 trailing-dot/space aliases must
// not bypass writer protection, even when the controller uses another OS.
// This only selects writer guards; it never validates or resolves a file path.
func IsArtifactPath(file string) bool {
	for _, component := range strings.Split(strings.ReplaceAll(file, `\`, "/"), "/") {
		component = strings.TrimRight(component, " .")
		for _, directory := range []string{".specstory", ".claude", ".codex", ".cursor", ".opencode", ".specify"} {
			if strings.EqualFold(component, directory) {
				return true
			}
		}
	}
	return false
}

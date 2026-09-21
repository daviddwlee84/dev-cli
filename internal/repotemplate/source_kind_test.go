package repotemplate

import "testing"

func TestExplicitGitSourceDoesNotProbeURLsAsLocalFiles(t *testing.T) {
	for _, source := range []string{"file:///C:/Users/test/source", "file:///tmp/source", "https://example.test/team/repo.git", "ssh://git@example.test/team/repo.git", "git@example.test:team/repo.git", "host:team/repo.git"} {
		if !explicitGitSource(source) {
			t.Errorf("Git source treated as local: %q", source)
		}
	}
	for _, source := range []string{"/tmp/source", "./owner/repo", "../owner/repo", "~/source", `C:\Users\test\source`, `C:relative`, `.\source`, `..\source`, "owner/repo", "./https://example.test"} {
		if explicitGitSource(source) {
			t.Errorf("local source treated as URL: %q", source)
		}
	}
}

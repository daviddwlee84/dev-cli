package repo

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactCloneRefRemovesCredentialUserinfo(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"https://user:secret@example.test/owner/repo.git", "https://example.test/owner/repo.git"},
		{"https://token@example.test/owner/repo.git", "https://example.test/owner/repo.git"},
		{"oauth2:secret@example.test:owner/repo.git", "***@example.test:owner/repo.git"},
		{"git@example.test:owner/repo.git", "git@example.test:owner/repo.git"},
	} {
		if got := RedactCloneRef(test.input); got != test.want {
			t.Errorf("RedactCloneRef(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRedactCloneErrorDoesNotRetainSecret(t *testing.T) {
	ref := "https://user:secret@example.test/owner/repo.git"
	err := RedactCloneError(errors.New("git clone "+ref+": authentication failed for "+ref), ref)
	if strings.Contains(err.Error(), "secret") || strings.Count(err.Error(), "https://example.test/owner/repo.git") != 2 {
		t.Fatalf("redacted error = %q", err)
	}
}

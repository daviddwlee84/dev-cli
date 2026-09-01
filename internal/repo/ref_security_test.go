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

func TestRedactRemoteRefStripsURLSecretsAndControls(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{
			"https://alice:password@example.test/group/repo.git?access_token=query-secret#fragment-secret",
			"https://example.test/group/repo.git",
		},
		{"ssh://git@example.test/group/repo.git?token=secret#fragment", "ssh://example.test/group/repo.git"},
		{"file:///tmp/repo?token=secret#fragment", "file:///tmp/repo"},
		{"git@example.test:group/repo.git?token=secret#fragment", "git@example.test:group/repo.git"},
		{"oauth2:password@example.test:group/repo.git?token=secret", "***@example.test:group/repo.git"},
		{"oauth2:password@example.test/group/repo.git?token=secret", "***@example.test/group/repo.git"},
		{"work:group/repo.git?token=secret#fragment", "work:group/repo.git"},
		{"relative/path?literal#name", "relative/path?literal#name"},
		{"https://example.test/group/repo%0a.git", unsafeRemoteLabel},
		{"https://[malformed/secret", unsafeRemoteLabel},
		{"git@example.test:group/repo.git\x1b[31m", unsafeRemoteLabel},
		{"git@example.test:group/" + string(rune(0x202e)) + "repo.git", unsafeRemoteLabel},
		{string([]byte{'b', 'a', 'd', 0xff}), unsafeRemoteLabel},
	} {
		got := RedactRemoteRef(test.input)
		if got != test.want {
			t.Errorf("RedactRemoteRef(%q) = %q, want %q", test.input, got, test.want)
		}
		if strings.IndexFunc(got, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
			t.Errorf("RedactRemoteRef(%q) retained a control in %q", test.input, got)
		}
	}
}

func TestRedactCloneErrorRemovesQueryAndFragmentCredentials(t *testing.T) {
	ref := "https://user:password@example.test/owner/repo.git?token=query-secret#fragment-secret"
	err := RedactCloneError(errors.New("fatal: unable to access "+ref), ref)
	message := err.Error()
	for _, secret := range []string{"user", "password", "query-secret", "fragment-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("redacted error leaked %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "https://example.test/owner/repo.git") {
		t.Fatalf("redacted error lost safe endpoint: %q", message)
	}
}

func TestRedactCloneErrorReplacesOriginalControlBearingRef(t *testing.T) {
	ref := "https://user:secret@example.test/owner/repo.git\n"
	err := RedactCloneError(errors.New("git clone failed for "+ref), ref)
	if strings.Contains(err.Error(), "secret") || strings.ContainsRune(err.Error(), '\n') || !strings.Contains(err.Error(), unsafeRemoteLabel) {
		t.Fatalf("redacted control-bearing error = %q", err)
	}
}

func TestDisplayRedactionDoesNotChangeCloneNormalization(t *testing.T) {
	ref := "https://user:password@github.com/owner/repo.git?token=raw#raw"
	if got := NormalizeCloneRef(ref); got != ref {
		t.Fatalf("NormalizeCloneRef changed raw clone input: %q", got)
	}
	if got := RedactCloneRef(ref); got == ref || strings.Contains(got, "password") || strings.Contains(got, "token") {
		t.Fatalf("RedactCloneRef did not isolate display: %q", got)
	}
}

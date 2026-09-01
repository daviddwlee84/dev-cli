package forge

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// wrapped rebuilds the error shape run() produces, so the tests match what the
// classifier actually receives rather than a tidied-up string.
func wrapped(bin, args, detail string) error {
	return fmt.Errorf("%s %s: %s: %w", bin, args, detail, errors.New("exit status 1"))
}

func TestClassifyAuthRecognisesSignedOutProviders(t *testing.T) {
	// The exact texts reported from a signed-out machine.
	ghErr := wrapped("gh", "api user/repos --method GET -f per_page=100",
		"gh: Bad credentials (HTTP 401)")
	glabErr := wrapped("glab", "api --hostname gitlab.com projects --method GET -f membership=true",
		"glab: HTTP 401")

	for name, tc := range map[string]struct {
		kind Kind
		bin  string
		err  error
	}{
		"github": {GitHub, "gh", ghErr},
		"gitlab": {GitLab, "glab", glabErr},
	} {
		t.Run(name, func(t *testing.T) {
			classified := classifyAuth(tc.kind, tc.bin, tc.err)
			var authErr *ErrAuth
			if !errors.As(classified, &authErr) {
				t.Fatalf("classifyAuth did not classify %v", classified)
			}
			if !IsAuth(classified) {
				t.Error("IsAuth = false")
			}
			// The headline is what lands in the TUI footer and in the cached
			// provider status, so it must carry the remedy and not the argv.
			message := classified.Error()
			if !strings.Contains(message, "signed out") || !strings.Contains(message, "auth login") {
				t.Errorf("message is not actionable: %q", message)
			}
			for _, leak := range []string{"--method", "-f", "api", "exit status"} {
				if strings.Contains(message, leak) {
					t.Errorf("message leaks %q: %q", leak, message)
				}
			}
			// Detail must still be recoverable for diagnosis.
			if !strings.Contains(errors.Unwrap(classified).Error(), "--method GET") {
				t.Error("unwrapping lost the original command")
			}
		})
	}
}

func TestClassifyAuthLeavesOtherFailuresAlone(t *testing.T) {
	// Each of these is a real failure that signing in again would not fix, so
	// the full diagnostic message must survive untouched.
	for name, detail := range map[string]string{
		"rate limit":     "gh: API rate limit exceeded (HTTP 403)",
		"forbidden":      "gh: Resource not accessible by integration (HTTP 403)",
		"not found":      "gh: Not Found (HTTP 404)",
		"dns":            "dial tcp: lookup api.github.com: no such host",
		"tls":            "net/http: TLS handshake timeout",
		"deadline":       "context deadline exceeded",
		"unknown server": "gh: Internal Server Error (HTTP 500)",
	} {
		t.Run(name, func(t *testing.T) {
			err := wrapped("gh", "api user/repos", detail)
			classified := classifyAuth(GitHub, "gh", err)
			if IsAuth(classified) {
				t.Fatalf("%q was misreported as an authentication failure", detail)
			}
			if classified.Error() != err.Error() {
				t.Errorf("message changed:\n got %q\nwant %q", classified.Error(), err.Error())
			}
		})
	}
}

func TestClassifyAuthIsIdempotentAndNilSafe(t *testing.T) {
	if got := classifyAuth(GitHub, "gh", nil); got != nil {
		t.Fatalf("classifyAuth(nil) = %v", got)
	}
	once := classifyAuth(GitHub, "gh", wrapped("gh", "api user", "gh: HTTP 401"))
	twice := classifyAuth(GitHub, "gh", once)
	if once != twice {
		t.Error("re-classifying wrapped an already-classified error again")
	}
}

func TestErrAuthReusesTheProbeRemediation(t *testing.T) {
	// The login instruction must have exactly one definition, so that a change
	// to authProbe cannot leave the two paths disagreeing.
	t.Setenv("GH_HOST", "github.example.com")
	_, action, _ := authProbe(GitHub)
	classified := classifyAuth(GitHub, "gh", wrapped("gh", "api user", "gh: HTTP 401"))
	if !strings.Contains(classified.Error(), action) {
		t.Errorf("message %q does not carry probe action %q", classified.Error(), action)
	}
}

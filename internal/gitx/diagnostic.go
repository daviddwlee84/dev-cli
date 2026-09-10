package gitx

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

// Diagnostic is safe, bounded display evidence, not retry or mutation authority.
// Raw command arguments and credentials must never be serialized into a ledger.
type Diagnostic struct {
	Code      string `json:"code"`
	Summary   string `json:"summary"`
	Next      string `json:"next"`
	ExitCode  int    `json:"exit_code"`
	Details   string `json:"details,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

var (
	diagnosticANSI        = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
	diagnosticPEM         = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
	diagnosticURL         = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s<>"']+`)
	diagnosticCredentials = regexp.MustCompile(`(?i)((?:authorization|proxy-authorization)\s*[:=]\s*|(?:bearer|basic)\s+)[^\r\n]+`)
	diagnosticAssignments = regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|access[_-]?key|credential)\s*[=:]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	diagnosticTokens      = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|glpat-[A-Za-z0-9_-]+|sk-[A-Za-z0-9_-]{12,}|AKIA[A-Z0-9]{16})\b`)
)

// SafeDiagnosticText also removes terminal escape sequences. Git/hook output
// is untrusted presentation, even when the command itself was authorized.
func SafeDiagnosticText(value string) string {
	value = diagnosticANSI.ReplaceAllString(value, "")
	value = diagnosticPEM.ReplaceAllString(value, "[redacted private material]")
	value = diagnosticURL.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[redacted URL]"
		}
		if u.User != nil {
			u.User = url.User("REDACTED")
		}
		if u.RawQuery != "" {
			u.RawQuery = "REDACTED"
		}
		u.Fragment = ""
		return u.String()
	})
	value = diagnosticCredentials.ReplaceAllString(value, "${1}[REDACTED]")
	value = diagnosticAssignments.ReplaceAllString(value, "${1}[REDACTED]")
	value = diagnosticTokens.ReplaceAllString(value, "[REDACTED]")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, value))
}

func Diagnose(err error) Diagnostic {
	d := Diagnostic{Code: "unknown", Summary: "Git operation failed", Next: "Inspect details or open a shell, then preview again before retrying.", ExitCode: -1}
	var command *Error
	raw := ""
	if errors.As(err, &command) {
		raw = command.Stdout + "\n" + command.Stderr
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		d.ExitCode = exit.ExitCode()
	}
	text := strings.ToLower(raw)
	switch {
	case errors.Is(err, context.Canceled):
		d.Code, d.Summary, d.Next = "canceled", "Git operation was interrupted", "Refresh local facts and verify the remote outcome before retrying."
	case errors.Is(err, context.DeadlineExceeded):
		d.Code, d.Summary, d.Next = "timeout", "Git operation timed out", "Check connectivity and verify the remote outcome before retrying."
	case containsDiagnostic(text, "authentication failed", "permission denied (publickey)", "could not read username", "terminal prompts disabled", "invalid username or password", "could not read password"):
		d.Code, d.Summary, d.Next = "authentication", "Authentication failed or requires interaction", "Open a shell to complete authentication, then return and preview again."
	case containsDiagnostic(text, "host key verification failed", "remote host identification has changed"):
		d.Code, d.Summary, d.Next = "host-verification", "SSH host identity could not be verified", "Verify the host identity in a shell; this batch does not change host-key policy."
	case containsDiagnostic(text, "non-fast-forward", "fetch first", "stale info"):
		d.Code, d.Summary, d.Next = "remote-ahead", "Remote branch changed; push was rejected", "Choose a separate Fetch round, then inspect divergence before retrying."
	case containsDiagnostic(text, "hook declined", "pre-receive hook", "protected branch", "repository rule", "gh013", "gh006", "remote rejected"):
		d.Code, d.Summary, d.Next = "remote-policy", "Remote policy or hook rejected the update", "Review the remote message and satisfy its rule before a new preview."
	case containsDiagnostic(text, "could not resolve", "connection refused", "connection timed out", "unable to access", "network is unreachable", "connection reset", "unable to connect"):
		d.Code, d.Summary, d.Next = "connection", "Could not complete the remote connection", "Check the connection and remote address; verify the outcome before retrying."
	case containsDiagnostic(text, "repository not found", "does not appear to be a git repository", "access denied"):
		d.Code, d.Summary, d.Next = "remote-access", "Repository access was denied or the endpoint is unavailable", "Check the remote address and account permissions in a shell."
	case containsDiagnostic(text, "would be overwritten", "local changes", "unmerged", "not possible to fast-forward", "index.lock", "cannot lock ref"):
		d.Code, d.Summary, d.Next = "local-blocker", "Local checkout or refs block this operation", "Inspect and resolve the local blocker individually, then refresh and preview again."
	}
	if raw == "" {
		d.Details = "No Git diagnostic was captured; inspect the operation individually."
		return d
	}
	safe := SafeDiagnosticText(raw)
	const limit = 16 * 1024
	if len(safe) > limit {
		safe = string([]rune(safe)[:min(len([]rune(safe)), 4096)]) + "\n[diagnostic truncated]"
		d.Truncated = true
	}
	d.Details = safe
	return d
}

func containsDiagnostic(text string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(text, v) {
			return true
		}
	}
	return false
}

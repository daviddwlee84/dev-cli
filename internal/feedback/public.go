package feedback

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

var redactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?m)(?:ssh-(?:rsa|dss|ed25519)(?:-cert-v01@openssh.com)?|ecdsa-sha2-[^\s]+|sk-[^\s]+)[ \t]+[A-Za-z0-9+/=]{16,}[^\r\n]*`), "[redacted public key]"},
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), "[redacted private key]"},
	{regexp.MustCompile(`(?i)\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]{12,}|AKIA[A-Z0-9]{16})\b`), "[redacted credential]"},
	{regexp.MustCompile(`(?im)\b(?:password|passwd|passphrase|token|secret|api[_-]?key|authorization)\s*[:=]\s*[^\r\n]+`), "[redacted credential field]"},
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"\x60]+`), "[redacted URL]"},
	{regexp.MustCompile(`(?i)\bSHA256:[A-Za-z0-9+/=]+`), "[redacted fingerprint]"},
	{regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){15}[0-9a-f]{2}\b`), "[redacted fingerprint]"},
	{regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`), "[redacted IP]"},
	{regexp.MustCompile(`(?i)\b(?:[0-9a-f]{0,4}:){2,}[0-9a-f:%.a-z0-9_-]*`), "[redacted IP]"},
	{regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\b`), "[redacted account]"},
	{regexp.MustCompile(`(?i)(?:[A-Z]:[\\/]|~/|/(?:Users|home|private|var|tmp|etc|opt|srv|mnt|Volumes)/)[^\s<>"\x60]+`), "[redacted path]"},
	{regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+(?:com|net|org|io|dev|local|internal|test|corp|lan|edu|cn|tw)\b`), "[redacted host]"},
	{regexp.MustCompile(`(?im)^\s*(?:User|HostName|IdentityFile|HostKeyAlias|ProxyCommand|ProxyJump|IdentityAgent)\s+[^\r\n]+`), "[redacted SSH option]"},
}

// Sanitize removes known identifying forms. Free-text review is still required:
// no regexp can determine whether every arbitrary name in prose is private.
func Sanitize(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, text)
	for _, rule := range redactions {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return text
}
func safeFacts(f Facts) Facts {
	f.Version = Sanitize(f.Version)
	if len(f.Version) > 100 {
		f.Version = "unknown"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9+._-]+$`).MatchString(f.Version) {
		f.Version = "unknown"
	}
	if !map[string]bool{"darwin": true, "linux": true, "windows": true}[f.OS] {
		f.OS = "unknown"
	}
	if !map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true}[f.Arch] {
		f.Arch = "unknown"
	}
	if !map[string]bool{"homebrew": true, "scoop": true, "go-install": true, "standalone": true, "unknown": true}[f.Installation] {
		f.Installation = "unknown"
	}
	return f
}
func publicBody(request DraftRequest) (string, error) {
	facts := safeFacts(request.Facts)
	text := fmt.Sprintf("# %s\n\n%s\n\n## Environment\n\n- dev: %s\n- OS/architecture: %s/%s\n- Installation: %s\n", Sanitize(request.Title), Sanitize(request.Body), facts.Version, facts.OS, facts.Arch, facts.Installation)
	if len(request.Diagnostic) > 0 {
		public, err := sshhost.ParsePublicDiagnosis(request.Diagnostic)
		if err != nil {
			return "", err
		}
		data, _ := json.MarshalIndent(public, "", "  ")
		text += "\n## SSH diagnostic evidence\n\n```json\n" + string(data) + "\n```\n"
	}
	return text, nil
}

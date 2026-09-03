package repo

import (
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const unsafeRemoteLabel = "<unsafe remote>"

// RedactRemoteRef returns a terminal-safe remote reference for public display.
// URL userinfo, query, and fragment components are removed structurally. SSH
// usernames such as git@host remain visible, while an scp-like prefix containing
// a password/token separator is redacted. Invalid UTF-8, controls, malformed
// explicit URLs, and encoded path controls are replaced by a fixed label.
func RedactRemoteRef(ref string) string {
	if !utf8.ValidString(ref) || strings.IndexFunc(ref, isRemoteControl) >= 0 {
		return unsafeRemoteLabel
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	parsed, err := url.Parse(ref)
	explicitURL := strings.Contains(ref, "://")
	if explicitURL && (err != nil || parsed.Scheme == "" || parsed.Host == "" && !strings.EqualFold(parsed.Scheme, "file")) {
		return unsafeRemoteLabel
	}
	if err == nil && parsed.Scheme != "" && (explicitURL || parsed.Host != "" || strings.EqualFold(parsed.Scheme, "file")) {
		path, unescapeErr := url.PathUnescape(parsed.EscapedPath())
		if unescapeErr != nil || !utf8.ValidString(path) || strings.IndexFunc(path, isRemoteControl) >= 0 {
			return unsafeRemoteLabel
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		redacted := parsed.String()
		if !utf8.ValidString(redacted) || strings.IndexFunc(redacted, isRemoteControl) >= 0 {
			return unsafeRemoteLabel
		}
		return redacted
	}
	if err != nil && strings.Contains(ref, "://") {
		return unsafeRemoteLabel
	}

	at := strings.LastIndexByte(ref, '@')
	if at > 0 && strings.Contains(ref[:at], ":") {
		return "***@" + stripRemoteQueryFragment(ref[at+1:])
	}
	if at > 0 {
		colon := strings.IndexByte(ref[at+1:], ':')
		if colon >= 0 {
			colon += at + 1
			return ref[:colon+1] + stripRemoteQueryFragment(ref[colon+1:])
		}
	}
	if colon := strings.IndexByte(ref, ':'); colon > 0 && !strings.ContainsAny(ref[:colon], `/\\`) && !isDrivePrefix(ref[:colon+1]) {
		return ref[:colon+1] + stripRemoteQueryFragment(ref[colon+1:])
	}
	return ref
}

// RedactCloneRef is retained for existing clone results and diagnostics. It
// changes display only; callers continue to pass the original reference to Git.
func RedactCloneRef(ref string) string {
	return RedactRemoteRef(ref)
}

func isRemoteControl(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func isDrivePrefix(value string) bool {
	return len(value) == 2 && value[1] == ':' && (value[0] >= 'a' && value[0] <= 'z' || value[0] >= 'A' && value[0] <= 'Z')
}

func stripRemoteQueryFragment(value string) string {
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		return value[:index]
	}
	return value
}

// RedactCloneError returns an error whose displayed message cannot echo
// credential-bearing clone references passed to Git.
func RedactCloneError(err error, refs ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	refs = append([]string(nil), refs...)
	sort.SliceStable(refs, func(i, j int) bool { return len(refs[i]) > len(refs[j]) })
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		message = strings.ReplaceAll(message, ref, RedactCloneRef(ref))
		trimmed := strings.TrimSpace(ref)
		if trimmed != "" && trimmed != ref {
			message = strings.ReplaceAll(message, trimmed, RedactCloneRef(trimmed))
		}
	}
	return errors.New(message)
}

package repo

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

// RedactCloneRef removes URL userinfo before a clone source is rendered or
// persisted in a result. SSH usernames such as git@host remain visible; an
// scp-like prefix containing a password/token separator is redacted.
func RedactCloneRef(ref string) string {
	ref = strings.TrimSpace(ref)
	parsed, err := url.Parse(ref)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.User != nil {
		parsed.User = nil
		return parsed.String()
	}
	at := strings.LastIndex(ref, "@")
	if at > 0 && strings.Contains(ref[:at], ":") {
		return "***@" + ref[at+1:]
	}
	return ref
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
		if ref = strings.TrimSpace(ref); ref != "" {
			message = strings.ReplaceAll(message, ref, RedactCloneRef(ref))
		}
	}
	return errors.New(message)
}

package gitx

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// NetworkCloneURL accepts portable Git network sources without embedded secrets.
// In particular, a local checkout must never become a committed submodule URL.
func NetworkCloneURL(raw string) error {
	bad := errors.New("source must be an HTTP(S), SSH or Git network URL without credentials, query, fragment or control characters")
	if raw == "" || strings.TrimSpace(raw) != raw || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "\\?#") || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return bad
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.Path == "" || u.RawQuery != "" || u.Fragment != "" {
			return bad
		}
		switch u.Scheme {
		case "http", "https", "git":
			if u.User != nil {
				return bad
			}
		case "ssh":
			if u.User != nil {
				if _, password := u.User.Password(); password {
					return bad
				}
			}
		default:
			return bad
		}
		return nil
	}
	// SCP spelling: [user@]host:path. A drive letter is not a host.
	host, path, ok := strings.Cut(raw, ":")
	if !ok || len(host) < 2 || path == "" || strings.HasPrefix(path, ":") || strings.ContainsAny(host, "/ :") || strings.ContainsAny(path, "\r\n") || strings.Count(host, "@") > 1 {
		return bad
	}
	if strings.HasPrefix(host, ".") || strings.HasPrefix(path, "-") {
		return bad
	}
	return nil
}

type CloneSource struct {
	Remote string `json:"remote"`
	URL    string `json:"url"`
}

// FetchCloneSources reads Git's effective fetch URLs locally. Unsafe URL text
// is never returned, even in an error. Multiple origins require an explicit choice.
func FetchCloneSources(ctx context.Context, root string) ([]CloneSource, error) {
	r, err := Discover(ctx, root)
	if err != nil || r.Bare || !sameCanonicalPath(r.Root, root) {
		return nil, errors.New("clone URL lookup requires an exact repository checkout")
	}
	out, err := Run(ctx, root, "remote")
	if err != nil {
		return nil, err
	}
	var sources []CloneSource
	for _, remote := range lines(out) {
		urls, err := Run(ctx, root, "remote", "get-url", "--all", remote)
		if err != nil {
			return nil, errors.New("cannot read repository fetch URLs")
		}
		for _, raw := range lines(urls) {
			if NetworkCloneURL(raw) != nil {
				continue
			}
			sources = append(sources, CloneSource{Remote: remote, URL: raw})
		}
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Remote < sources[j].Remote })
	return sources, nil
}

func PreferredCloneSources(sources []CloneSource) []CloneSource {
	var origins []CloneSource
	for _, s := range sources {
		if s.Remote == "origin" {
			origins = append(origins, s)
		}
	}
	if len(origins) > 0 {
		return origins
	}
	return sources
}

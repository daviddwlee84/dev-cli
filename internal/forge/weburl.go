package forge

import (
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WebURLSource identifies the evidence used to derive a browser URL.
type WebURLSource string

const (
	// WebURLSourceForgeRecord is an exact live RemoteRepo inventory match.
	WebURLSourceForgeRecord WebURLSource = "forge-record"
	// WebURLSourceAzureRemote is a remote accepted by Azure's strict URL mapper.
	WebURLSourceAzureRemote WebURLSource = "azure-remote"
	// WebURLSourceGitRemote is a conservative literal/configured GitHub or
	// GitLab remote transform.
	WebURLSourceGitRemote WebURLSource = "git-remote"
)

// WebURLConfidence describes why a derived link is safe to expose.
type WebURLConfidence string

const (
	WebURLConfidenceExact        WebURLConfidence = "exact"
	WebURLConfidenceStrict       WebURLConfidence = "strict"
	WebURLConfidenceConservative WebURLConfidence = "conservative"
)

// WebURL is a plain, display-safe HTTPS repository URL with derivation
// metadata. URL never contains userinfo, query, or fragment components.
type WebURL struct {
	URL        string           `json:"url"`
	Provider   Kind             `json:"provider"`
	Source     WebURLSource     `json:"source"`
	Confidence WebURLConfidence `json:"confidence"`
}

// WebURLRequest supplies a Git remote plus, when available, the exact live
// forge inventory record matched to it. Additional host lists identify trusted
// enterprise web hosts and authorize matching SSH-host transforms. Environment-
// configured GH_HOST and GITLAB_HOST values are included automatically.
type WebURLRequest struct {
	Remote      string
	Exact       *RemoteRepo
	GitHubHosts []string
	GitLabHosts []string
}

// DeriveWebURL returns a display-safe browser URL when the evidence can be
// mapped without guessing. Evidence order is exact RemoteRepo URL, strict Azure
// mapping, then literal/configured GitHub or GitLab endpoints. Unknown SSH
// aliases, local paths, file URLs, unsupported schemes/ports, controls, and
// ambiguous encoded separators return unavailable.
func DeriveWebURL(request WebURLRequest) (WebURL, bool) {
	if request.Exact != nil {
		if result, ok := webURLFromExact(*request.Exact); ok {
			return result, true
		}
	}
	if _, rawURL, ok := parseAzureDevOpsRemote(request.Remote); ok {
		if _, _, safeURL, ok := parseSafeHTTPS(rawURL); ok {
			return WebURL{
				URL: safeURL, Provider: AzureDevOps,
				Source: WebURLSourceAzureRemote, Confidence: WebURLConfidenceStrict,
			}, true
		}
	}

	endpoint, ok := parseForgeEndpoint(request.Remote)
	if !ok {
		return WebURL{}, false
	}
	hosts := webHostPolicy(request)
	provider := hosts.provider(endpoint.host, endpoint.transport == "ssh")
	if provider == Unknown || !validRepositorySegments(provider, endpoint.segments) {
		return WebURL{}, false
	}
	segments := stripGitSuffix(endpoint.segments)
	if segments == nil {
		return WebURL{}, false
	}
	return WebURL{
		URL: buildHTTPSURL(endpoint.host, segments), Provider: provider,
		Source: WebURLSourceGitRemote, Confidence: WebURLConfidenceConservative,
	}, true
}

func webURLFromExact(repo RemoteRepo) (WebURL, bool) {
	if repo.Forge == AzureDevOps {
		identity, rawURL, ok := parseAzureDevOpsRemote(repo.URL)
		if !ok || repo.FullName == "" || identity != repo.FullName {
			return WebURL{}, false
		}
		_, _, safeURL, ok := parseSafeHTTPS(rawURL)
		if !ok {
			return WebURL{}, false
		}
		return WebURL{
			URL: safeURL, Provider: AzureDevOps,
			Source: WebURLSourceForgeRecord, Confidence: WebURLConfidenceExact,
		}, true
	}
	if repo.Forge != GitHub && repo.Forge != GitLab {
		return WebURL{}, false
	}
	host, segments, _, ok := parseSafeHTTPS(repo.URL)
	if !ok {
		return WebURL{}, false
	}
	segments = stripGitSuffix(segments)
	if !validRepositorySegments(repo.Forge, segments) || repo.FullName == "" || strings.Join(segments, "/") != repo.FullName {
		return WebURL{}, false
	}
	return WebURL{
		URL: buildHTTPSURL(host, segments), Provider: repo.Forge,
		Source: WebURLSourceForgeRecord, Confidence: WebURLConfidenceExact,
	}, true
}

type forgeEndpoint struct {
	transport string
	host      string
	segments  []string
}

func parseForgeEndpoint(raw string) (forgeEndpoint, bool) {
	if !safeRawURLText(raw) {
		return forgeEndpoint{}, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return forgeEndpoint{}, false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.Hostname() == "" || !validWebHost(u.Hostname()) {
			return forgeEndpoint{}, false
		}
		host := strings.ToLower(u.Hostname())
		segments, ok := decodeURLPath(u.EscapedPath())
		if !ok {
			return forgeEndpoint{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if u.Port() != "" && u.Port() != "443" {
				return forgeEndpoint{}, false
			}
			return forgeEndpoint{transport: "https", host: host, segments: segments}, true
		case "ssh":
			if u.Port() != "" && u.Port() != "22" || u.User == nil || u.User.Username() != "git" {
				return forgeEndpoint{}, false
			}
			if _, hasPassword := u.User.Password(); hasPassword {
				return forgeEndpoint{}, false
			}
			return forgeEndpoint{transport: "ssh", host: host, segments: segments}, true
		default:
			return forgeEndpoint{}, false
		}
	}

	at := strings.IndexByte(raw, '@')
	if at <= 0 || raw[:at] != "git" {
		return forgeEndpoint{}, false
	}
	colon := strings.IndexByte(raw[at+1:], ':')
	if colon < 0 {
		return forgeEndpoint{}, false
	}
	colon += at + 1
	host := raw[at+1 : colon]
	if host == "" || strings.ContainsAny(host, `/\\[]:`) || !validWebHost(host) {
		return forgeEndpoint{}, false
	}
	path := stripQueryFragment(raw[colon+1:])
	segments, ok := decodeURLPath(path)
	if !ok {
		return forgeEndpoint{}, false
	}
	return forgeEndpoint{transport: "ssh", host: strings.ToLower(host), segments: segments}, true
}

func parseSafeHTTPS(raw string) (host string, segments []string, safeURL string, ok bool) {
	if !safeRawURLText(raw) {
		return "", nil, "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.Hostname() == "" || !validWebHost(u.Hostname()) {
		return "", nil, "", false
	}
	if u.Port() != "" && u.Port() != "443" {
		return "", nil, "", false
	}
	segments, ok = decodeURLPath(u.EscapedPath())
	if !ok {
		return "", nil, "", false
	}
	host = strings.ToLower(u.Hostname())
	return host, segments, buildHTTPSURL(host, segments), true
}

func decodeURLPath(path string) ([]string, bool) {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil, false
	}
	rawSegments := strings.Split(path, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		if raw == "" || hasEncodedSeparator(raw) {
			return nil, false
		}
		decoded, err := url.PathUnescape(raw)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || !utf8.ValidString(decoded) ||
			strings.ContainsAny(decoded, `/\\`) || hasEncodedSeparator(decoded) || containsControl(decoded) {
			return nil, false
		}
		segments = append(segments, decoded)
	}
	return segments, true
}

func stripGitSuffix(segments []string) []string {
	if len(segments) == 0 {
		return nil
	}
	result := append([]string(nil), segments...)
	result[len(result)-1] = strings.TrimSuffix(result[len(result)-1], ".git")
	if result[len(result)-1] == "" {
		return nil
	}
	return result
}

func validRepositorySegments(provider Kind, segments []string) bool {
	segments = stripGitSuffix(segments)
	switch provider {
	case GitHub:
		return len(segments) == 2
	case GitLab:
		return len(segments) >= 2
	default:
		return false
	}
}

func buildHTTPSURL(host string, segments []string) string {
	return (&url.URL{Scheme: "https", Host: strings.ToLower(host), Path: "/" + strings.Join(segments, "/")}).String()
}

func safeRawURLText(value string) bool {
	return utf8.ValidString(value) && !containsControl(value)
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0
}

func hasEncodedSeparator(value string) bool {
	current := value
	for depth := 0; depth < 8; depth++ {
		lower := strings.ToLower(current)
		if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
			return true
		}
		if !strings.ContainsRune(current, '%') {
			return false
		}
		decoded, err := url.PathUnescape(current)
		if err != nil || decoded == current {
			return false
		}
		current = decoded
	}
	// Deeply nested escaping is unnecessary for repository identities and is
	// ambiguous across clients with different decode counts.
	return true
}

func stripQueryFragment(value string) string {
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		return value[:index]
	}
	return value
}

type hostPolicy struct {
	github map[string]struct{}
	gitlab map[string]struct{}
}

func webHostPolicy(request WebURLRequest) hostPolicy {
	policy := hostPolicy{github: map[string]struct{}{}, gitlab: map[string]struct{}{}}
	for _, host := range append([]string{"github.com", os.Getenv("GH_HOST")}, request.GitHubHosts...) {
		if normalized, ok := normalizeConfiguredHost(host); ok {
			policy.github[normalized] = struct{}{}
		}
	}
	for _, host := range append([]string{"gitlab.com", os.Getenv("GITLAB_HOST")}, request.GitLabHosts...) {
		if normalized, ok := normalizeConfiguredHost(host); ok {
			policy.gitlab[normalized] = struct{}{}
		}
	}
	return policy
}

func (policy hostPolicy) provider(host string, ssh bool) Kind {
	host = strings.ToLower(host)
	if configured, decided := policy.configuredProvider(host); decided {
		return configured
	}
	if ssh {
		// An unconfigured SSH hostname may be an alias whose real web host is
		// unrelated. Only explicit web-host configuration or public defaults can
		// authorize an SSH transform.
		return Unknown
	}
	return literalForgeHost(host)
}

func (policy hostPolicy) identityProvider(host string) Kind {
	host = strings.ToLower(host)
	if configured, decided := policy.configuredProvider(host); decided {
		return configured
	}
	// Legacy identity classification may recognize provider-named enterprise
	// hosts. Unlike DeriveWebURL, this does not manufacture a browser hostname.
	return literalForgeHost(host)
}

func (policy hostPolicy) configuredProvider(host string) (Kind, bool) {
	_, github := policy.github[host]
	_, gitlab := policy.gitlab[host]
	switch {
	case github && gitlab:
		return Unknown, true
	case github:
		return GitHub, true
	case gitlab:
		return GitLab, true
	default:
		return Unknown, false
	}
}

func literalForgeHost(host string) Kind {
	switch {
	case host == "github.com", strings.HasPrefix(host, "github."):
		return GitHub
	case host == "gitlab.com", strings.HasPrefix(host, "gitlab."):
		return GitLab
	default:
		return Unknown
	}
}

func normalizeConfiguredHost(raw string) (string, bool) {
	if !safeRawURLText(raw) {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			u.Path != "" && u.Path != "/" || u.Hostname() == "" || u.Port() != "" && u.Port() != "443" {
			return "", false
		}
		raw = u.Hostname()
	} else if strings.ContainsAny(raw, `/\\@?#`) {
		return "", false
	} else {
		u, err := url.Parse("//" + raw)
		if err != nil || u.Hostname() == "" || u.Port() != "" && u.Port() != "443" {
			return "", false
		}
		raw = u.Hostname()
	}
	if !validWebHost(raw) || !strings.Contains(raw, ".") {
		return "", false
	}
	return strings.ToLower(raw), true
}

func validWebHost(host string) bool {
	if host == "" || strings.HasSuffix(host, ".") || !utf8.ValidString(host) {
		return false
	}
	for _, r := range host {
		if r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

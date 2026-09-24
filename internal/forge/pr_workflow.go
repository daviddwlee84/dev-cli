package forge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PRReference identifies a request independently of the current checkout.
type PRReference struct {
	Forge  Kind   `json:"forge"`
	Host   string `json:"host"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (r PRReference) URL() string {
	path := "/pull/"
	if r.Forge == GitLab {
		path = "/-/merge_requests/"
	}
	return "https://" + r.Host + "/" + r.Repo + path + strconv.Itoa(r.Number)
}

func (r PRReference) Validate() error {
	if r.Forge != GitHub && r.Forge != GitLab {
		return fmt.Errorf("unsupported pull request provider %q", r.Forge)
	}
	if r.Host != ConfiguredHost(r.Forge) {
		return fmt.Errorf("request host does not match configured %s host", r.Forge)
	}
	parts := strings.Split(r.Repo, "/")
	if len(parts) < 2 || (r.Forge == GitHub && len(parts) != 2) {
		return fmt.Errorf("invalid request repository")
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("invalid request repository")
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return fmt.Errorf("invalid request repository")
			}
		}
	}
	if r.Number < 0 {
		return fmt.Errorf("invalid request number")
	}
	return nil
}

// ParsePRReference accepts only a complete provider URL, never a repository URL
// interpreted as a request or a host selected by an untrusted response.
func ParsePRReference(raw string) (PRReference, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || u.RawPath != "" {
		return PRReference{}, fmt.Errorf("want a canonical HTTPS pull/merge request URL")
	}
	host := strings.ToLower(u.Host)
	kind := Unknown
	for _, k := range []Kind{GitHub, GitLab} {
		if host == ConfiguredHost(k) {
			kind = k
			break
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	index := -1
	if kind == GitHub && len(parts) == 4 && parts[2] == "pull" {
		index = 2
	}
	if kind == GitLab && len(parts) >= 5 && parts[len(parts)-3] == "-" && parts[len(parts)-2] == "merge_requests" {
		index = len(parts) - 3
	}
	if index < 0 {
		return PRReference{}, fmt.Errorf("want a GitHub pull request or GitLab merge request URL on the configured host")
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n <= 0 {
		return PRReference{}, fmt.Errorf("invalid pull request number")
	}
	r := PRReference{Forge: kind, Host: host, Repo: strings.Join(parts[:index], "/"), Number: n}
	return r, r.Validate()
}

type PRSize struct {
	Files     *int `json:"files,omitempty"`
	Additions *int `json:"additions,omitempty"`
	Deletions *int `json:"deletions,omitempty"`
}
type PRCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}
type PRDetailResult struct {
	PullRequest
	Reference        PRReference `json:"reference"`
	AccountID        string      `json:"account_id"`
	RepositoryID     string      `json:"repository_id"`
	HeadOID          string      `json:"head_oid"`
	BaseOID          string      `json:"base_oid"`
	DiffStartOID     string      `json:"diff_start_oid,omitempty"`
	HeadURL          string      `json:"head_url"`
	BaseURL          string      `json:"base_url"`
	ObservedAt       time.Time   `json:"observed_at"`
	Readiness        string      `json:"readiness"`
	Reason           string      `json:"reason,omitempty"`
	Size             PRSize      `json:"size"`
	CheckDetails     []PRCheck   `json:"check_details,omitempty"`
	Queue            bool        `json:"queue"`
	CanMerge         bool        `json:"can_merge"`
	SquashAllowed    bool        `json:"squash_allowed"`
	MergeOID         string      `json:"merge_oid,omitempty"`
	AutoDeleteBranch bool        `json:"auto_delete_branch"`
}

type PRPageQuery struct {
	Reference    PRReference `json:"reference"`
	Relationship string      `json:"relationship"`
	State        PRState     `json:"state"`
	Cursor       string      `json:"cursor,omitempty"`
	Limit        int         `json:"limit"`
	accountID    string
}
type PRPage struct {
	PullRequests    []PullRequest `json:"pull_requests"`
	NextCursor      string        `json:"next_cursor,omitempty"`
	Total           *int          `json:"total,omitempty"`
	TotalLowerBound bool          `json:"total_lower_bound,omitempty"`
	Scope           string        `json:"scope"`
	Complete        bool          `json:"complete"`
}
type PRDiff struct {
	Text         string `json:"text"`
	TerminalText string `json:"terminal_text"`
	HeadOID      string `json:"head_oid"`
	BaseOID      string `json:"base_oid"`
	Live         bool   `json:"live"`
	Complete     bool   `json:"complete"`
	Warning      string `json:"warning,omitempty"`
}
type PRMergeOutcome struct {
	Status   string `json:"status"`
	MergeOID string `json:"merge_oid,omitempty"`
}

// PRProvider is deliberately optional; legacy inventory and portable review
// evidence retain their independent contracts.
type PRProvider interface {
	ListPage(context.Context, PRPageQuery) (PRPage, error)
	Detail(context.Context, PRReference) (PRDetailResult, error)
	Diff(context.Context, PRDetailResult) (PRDiff, error)
	Merge(context.Context, PRDetailResult) (PRMergeOutcome, error)
}

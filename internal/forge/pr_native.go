package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const prOutputLimit = 32 << 20

type nativePRProvider struct {
	kind Kind
	host string
}

func NewPRProvider(kind Kind) PRProvider {
	return &nativePRProvider{kind: kind, host: ConfiguredHost(kind)}
}

type prRunResult struct {
	Body    []byte
	Status  int
	Headers map[string]string
}
type prCommandRunner func(context.Context, string, []string, []byte) (prRunResult, error)

var prCommand prCommandRunner = runPRCommand

type boundedPRBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedPRBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining < len(p) {
		b.exceeded = true
		p = p[:max(remaining, 0)]
	}
	_, _ = b.data.Write(p)
	return n, nil
}
func (b *boundedPRBuffer) Len() int       { return b.data.Len() }
func (b *boundedPRBuffer) Bytes() []byte  { return b.data.Bytes() }
func (b *boundedPRBuffer) String() string { return b.data.String() }

func runPRCommand(ctx context.Context, bin string, args []string, input []byte) (prRunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GH_PAGER=cat", "GLAB_PAGER=cat", "NO_COLOR=1")
	out := &boundedPRBuffer{limit: prOutputLimit}
	errout := &boundedPRBuffer{limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = errout
	err := cmd.Run()
	result := prRunResult{Body: append([]byte(nil), out.Bytes()...)}
	// Keep response headers for pagination and distinguish a structured HTTP
	// refusal from a transport whose write effects are unknown.
	if bytes.HasPrefix(result.Body, []byte("HTTP/")) {
		body := strings.ReplaceAll(string(result.Body), "\r\n", "\n")
		if i := strings.Index(body, "\n\n"); i >= 0 {
			result.Headers = map[string]string{}
			for _, line := range strings.Split(body[:i], "\n")[1:] {
				if key, value, ok := strings.Cut(line, ":"); ok {
					result.Headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
				}
			}
			fields := strings.Fields(body[:strings.Index(body, "\n")])
			if len(fields) > 1 {
				result.Status, _ = strconv.Atoi(fields[1])
			}
			result.Body = []byte(body[i+2:])
		}
	}
	if out.exceeded {
		return result, errors.New("provider response exceeds the 32 MiB preview limit")
	}
	if err != nil {
		return result, fmt.Errorf("%s request failed: %w", bin, err)
	}
	return result, nil
}

func (p *nativePRProvider) api(ctx context.Context, method, path string, body any) (prRunResult, error) {
	if p.kind != GitHub && p.kind != GitLab {
		return prRunResult{}, &ErrUnsupported{Kind: p.kind, Operation: "pull request actions"}
	}
	bin := "gh"
	if p.kind == GitLab {
		bin = "glab"
	}
	args := []string{"api", "--hostname", p.host, "--method", method, "--include", path}
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		if err != nil {
			return prRunResult{}, err
		}
		args = append(args, "--input", "-")
	}
	return prCommand(ctx, bin, args, input)
}
func (p *nativePRProvider) check(r PRReference, number bool) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Forge != p.kind || r.Host != p.host {
		return errors.New("request does not match selected provider")
	}
	if number && r.Number <= 0 {
		return errors.New("request number is required")
	}
	return nil
}
func (p *nativePRProvider) account(ctx context.Context) (string, string, error) {
	out, err := p.api(ctx, "GET", "user", nil)
	if err != nil {
		return "", "", err
	}
	var u struct {
		ID       json.Number `json:"id"`
		Login    string      `json:"login"`
		Username string      `json:"username"`
	}
	if err = json.Unmarshal(out.Body, &u); err != nil {
		return "", "", err
	}
	name := u.Login
	if p.kind == GitLab {
		name = u.Username
	}
	if u.ID == "" || name == "" {
		return "", "", errors.New("provider account identity missing")
	}
	return string(u.ID), name, nil
}
func prRepoPath(r PRReference) string {
	if r.Forge == GitLab {
		return "projects/" + url.PathEscape(r.Repo)
	}
	return "repos/" + r.Repo
}
func prItemPath(r PRReference) string {
	path := prRepoPath(r)
	if r.Forge == GitLab {
		return path + "/merge_requests/" + strconv.Itoa(r.Number)
	}
	return path + "/pulls/" + strconv.Itoa(r.Number)
}

type githubNativePR struct {
	ID         json.Number `json:"id"`
	Number     int         `json:"number"`
	Title      string      `json:"title"`
	URL        string      `json:"html_url"`
	State      string      `json:"state"`
	Draft      bool        `json:"draft"`
	Merged     bool        `json:"merged"`
	MergedAt   *time.Time  `json:"merged_at"`
	MergeOID   string      `json:"merge_commit_sha"`
	Mergeable  *bool       `json:"mergeable"`
	MergeState string      `json:"mergeable_state"`
	User       struct {
		Login string `json:"login"`
	} `json:"user"`
	Head               nativePRRef `json:"head"`
	Base               nativePRRef `json:"base"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
	Additions *int      `json:"additions"`
	Deletions *int      `json:"deletions"`
	Files     *int      `json:"changed_files"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type nativePRRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		ID       json.Number `json:"id"`
		FullName string      `json:"full_name"`
		CloneURL string      `json:"clone_url"`
	} `json:"repo"`
}

func (r githubNativePR) row(ref PRReference) PullRequest {
	state := PRState(r.State)
	if r.Merged || r.MergedAt != nil {
		state = PRStateMerged
	}
	mergeable := "unknown"
	if r.Mergeable != nil {
		mergeable = "conflicting"
		if *r.Mergeable {
			mergeable = "mergeable"
		}
	}
	return PullRequest{Forge: GitHub, Host: ref.Host, Repo: ref.Repo, Number: r.Number, Title: r.Title, URL: r.URL, State: state, Draft: r.Draft, Author: r.User.Login, Detail: PRDetailFull, HeadRepo: r.Head.Repo.FullName, CrossRepository: !strings.EqualFold(r.Head.Repo.FullName, r.Base.Repo.FullName), HeadBranch: r.Head.Ref, BaseBranch: r.Base.Ref, Mergeable: mergeable, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (p *nativePRProvider) ListPage(ctx context.Context, q PRPageQuery) (PRPage, error) {
	page := PRPage{PullRequests: []PullRequest{}, Scope: q.Relationship}
	if err := p.check(q.Reference, false); err != nil {
		return page, err
	}
	if q.Relationship == "" {
		q.Relationship = "all"
		page.Scope = "all"
	}
	if q.Relationship != "all" && q.Relationship != "related" && q.Relationship != "author" && q.Relationship != "reviewer" {
		return page, errors.New("relationship must be all, related, author or reviewer")
	}
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return page, errors.New("page limit must be 1 through 100")
	}
	state := q.State
	if state == "" {
		state = PRStateOpen
	}
	if state != PRStateOpen && state != PRStateAll && state != PRStateMerged && state != PRStateClosed {
		return page, errors.New("invalid pull request state")
	}
	q.State = state
	q.Limit = limit
	account, username, err := p.account(ctx)
	if err != nil {
		return page, err
	}
	q.accountID = account
	n, err := decodePRCursor(q)
	if err != nil {
		return page, err
	}
	if q.Relationship != "all" {
		return p.listRelated(ctx, q, n, username)
	}
	query := url.Values{"per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(n)}, "sort": {"updated"}, "direction": {"desc"}}
	path := prRepoPath(q.Reference)
	if p.kind == GitHub {
		apiState := string(state)
		if state == PRStateMerged {
			apiState = "closed"
		}
		query.Set("state", apiState)
		path += "/pulls?" + query.Encode()
		out, err := p.api(ctx, "GET", path, nil)
		if err != nil {
			return page, err
		}
		var raw []githubNativePR
		if err = json.Unmarshal(out.Body, &raw); err != nil {
			return page, err
		}
		if prHasNext(out, len(raw), limit) {
			page.NextCursor = encodePRCursor(q, n+1)
		}
		for _, item := range raw {
			row := item.row(q.Reference)
			if state != PRStateAll && row.State != state {
				continue
			}
			author := strings.EqualFold(username, row.Author)
			reviewer := false
			for _, v := range item.RequestedReviewers {
				reviewer = reviewer || strings.EqualFold(username, v.Login)
			}
			row.Roles = prNativeRoles(author, reviewer)
			if !prNativeRelationship(q.Relationship, author, reviewer) {
				continue
			}
			page.PullRequests = append(page.PullRequests, row)
		}
		page.Total = p.githubCount(ctx, q.Reference, state)
		if page.Total == nil && (state == PRStateOpen || state == PRStateAll) {
			total := (n-1)*limit + len(raw)
			if page.NextCursor != "" {
				total++
				page.TotalLowerBound = true
			}
			page.Total = &total
		}
	} else {
		query.Del("sort")
		query.Del("direction")
		query.Set("order_by", "updated_at")
		query.Set("sort", "desc")
		query.Set("state", gitLabMRState(state))
		path += "/merge_requests?" + query.Encode()
		out, err := p.api(ctx, "GET", path, nil)
		if err != nil {
			return page, err
		}
		var raw []gitlabNativePR
		if err = json.Unmarshal(out.Body, &raw); err != nil {
			return page, err
		}
		if prHasNext(out, len(raw), limit) {
			page.NextCursor = encodePRCursor(q, n+1)
		}
		for _, item := range raw {
			row := item.row(q.Reference)
			author := strings.EqualFold(username, row.Author)
			reviewer := false
			for _, v := range item.Reviewers {
				reviewer = reviewer || strings.EqualFold(username, v.Username)
			}
			row.Roles = prNativeRoles(author, reviewer)
			if !prNativeRelationship(q.Relationship, author, reviewer) {
				continue
			}
			page.PullRequests = append(page.PullRequests, row)
		}
		if total, e := strconv.Atoi(out.Headers["x-total"]); e == nil && total >= 0 {
			page.Total = &total
		}
		if page.Total == nil {
			total := (n-1)*limit + len(raw)
			if page.NextCursor != "" {
				total++
				page.TotalLowerBound = true
			}
			page.Total = &total
		}
	}
	page.Complete = page.NextCursor == ""
	return page, nil
}

func prHasNext(out prRunResult, count, limit int) bool {
	if out.Headers != nil {
		if next, ok := out.Headers["x-next-page"]; ok {
			return next != ""
		}
		return strings.Contains(out.Headers["link"], `rel="next"`)
	}
	return count == limit
}

func (p *nativePRProvider) Detail(ctx context.Context, r PRReference) (PRDetailResult, error) {
	if err := p.check(r, true); err != nil {
		return PRDetailResult{}, err
	}
	account, _, err := p.account(ctx)
	if err != nil {
		return PRDetailResult{}, err
	}
	if p.kind == GitHub {
		return p.githubDetail(ctx, r, account)
	}
	return p.gitlabDetail(ctx, r, account)
}

func (p *nativePRProvider) githubDetail(ctx context.Context, r PRReference, account string) (PRDetailResult, error) {
	out, err := p.api(ctx, "GET", prItemPath(r), nil)
	if err != nil {
		return PRDetailResult{}, err
	}
	var raw githubNativePR
	if err = json.Unmarshal(out.Body, &raw); err != nil {
		return PRDetailResult{}, err
	}
	if raw.Number != r.Number || !strings.EqualFold(raw.Base.Repo.FullName, r.Repo) || raw.Base.Repo.ID == "" || !validPROID(raw.Head.SHA) || !validPROID(raw.Base.SHA) {
		return PRDetailResult{}, errors.New("provider returned a mismatched or incomplete request identity")
	}
	d := PRDetailResult{PullRequest: raw.row(r), Reference: r, AccountID: account, RepositoryID: string(raw.Base.Repo.ID), HeadOID: raw.Head.SHA, BaseOID: raw.Base.SHA, BaseURL: "https://" + r.Host + "/" + r.Repo + ".git", ObservedAt: time.Now().UTC(), Size: PRSize{Files: raw.Files, Additions: raw.Additions, Deletions: raw.Deletions}, MergeOID: raw.MergeOID, Readiness: "unknown"}
	headRef := PRReference{Forge: r.Forge, Host: r.Host, Repo: raw.Head.Repo.FullName}
	if headRef.Validate() == nil {
		d.HeadURL = "https://" + r.Host + "/" + raw.Head.Repo.FullName + ".git"
	}
	// One GraphQL observation adds policy/review/queue and exact-head checks.
	owner, name, _ := strings.Cut(r.Repo, "/")
	query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){squashMergeAllowed viewerPermission deleteBranchOnMerge pullRequest(number:$number){headRefOid baseRefOid mergeStateStatus reviewDecision mergeQueue{__typename} mergeQueueEntry{__typename} autoMergeRequest{enabledAt} commits(last:1){nodes{commit{oid statusCheckRollup{contexts(first:100){pageInfo{hasNextPage} nodes{... on CheckRun{name status conclusion detailsUrl} ... on StatusContext{context state targetUrl}}}}}}}}}}`
	// GraphQL is a read even though it uses POST. Never use the write runner's
	// --include pathway or infer mutation from its HTTP method.
	input, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"owner": owner, "name": name, "number": r.Number}})
	result, e := prCommand(ctx, "gh", []string{"api", "--hostname", p.host, "graphql", "--input", "-"}, input)
	if e != nil {
		d.Reason = "merge policy unavailable"
		return d, nil
	}
	var graph struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository struct {
				Squash     *bool  `json:"squashMergeAllowed"`
				Permission string `json:"viewerPermission"`
				Delete     bool   `json:"deleteBranchOnMerge"`
				PR         struct {
					Head    string          `json:"headRefOid"`
					Base    string          `json:"baseRefOid"`
					Status  string          `json:"mergeStateStatus"`
					Review  string          `json:"reviewDecision"`
					Queue   json.RawMessage `json:"mergeQueue"`
					Entry   json.RawMessage `json:"mergeQueueEntry"`
					Auto    json.RawMessage `json:"autoMergeRequest"`
					Commits struct {
						Nodes []struct {
							Commit struct {
								OID    string          `json:"oid"`
								Rollup json.RawMessage `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(result.Body, &graph) != nil || len(graph.Errors) > 0 {
		d.Reason = "merge policy unavailable"
		return d, nil
	}
	repo := graph.Data.Repository
	pr := repo.PR
	if pr.Head != d.HeadOID || pr.Base != d.BaseOID {
		return PRDetailResult{}, errors.New("pull request changed while reading details; refresh")
	}
	d.CanMerge = repo.Permission == "WRITE" || repo.Permission == "MAINTAIN" || repo.Permission == "ADMIN"
	d.SquashAllowed = repo.Squash != nil && *repo.Squash
	d.Queue = nonNullJSON(pr.Queue) || nonNullJSON(pr.Entry) || nonNullJSON(pr.Auto)
	d.AutoDeleteBranch = repo.Delete
	d.ReviewDecision = strings.ToLower(pr.Review)
	var outcomes []checkOutcome
	policyComplete := repo.Squash != nil && len(pr.Queue) > 0 && len(pr.Entry) > 0 && len(pr.Auto) > 0 && raw.Mergeable != nil && pr.Status != "" && pr.Status != "UNKNOWN"
	checksComplete := len(pr.Commits.Nodes) == 1 && pr.Commits.Nodes[0].Commit.OID == d.HeadOID
	var rollup *githubPRRollup
	if len(pr.Commits.Nodes) == 1 {
		reported := pr.Commits.Nodes[0].Commit.Rollup
		checksComplete = checksComplete && len(reported) > 0
		if nonNullJSON(reported) && json.Unmarshal(reported, &rollup) != nil {
			checksComplete = false
			rollup = nil
		}
	}
	if rollup != nil {
		checksComplete = checksComplete && rollup.Contexts.Page.Next != nil && !*rollup.Contexts.Page.Next && rollup.Contexts.Nodes != nil
		for _, c := range rollup.Contexts.Nodes {
			outcome := checkOutcome{Status: c.Status, Conclusion: c.Conclusion, State: c.State}
			if classifyCheck(outcome) == "" {
				checksComplete = false
			}
			outcomes = append(outcomes, outcome)
			name := c.Name
			if name == "" {
				name = c.Context
			}
			link := c.URL
			if link == "" {
				link = c.Target
			}
			d.CheckDetails = append(d.CheckDetails, PRCheck{Name: name, State: classifyCheck(outcome), URL: link})
		}
	}
	d.Checks = foldChecks(outcomes)
	if !checksComplete && (d.Checks == ChecksPassing || d.Checks == ChecksNone) {
		d.Checks = ""
	}
	setPRReadiness(&d, pr.Status == "CLEAN" && d.Mergeable == "mergeable", policyComplete && checksComplete)
	return d, nil
}

func nonNullJSON(b []byte) bool { return len(b) > 0 && string(b) != "null" }

type githubPRRollup struct {
	Contexts struct {
		Page struct {
			Next *bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Name       string `json:"name"`
			Context    string `json:"context"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
			URL        string `json:"detailsUrl"`
			Target     string `json:"targetUrl"`
		} `json:"nodes"`
	} `json:"contexts"`
}

func validPROID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func setPRReadiness(d *PRDetailResult, ready, complete bool) {
	d.Readiness = "blocked"
	switch {
	case d.State != PRStateOpen:
		d.Reason = "request is not open"
	case d.Draft:
		d.Reason = "request is a draft"
	case d.Queue:
		d.Reason = "queue, merge train or auto-merge requires the provider UI"
	case !complete:
		d.Readiness = "unknown"
		d.Reason = "checks or merge policy are incomplete"
	case !d.CanMerge:
		d.Reason = "merge permission unavailable"
	case !d.SquashAllowed:
		d.Reason = "squash merge is not enabled"
	case d.Checks == ChecksFailing:
		d.Reason = "checks are failing"
	case d.Checks == ChecksPending:
		d.Reason = "checks are pending"
	case d.ReviewDecision == "changes_requested" || d.ReviewDecision == "review_required":
		d.Reason = "review requirements are not met"
	case ready:
		d.Readiness = "ready"
		d.Reason = ""
	default:
		d.Reason = "provider merge requirements are not met"
	}
}

func (p *nativePRProvider) Diff(ctx context.Context, d PRDetailResult) (PRDiff, error) {
	result := PRDiff{HeadOID: d.HeadOID, BaseOID: d.BaseOID, Live: true}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := p.check(d.Reference, true); err != nil {
		return result, err
	}
	bin := "gh"
	args := []string{"api", "--hostname", p.host, prItemPath(d.Reference), "-H", "Accept: application/vnd.github.diff"}
	if p.kind == GitLab {
		result.Complete, result.Warning = p.gitlabDiffCompleteness(ctx, d)
		bin = "glab"
		args = []string{"api", "--hostname", p.host, prItemPath(d.Reference) + "/raw_diffs"}
	}
	out, err := prCommand(ctx, bin, args, nil)
	if err != nil {
		return result, err
	}
	result.Text = string(out.Body)
	result.TerminalText = safePRTerminal(result.Text)
	files := 0
	for _, line := range strings.Split(result.Text, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			files++
		}
	}
	if p.kind == GitHub {
		result.Complete = d.Size.Files != nil
		if !result.Complete {
			result.Warning = "provider diff completeness is unknown"
		}
	}
	if d.Size.Files == nil || files != *d.Size.Files {
		result.Complete = false
		result.Warning = "live diff file count is incomplete or changed; refresh or inspect a local checkout"
	}
	if strings.Contains(result.Text, "\nBinary files ") {
		result.Complete = false
		result.Warning = "binary file contents are omitted from the provider diff"
	}
	return result, nil
}

func safePRTerminal(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) {
			if r < 256 {
				fmt.Fprintf(&b, "\\x%02x", r)
			} else {
				fmt.Fprintf(&b, "\\u%04x", r)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func prNativeRoles(author, reviewer bool) []PRRole {
	var roles []PRRole
	if author {
		roles = append(roles, RoleAuthor)
	}
	if reviewer {
		roles = append(roles, RoleReviewer)
	}
	return roles
}
func prNativeRelationship(scope string, author, reviewer bool) bool {
	switch scope {
	case "all":
		return true
	case "author":
		return author
	case "reviewer":
		return reviewer
	default:
		return author || reviewer
	}
}

type prPageCursor struct {
	Page         int         `json:"page"`
	Reference    PRReference `json:"reference"`
	State        PRState     `json:"state"`
	Scope        string      `json:"scope"`
	Limit        int         `json:"limit"`
	AuthorDone   bool        `json:"author_done,omitempty"`
	ReviewerDone bool        `json:"reviewer_done,omitempty"`
	AccountID    string      `json:"account_id"`
	AuthorPage   int         `json:"author_page,omitempty"`
	ReviewerPage int         `json:"reviewer_page,omitempty"`
	NextReviewer bool        `json:"next_reviewer,omitempty"`
}

func encodePRCursor(q PRPageQuery, n int) string {
	v := prPageCursor{Page: n, Reference: q.Reference, State: q.State, Scope: q.Relationship, Limit: q.Limit, AccountID: q.accountID}
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodePRCursor(q PRPageQuery) (int, error) {
	if q.Cursor == "" {
		return 1, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil || len(b) > 4096 {
		return 0, errors.New("invalid page cursor")
	}
	var v prPageCursor
	if json.Unmarshal(b, &v) != nil || v.Page < 1 || v.Page > 10000 || v.AuthorPage < 0 || v.AuthorPage > 10000 || v.ReviewerPage < 0 || v.ReviewerPage > 10000 || v.Reference != q.Reference || v.State != q.State || v.Scope != q.Relationship || v.Limit != q.Limit || v.AccountID != q.accountID {
		return 0, errors.New("page cursor does not match this query")
	}
	return v.Page, nil
}

func (p *nativePRProvider) Merge(ctx context.Context, d PRDetailResult) (PRMergeOutcome, error) {
	if err := p.check(d.Reference, true); err != nil {
		return PRMergeOutcome{}, err
	}
	if !validPROID(d.HeadOID) || d.Readiness != "ready" {
		return PRMergeOutcome{}, errors.New("request has no ready exact-head merge authority")
	}
	body := map[string]any{"sha": d.HeadOID, "merge_method": "squash"}
	if p.kind == GitLab {
		body = map[string]any{"sha": d.HeadOID, "squash": true, "auto_merge": false, "should_remove_source_branch": false}
	}
	out, err := p.api(ctx, "PUT", prItemPath(d.Reference)+"/merge", body)
	result := PRMergeOutcome{Status: "unknown"}
	if out.Status >= 400 && out.Status < 500 {
		result.Status = "rejected"
	}
	if err != nil {
		return result, err
	}
	if out.Status != 200 {
		return result, errors.New("provider did not confirm an immediate merge response; do not retry")
	}
	if p.kind == GitHub {
		var v struct {
			Merged *bool  `json:"merged"`
			SHA    string `json:"sha"`
		}
		if json.Unmarshal(out.Body, &v) == nil && v.Merged != nil && *v.Merged && validPROID(v.SHA) {
			return PRMergeOutcome{Status: "merged", MergeOID: v.SHA}, nil
		}
	} else {
		var v gitlabNativePR
		if err := json.Unmarshal(out.Body, &v); err != nil {
			return result, errors.New("provider merge outcome is unconfirmed; do not retry")
		}
		if v.MergeOID == "" {
			v.MergeOID = v.SquashOID
		}
		if v.State == "merged" && v.IID == d.Reference.Number && validPROID(v.MergeOID) {
			return PRMergeOutcome{Status: "merged", MergeOID: v.MergeOID}, nil
		}
	}
	return result, errors.New("provider merge outcome is unconfirmed; do not retry")
}

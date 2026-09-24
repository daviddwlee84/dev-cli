package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Each role has an independent provider query. In particular, a related view
// never scans the first all-request page hoping to find the viewer there.
func (p *nativePRProvider) listRelated(ctx context.Context, q PRPageQuery, n int, username string) (PRPage, error) {
	page := PRPage{PullRequests: []PullRequest{}, Scope: q.Relationship}
	roles := []PRRole{RoleAuthor, RoleReviewer}
	limit := q.Limit
	if q.Relationship == "author" {
		roles = roles[:1]
	} else if q.Relationship == "reviewer" {
		roles = roles[1:]
	} else if limit > 1 {
		limit /= 2
	}
	cursor := prPageCursor{Page: n + 1, Reference: q.Reference, State: q.State, Scope: q.Relationship, Limit: q.Limit, AccountID: q.accountID, AuthorPage: 1, ReviewerPage: 1}
	if q.Cursor != "" {
		b, _ := base64.RawURLEncoding.DecodeString(q.Cursor)
		var old prPageCursor
		_ = json.Unmarshal(b, &old)
		cursor.AuthorDone = old.AuthorDone
		cursor.ReviewerDone = old.ReviewerDone
		cursor.AuthorPage = max(1, old.AuthorPage)
		cursor.ReviewerPage = max(1, old.ReviewerPage)
		cursor.NextReviewer = old.NextReviewer
	}
	if q.Relationship == "related" && q.Limit == 1 {
		if (cursor.NextReviewer || cursor.AuthorDone) && !cursor.ReviewerDone {
			roles = roles[1:]
		} else {
			roles = roles[:1]
		}
		cursor.NextReviewer = !cursor.NextReviewer
	}
	byKey := map[string]PullRequest{}
	var errs []error
	for _, role := range roles {
		if role == RoleAuthor && cursor.AuthorDone || role == RoleReviewer && cursor.ReviewerDone {
			continue
		}
		rolePage := cursor.AuthorPage
		if role == RoleReviewer {
			rolePage = cursor.ReviewerPage
		}
		rows, total, more, err := p.relatedRole(ctx, q.Reference, q.State, role, username, rolePage, limit)
		if err != nil {
			errs = append(errs, err)
		}
		if role == RoleAuthor {
			cursor.AuthorDone = err == nil && !more
			if err == nil {
				cursor.AuthorPage++
			}
		} else {
			cursor.ReviewerDone = err == nil && !more
			if err == nil {
				cursor.ReviewerPage++
			}
		}
		if q.Relationship != "related" {
			page.Total = total
		}
		for _, row := range rows {
			if old, ok := byKey[row.Key()]; ok {
				row.Roles = unionRoles(old.Roles, row.Roles)
			}
			byKey[row.Key()] = row
		}
	}
	for _, row := range byKey {
		page.PullRequests = append(page.PullRequests, row)
	}
	sort.Slice(page.PullRequests, func(i, j int) bool {
		a, b := page.PullRequests[i], page.PullRequests[j]
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.Key() < b.Key()
	})
	more := !cursor.AuthorDone || !cursor.ReviewerDone
	if q.Relationship == "author" {
		more = !cursor.AuthorDone
	}
	if q.Relationship == "reviewer" {
		more = !cursor.ReviewerDone
	}
	if more && len(errs) == 0 {
		b, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	page.Complete = !more && len(errs) == 0
	return page, errors.Join(errs...)
}

func (p *nativePRProvider) relatedRole(ctx context.Context, r PRReference, state PRState, role PRRole, username string, n, limit int) ([]PullRequest, *int, bool, error) {
	query := url.Values{"per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(n)}}
	if p.kind == GitHub {
		qualifier := "author:"
		if role == RoleReviewer {
			qualifier = "review-requested:"
		}
		search := "repo:" + r.Repo + " is:pr " + qualifier + username
		switch state {
		case PRStateOpen:
			search += " is:open"
		case PRStateMerged:
			search += " is:merged"
		case PRStateClosed:
			search += " is:closed is:unmerged"
		}
		query.Set("q", search)
		query.Set("sort", "updated")
		query.Set("order", "desc")
		out, err := p.api(ctx, "GET", "search/issues?"+query.Encode(), nil)
		if err != nil {
			return nil, nil, false, err
		}
		var raw struct {
			Total      int  `json:"total_count"`
			Incomplete bool `json:"incomplete_results"`
			Items      []struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
				URL    string `json:"html_url"`
				State  string `json:"state"`
				Draft  bool   `json:"draft"`
				User   struct {
					Login string `json:"login"`
				} `json:"user"`
				PR *struct {
					MergedAt *time.Time `json:"merged_at"`
				} `json:"pull_request"`
				CreatedAt time.Time `json:"created_at"`
				UpdatedAt time.Time `json:"updated_at"`
			} `json:"items"`
		}
		if err = json.Unmarshal(out.Body, &raw); err != nil {
			return nil, nil, false, err
		}
		if raw.Incomplete {
			return nil, nil, false, errors.New("provider search is incomplete; narrow the query")
		}
		rows := make([]PullRequest, 0, len(raw.Items))
		for _, item := range raw.Items {
			ref, e := ParsePRReference(item.URL)
			if e != nil || ref.Repo != r.Repo || ref.Host != r.Host || item.Number != ref.Number || item.PR == nil {
				return nil, nil, false, errors.New("provider search returned a mismatched request")
			}
			s := PRState(item.State)
			if item.PR.MergedAt != nil {
				s = PRStateMerged
			}
			rows = append(rows, PullRequest{Forge: r.Forge, Host: r.Host, Repo: r.Repo, Number: item.Number, Title: item.Title, URL: item.URL, State: s, Draft: item.Draft, Author: item.User.Login, Roles: []PRRole{role}, Detail: PRDetailSummary, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
		}
		if n*limit >= 1000 && n*limit < raw.Total {
			return rows, &raw.Total, false, errors.New("provider search limit reached; narrow the query")
		}
		return rows, &raw.Total, n*limit < raw.Total, nil
	}
	query.Set("state", gitLabMRState(state))
	query.Set("scope", "all")
	query.Set("order_by", "updated_at")
	query.Set("sort", "desc")
	key := "author_username"
	if role == RoleReviewer {
		key = "reviewer_username"
	}
	query.Set(key, username)
	out, err := p.api(ctx, "GET", prRepoPath(r)+"/merge_requests?"+query.Encode(), nil)
	if err != nil {
		return nil, nil, false, err
	}
	var raw []gitlabNativePR
	if err = json.Unmarshal(out.Body, &raw); err != nil {
		return nil, nil, false, err
	}
	rows := make([]PullRequest, 0, len(raw))
	for _, item := range raw {
		row := item.row(r)
		row.Roles = []PRRole{role}
		rows = append(rows, row)
	}
	return rows, nil, prHasNext(out, len(raw), limit), nil
}

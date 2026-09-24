package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type gitlabNativePR struct {
	ID           json.Number `json:"id"`
	IID          int         `json:"iid"`
	ProjectID    json.Number `json:"project_id"`
	SourceID     json.Number `json:"source_project_id"`
	TargetID     json.Number `json:"target_project_id"`
	Title        string      `json:"title"`
	URL          string      `json:"web_url"`
	State        string      `json:"state"`
	Draft        bool        `json:"draft"`
	SHA          string      `json:"sha"`
	MergeOID     string      `json:"merge_commit_sha"`
	SquashOID    string      `json:"squash_commit_sha"`
	SourceBranch string      `json:"source_branch"`
	TargetBranch string      `json:"target_branch"`
	MergeStatus  string      `json:"detailed_merge_status"`
	ChangesCount string      `json:"changes_count"`
	ForceRemove  bool        `json:"force_remove_source_branch"`
	Auto         bool        `json:"merge_when_pipeline_succeeds"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
	Reviewers []struct {
		Username string `json:"username"`
	} `json:"reviewers"`
	User struct {
		CanMerge *bool `json:"can_merge"`
	} `json:"user"`
	DiffRefs struct {
		Base  string `json:"base_sha"`
		Head  string `json:"head_sha"`
		Start string `json:"start_sha"`
	} `json:"diff_refs"`
	Pipeline  json.RawMessage `json:"head_pipeline"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (r gitlabNativePR) row(ref PRReference) PullRequest {
	return PullRequest{Forge: GitLab, Host: ref.Host, Repo: ref.Repo, Number: r.IID, Title: r.Title, URL: r.URL, State: gitLabPRState(r.State), Draft: r.Draft, Author: r.Author.Username, Detail: PRDetailFull, CrossRepository: r.SourceID != "" && r.SourceID != r.TargetID, HeadBranch: r.SourceBranch, BaseBranch: r.TargetBranch, Mergeable: r.MergeStatus, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type gitlabPRProject struct {
	ID       json.Number `json:"id"`
	Path     string      `json:"path_with_namespace"`
	CloneURL string      `json:"http_url_to_repo"`
	Squash   *string     `json:"squash_option"`
	Trains   *bool       `json:"merge_trains_enabled"`
}

func (p *nativePRProvider) gitlabDetail(ctx context.Context, r PRReference, account string) (PRDetailResult, error) {
	out, err := p.api(ctx, "GET", prItemPath(r), nil)
	if err != nil {
		return PRDetailResult{}, err
	}
	var raw gitlabNativePR
	if err = json.Unmarshal(out.Body, &raw); err != nil {
		return PRDetailResult{}, err
	}
	projectOut, err := p.api(ctx, "GET", prRepoPath(r), nil)
	if err != nil {
		return PRDetailResult{}, err
	}
	var project gitlabPRProject
	if err = json.Unmarshal(projectOut.Body, &project); err != nil {
		return PRDetailResult{}, err
	}
	if raw.IID != r.Number || project.ID == "" || raw.TargetID != project.ID || !strings.EqualFold(project.Path, r.Repo) || !validPROID(raw.SHA) || !validPROID(raw.DiffRefs.Start) || raw.DiffRefs.Head != raw.SHA {
		return PRDetailResult{}, errors.New("provider returned a mismatched or incomplete merge request identity")
	}
	// diff_refs.start_sha belongs to the stored diff version. Bind merge and
	// checkout authority to today's target tip even if that version is old.
	targetOut, err := p.api(ctx, "GET", "projects/"+string(project.ID)+"/repository/branches/"+url.PathEscape(raw.TargetBranch), nil)
	if err != nil {
		return PRDetailResult{}, err
	}
	var target struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if json.Unmarshal(targetOut.Body, &target) != nil || target.Name != raw.TargetBranch || !validPROID(target.Commit.ID) {
		return PRDetailResult{}, errors.New("provider target branch identity is incomplete")
	}
	squashKnown := false
	squashAllowed := false
	if project.Squash != nil {
		switch *project.Squash {
		case "never":
			squashKnown = true
		case "always", "default_on", "default_off":
			squashKnown = true
			squashAllowed = true
		}
	}
	d := PRDetailResult{PullRequest: raw.row(r), Reference: r, AccountID: account, RepositoryID: string(project.ID), HeadOID: raw.SHA, BaseOID: target.Commit.ID, DiffStartOID: raw.DiffRefs.Start, BaseURL: "https://" + r.Host + "/" + project.Path + ".git", ObservedAt: time.Now().UTC(), Readiness: "unknown", CanMerge: raw.User.CanMerge != nil && *raw.User.CanMerge, Queue: raw.Auto || (project.Trains != nil && *project.Trains), SquashAllowed: squashAllowed, MergeOID: raw.MergeOID, AutoDeleteBranch: raw.ForceRemove}
	if d.MergeOID == "" {
		d.MergeOID = raw.SquashOID
	}
	if raw.SourceID == project.ID {
		d.HeadRepo = project.Path
		d.HeadURL = d.BaseURL
	} else if raw.SourceID != "" {
		sourceOut, e := p.api(ctx, "GET", "projects/"+string(raw.SourceID), nil)
		if e == nil {
			var source gitlabPRProject
			if json.Unmarshal(sourceOut.Body, &source) == nil && source.ID == raw.SourceID {
				ref := PRReference{Forge: r.Forge, Host: r.Host, Repo: source.Path}
				if ref.Validate() == nil {
					d.HeadRepo = source.Path
					d.HeadURL = "https://" + r.Host + "/" + source.Path + ".git"
				}
			}
		}
	}
	if n, e := strconv.Atoi(raw.ChangesCount); e == nil && n >= 0 {
		d.Size.Files = &n
	}
	policyComplete := raw.User.CanMerge != nil && squashKnown && project.Trains != nil && raw.MergeStatus != "" && raw.MergeStatus != "checking" && raw.MergeStatus != "unchecked" && raw.MergeStatus != "preparing"
	checksComplete := len(raw.Pipeline) > 0
	d.Checks = ChecksNone
	var pipeline *struct {
		Status string `json:"status"`
		SHA    string `json:"sha"`
		URL    string `json:"web_url"`
	}
	if nonNullJSON(raw.Pipeline) && json.Unmarshal(raw.Pipeline, &pipeline) != nil {
		checksComplete = false
		pipeline = nil
	}
	if pipeline != nil {
		switch pipeline.Status {
		case "success", "skipped":
			d.Checks = ChecksPassing
		case "failed", "canceled":
			d.Checks = ChecksFailing
		case "created", "waiting_for_resource", "preparing", "pending", "running", "scheduled", "manual":
			d.Checks = ChecksPending
		default:
			d.Checks = ""
			checksComplete = false
		}
		if pipeline.SHA != d.HeadOID {
			checksComplete = false
		}
		d.CheckDetails = []PRCheck{{Name: "pipeline", State: d.Checks, URL: pipeline.URL}}
	}
	if !checksComplete && (d.Checks == ChecksPassing || d.Checks == ChecksNone) {
		d.Checks = ""
	}
	if raw.MergeStatus == "not_approved" {
		d.ReviewDecision = "review_required"
	}
	if raw.MergeStatus == "requested_changes" {
		d.ReviewDecision = "changes_requested"
	}
	setPRReadiness(&d, raw.MergeStatus == "mergeable", policyComplete && checksComplete)
	return d, nil
}

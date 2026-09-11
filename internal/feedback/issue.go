package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

type IssueRequest struct {
	ID, Repository string
	Existing       int
	Revision       string
}
type IssuePreview struct {
	SchemaVersion int                  `json:"schema_version"`
	Kind          string               `json:"kind"`
	ReportID      string               `json:"report_id"`
	Target        forge.IssueTarget    `json:"target"`
	Existing      int                  `json:"existing_issue,omitempty"`
	Operation     string               `json:"operation"`
	Title         string               `json:"title"`
	Body          string               `json:"body"`
	Revision      string               `json:"revision"`
	Candidates    []forge.IssueSummary `json:"candidates,omitempty"`
}
type IssueResult struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	URL           string `json:"url,omitempty"`
	Code          string `json:"code,omitempty"`
	ReportID      string `json:"report_id"`
}
type issueOperation struct {
	Revision string            `json:"revision"`
	Target   forge.IssueTarget `json:"target"`
	Existing int               `json:"existing_issue,omitempty"`
	Marker   string            `json:"marker"`
	State    string            `json:"state"`
	URL      string            `json:"url,omitempty"`
}
type issueLedger struct {
	SchemaVersion int              `json:"schema_version"`
	Operations    []issueOperation `json:"operations"`
}

func (s Store) PreviewIssue(ctx context.Context, r IssueRequest) (IssuePreview, error) {
	p := IssuePreview{SchemaVersion: 1, Kind: "feedback_issue_preview", ReportID: r.ID, Existing: r.Existing, Operation: "create_issue"}
	if r.Existing < 0 {
		return p, errors.New("--existing must be a positive issue number")
	}
	if r.Existing > 0 {
		p.Operation = "comment"
	}
	target, err := forge.ParseIssueTarget(r.Repository)
	if err != nil {
		return p, err
	}
	p.Target = target
	report, err := s.Load(ctx, r.ID)
	if err != nil {
		return p, err
	}
	body, err := s.read(ctx, r.ID, "public.md")
	if err != nil {
		return p, err
	}
	p.Title = Sanitize(report.Title)
	p.Body = Sanitize(string(body))
	// Re-sanitize an edited draft at every preview and publication boundary.
	data, _ := json.Marshal(struct {
		Target      forge.IssueTarget
		Existing    int
		Title, Body string
	}{target, r.Existing, p.Title, p.Body})
	p.Revision = digest(data)
	return p, nil
}
func (s Store) SearchIssues(ctx context.Context, r IssueRequest, reporter forge.IssueReporter) (IssuePreview, error) {
	p, err := s.PreviewIssue(ctx, r)
	if err != nil {
		return p, err
	}
	p.Candidates, err = reporter.Search(ctx, p.Target, p.Title)
	for i := range p.Candidates {
		p.Candidates[i].Title = Sanitize(p.Candidates[i].Title)
	}
	return p, err
}
func (s Store) PublishIssue(ctx context.Context, r IssueRequest, reporter forge.IssueReporter) (IssueResult, error) {
	result := IssueResult{SchemaVersion: 1, Kind: "feedback_issue_result", Status: "not_published", ReportID: r.ID}
	err := s.withLock(ctx, r.ID, func() error {
		p, err := s.PreviewIssue(ctx, r)
		if err != nil {
			return err
		}
		if r.Revision == "" || r.Revision != p.Revision {
			return ErrStale
		}
		ledger := issueLedger{SchemaVersion: 1, Operations: []issueOperation{}}
		raw, err := s.read(ctx, r.ID, "issues.json")
		if err == nil {
			if json.Unmarshal(raw, &ledger) != nil || ledger.SchemaVersion != 1 {
				return errors.New("invalid issue publication ledger")
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for i := range ledger.Operations {
			prior := &ledger.Operations[i]
			if prior.Target != p.Target || prior.Existing != p.Existing {
				continue
			}
			if prior.State == "confirmed" && (p.Existing == 0 || prior.Revision == p.Revision) {
				result.Status = "confirmed"
				result.URL = prior.URL
				return nil
			}
			if prior.State == "unknown" {
				url, err := reporter.FindMarker(ctx, prior.Target, prior.Existing, prior.Marker)
				if err != nil || url == "" {
					result.Status = "unknown"
					result.Code = "reconcile_required"
					return errors.New("publication outcome remains unknown; inspect GitHub before creating another report")
				}
				prior.State = "confirmed"
				prior.URL = url
				result.Status = "confirmed"
				result.URL = url
				return s.writeJSON(context.WithoutCancel(ctx), r.ID, "issues.json", ledger, true)
			}
		}
		marker := fmt.Sprintf("<!-- dev-feedback:%s:%s -->", r.ID, p.Revision)
		operation := issueOperation{Revision: p.Revision, Target: p.Target, Existing: p.Existing, Marker: marker, State: "unknown"}
		ledger.Operations = append(ledger.Operations, operation)
		body := strings.TrimSpace(p.Body) + "\n\n" + marker + "\n"
		if err = s.write(ctx, r.ID, "publication-"+p.Revision+".md", []byte(body), false); err != nil {
			old, readErr := s.read(ctx, r.ID, "publication-"+p.Revision+".md")
			if readErr != nil || string(old) != body {
				return err
			}
		}
		// Persist unknown before the request; a crash can never authorize a duplicate.
		if err = s.writeJSON(ctx, r.ID, "issues.json", ledger, true); err != nil {
			return err
		}
		url, publishErr := reporter.Publish(ctx, forge.IssuePublication{Target: p.Target, Existing: p.Existing, Title: p.Title, Body: body})
		last := &ledger.Operations[len(ledger.Operations)-1]
		if publishErr == nil {
			last.State = "confirmed"
			last.URL = url
			result.Status = "confirmed"
			result.URL = url
		} else {
			var issueErr *forge.IssueError
			result.Status = "unknown"
			result.Code = "request_failed"
			if errors.As(publishErr, &issueErr) {
				result.Code = issueErr.Code
				if !issueErr.Unknown {
					last.State = "failed"
					result.Status = "not_published"
				}
			}
		}
		saveErr := s.writeJSON(context.WithoutCancel(ctx), r.ID, "issues.json", ledger, true)
		return errors.Join(publishErr, saveErr)
	})
	if errors.Is(err, ErrStale) {
		result.Code = "stale_preview"
	}
	return result, err
}

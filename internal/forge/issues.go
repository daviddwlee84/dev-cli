package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const DefaultIssueRepository = "github.com/daviddwlee84/dev-cli"

type IssueTarget struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
}

func (t IssueTarget) String() string { return t.Host + "/" + t.Repository }

var issueComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`)
var issueHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func ParseIssueTarget(value string) (IssueTarget, error) {
	if value == "" {
		value = DefaultIssueRepository
	}
	parts := strings.Split(value, "/")
	if len(parts) == 2 {
		parts = append([]string{"github.com"}, parts...)
	}
	if len(parts) != 3 || !issueHost.MatchString(parts[0]) || !issueComponent.MatchString(parts[1]) || !issueComponent.MatchString(parts[2]) || strings.Contains(parts[0], "..") {
		return IssueTarget{}, errors.New("issue repository must be [host/]owner/name")
	}
	return IssueTarget{Host: strings.ToLower(parts[0]), Repository: strings.ToLower(parts[1] + "/" + parts[2])}, nil
}

type IssueSummary struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}
type IssuePublication struct {
	Target      IssueTarget
	Existing    int
	Title, Body string
}
type IssueError struct {
	Code    string
	Unknown bool
}

func (e *IssueError) Error() string { return "GitHub feedback operation: " + e.Code }

// IssueReporter is optional; adding feedback does not expand the Forge interface
// or require GitLab/Azure adapters to implement GitHub issue publication.
type IssueReporter interface {
	Search(context.Context, IssueTarget, string) ([]IssueSummary, error)
	Publish(context.Context, IssuePublication) (string, error)
	FindMarker(context.Context, IssueTarget, int, string) (string, error)
}
type GitHubIssueReporter struct{ Runner sshhost.Runner }

func (g GitHubIssueReporter) run(ctx context.Context, write bool, args []string, input []byte) ([]byte, error) {
	runner := g.Runner
	if runner == nil {
		runner = sshhost.ExecRunner{}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, sshhost.RunRequest{Name: "gh", Args: args, Stdin: input, Env: []string{"GH_PROMPT_DISABLED=1"}, Display: "GitHub feedback operation"})
	if err != nil {
		code := "network_unavailable"
		unknown := write
		if errors.Is(err, exec.ErrNotFound) {
			code = "missing_gh"
			unknown = false
		}
		return nil, &IssueError{Code: code, Unknown: unknown}
	}
	if result.ExitCode != 0 {
		code := "request_failed"
		unknown := write
		message := strings.ToLower(string(result.Stderr))
		switch {
		case strings.Contains(message, "gh auth login") || strings.Contains(message, "http 401") || strings.Contains(message, "not logged"):
			code = "unauthenticated"
			unknown = false
		case strings.Contains(message, "http 403") || strings.Contains(message, "http 404"):
			code = "permission_denied"
			unknown = false
		case strings.Contains(message, "http 422"):
			code = "request_rejected"
			unknown = false
		}
		return nil, &IssueError{Code: code, Unknown: unknown}
	}
	if result.StdoutTruncated {
		return nil, &IssueError{Code: "response_too_large", Unknown: write}
	}
	return result.Stdout, nil
}
func issueAPI(t IssueTarget, endpoint string) []string {
	return []string{"api", "--hostname", t.Host, endpoint}
}
func validateIssueTarget(t IssueTarget) error {
	valid, err := ParseIssueTarget(t.String())
	if err != nil || valid != t {
		return errors.New("invalid issue target")
	}
	return nil
}
func validIssueURL(raw string, t IssueTarget, existing int) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, t.Host) || u.User != nil || u.RawQuery != "" {
		return false
	}
	prefix := "/" + t.Repository + "/issues/"
	if len(u.Path) < len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) {
		return false
	}
	number, err := strconv.Atoi(u.Path[len(prefix):])
	return err == nil && number > 0 && (existing == 0 || number == existing)
}
func (g GitHubIssueReporter) Publish(ctx context.Context, p IssuePublication) (string, error) {
	if err := validateIssueTarget(p.Target); err != nil {
		return "", err
	}
	if p.Existing < 0 {
		return "", errors.New("existing issue must be positive")
	}
	endpoint := "repos/" + p.Target.Repository + "/issues"
	payload := map[string]string{"body": p.Body}
	if p.Existing > 0 {
		endpoint += "/" + strconv.Itoa(p.Existing) + "/comments"
	} else {
		payload["title"] = p.Title
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	args := append(issueAPI(p.Target, endpoint), "--method", "POST", "--input", "-")
	raw, err := g.run(ctx, true, args, input)
	if err != nil {
		return "", err
	}
	var response struct {
		URL string `json:"html_url"`
	}
	if json.Unmarshal(raw, &response) != nil || !validIssueURL(response.URL, p.Target, p.Existing) {
		return "", &IssueError{Code: "unconfirmed_response", Unknown: true}
	}
	return response.URL, nil
}
func (g GitHubIssueReporter) Search(ctx context.Context, t IssueTarget, title string) ([]IssueSummary, error) {
	if err := validateIssueTarget(t); err != nil {
		return nil, err
	}
	phrase := strings.NewReplacer("\"", " ", "\\", " ").Replace(title)
	query := "repo:" + t.Repository + " is:issue in:title \"" + phrase + "\""
	endpoint := "search/issues?q=" + url.QueryEscape(query) + "&per_page=20"
	raw, err := g.run(ctx, false, issueAPI(t, endpoint), nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Items []struct {
			Number int    `json:"number"`
			URL    string `json:"html_url"`
			Title  string `json:"title"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, &IssueError{Code: "invalid_response"}
	}
	results := []IssueSummary{}
	for _, item := range response.Items {
		if validIssueURL(item.URL, t, 0) && len(results) < 20 {
			results = append(results, IssueSummary{Number: item.Number, URL: item.URL, Title: item.Title})
		}
	}
	return results, nil
}
func (g GitHubIssueReporter) FindMarker(ctx context.Context, t IssueTarget, existing int, marker string) (string, error) {
	if err := validateIssueTarget(t); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := "repos/" + t.Repository + "/issues"
	query := "state=all&sort=created&direction=desc&"
	if existing > 0 {
		endpoint += "/" + strconv.Itoa(existing) + "/comments"
		query = ""
	}
	found := ""
	for page := 1; page <= 10; page++ {
		raw, err := g.run(ctx, false, issueAPI(t, fmt.Sprintf("%s?%sper_page=100&page=%d", endpoint, query, page)), nil)
		if err != nil {
			return "", err
		}
		var rows []struct {
			Body string `json:"body"`
			URL  string `json:"html_url"`
		}
		if json.Unmarshal(raw, &rows) != nil {
			return "", &IssueError{Code: "invalid_response"}
		}
		for _, row := range rows {
			if strings.Contains(row.Body, marker) && validIssueURL(row.URL, t, existing) {
				if found != "" && found != row.URL {
					return "", &IssueError{Code: "ambiguous_remote_result", Unknown: true}
				}
				found = row.URL
			}
		}
		if len(rows) < 100 {
			return found, nil
		}
	}
	// A bounded incomplete scan cannot prove that another matching result is absent.
	return "", &IssueError{Code: "reconciliation_incomplete", Unknown: true}
}

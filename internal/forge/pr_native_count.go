package forge

import (
	"context"
	"encoding/json"
	"strings"
)

func (p *nativePRProvider) githubCount(ctx context.Context, r PRReference, state PRState) *int {
	owner, name, _ := strings.Cut(r.Repo, "/")
	var states []string
	if state != PRStateAll {
		states = []string{strings.ToUpper(string(state))}
	}
	query := `query($owner:String!,$name:String!,$states:[PullRequestState!]){repository(owner:$owner,name:$name){pullRequests(first:1,states:$states){totalCount}}}`
	input, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"owner": owner, "name": name, "states": states}})
	out, err := prCommand(ctx, "gh", []string{"api", "--hostname", p.host, "graphql", "--input", "-"}, input)
	if err != nil {
		return nil
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository struct {
				PRs struct {
					Count *int `json:"totalCount"`
				} `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(out.Body, &result) != nil || len(result.Errors) > 0 {
		return nil
	}
	return result.Data.Repository.PRs.Count
}

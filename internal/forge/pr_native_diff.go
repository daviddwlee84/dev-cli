package forge

import (
	"context"
	"encoding/json"
	"strconv"
)

// GitLab documents raw_diffs as subject to the same provider diff limits. Read
// the corresponding stored version's completeness instead of treating HTTP
// 200 as evidence that all files and hunks were returned.
func (p *nativePRProvider) gitlabDiffCompleteness(ctx context.Context, d PRDetailResult) (bool, string) {
	unknown := "provider diff completeness is unknown; inspect a local checkout for the full diff"
	path := prItemPath(d.Reference) + "/versions"
	out, err := p.api(ctx, "GET", path+"?per_page=1", nil)
	if err != nil {
		return false, unknown
	}
	var versions []struct {
		ID    int    `json:"id"`
		Head  string `json:"head_commit_sha"`
		Start string `json:"start_commit_sha"`
		State string `json:"state"`
	}
	if json.Unmarshal(out.Body, &versions) != nil || len(versions) != 1 || versions[0].ID <= 0 {
		return false, unknown
	}
	v := versions[0]
	if v.Head != d.HeadOID || v.Start != d.DiffStartOID {
		return false, "merge request diff changed; refresh before relying on this live preview"
	}
	if v.State != "collected" {
		return false, "provider diff is incomplete or truncated; inspect a local checkout for the full diff"
	}
	out, err = p.api(ctx, "GET", path+"/"+strconv.Itoa(v.ID), nil)
	if err != nil {
		return false, unknown
	}
	var version struct {
		Head  string `json:"head_commit_sha"`
		Start string `json:"start_commit_sha"`
		State string `json:"state"`
		Diffs []struct {
			Collapsed bool `json:"collapsed"`
			TooLarge  bool `json:"too_large"`
		} `json:"diffs"`
	}
	if json.Unmarshal(out.Body, &version) != nil || version.Head != d.HeadOID || version.Start != d.DiffStartOID || version.State != "collected" {
		return false, unknown
	}
	for _, file := range version.Diffs {
		if file.Collapsed || file.TooLarge {
			return false, "provider omitted collapsed or oversized file diffs; inspect a local checkout for the full diff"
		}
	}
	if d.Size.Files == nil || len(version.Diffs) != *d.Size.Files {
		return false, unknown
	}
	return true, ""
}

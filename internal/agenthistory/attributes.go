package agenthistory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

const archiveAttributes = "sessions/** -text -ident\nrecords/** -text -ident\n"

func contentPath(project string, item snapshot) string {
	return filepath.ToSlash(filepath.Join("projects", project, "sessions", item.Provider, item.SessionID, item.DesiredDigest+".md"))
}

// Preserve bytes without bypassing an archive's encryption, LFS or encoding
// filters. Owned text attributes are explicit effects of the reviewed plan.
func checkArchiveAttributes(ctx context.Context, root string, p planRecord, installed bool) (string, error) {
	var paths strings.Builder
	for _, item := range p.Snapshots {
		paths.WriteString(contentPath(p.Policy.ProjectID, item))
		paths.WriteByte(0)
	}
	paths.WriteString("projects/" + p.Policy.ProjectID + "/records/" + p.View.ID + "/000.json")
	paths.WriteByte(0)
	data, e := runGit(ctx, root, []byte(paths.String()), 8<<20, "check-attr", "-z", "--stdin", "filter", "working-tree-encoding", "text", "ident")
	if e != nil {
		return "", e
	}
	fields := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(fields)%3 != 0 {
		return "", errors.New("invalid archive attribute observation")
	}
	for i := 0; i < len(fields); i += 3 {
		attribute, value := fields[i+1], fields[i+2]
		if (attribute == "filter" || attribute == "working-tree-encoding") && value != "unspecified" && value != "unset" {
			return "", errors.New("archive has content filters or encoding conversions; use a separate archive or its native Git workflow")
		}
		if installed && (attribute == "text" || attribute == "ident") && value != "unset" {
			return "", errors.New("archive attributes prevent preserving exact snapshot bytes")
		}
	}
	return hash(data), nil
}

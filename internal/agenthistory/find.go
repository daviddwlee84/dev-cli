package agenthistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type Query struct {
	Session, Commit, Text string
	All                   bool
	Limit                 int
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var contentDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Service) Find(ctx context.Context, q Query) ([]Match, error) {
	if !s.Configured || s.Binding.Archive == "" {
		return nil, errors.New("configure an archive binding before searching")
	}
	root, err := archiveCheckout(ctx, s.Root, s.Binding.Archive)
	if err != nil {
		return nil, err
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return nil, errors.New("search limit must be 1 to 1000")
	}
	if q.Commit != "" && !objectID.MatchString(q.Commit) {
		return nil, errors.New("commit filter requires a full commit ID")
	}
	provider, id := "", ""
	if q.Session != "" {
		provider, id, err = parseSession(q.Session)
		if err != nil {
			return nil, err
		}
	}
	_, head, _, err := checkoutToken(ctx, root)
	if err != nil {
		return nil, err
	}
	if head == "unborn" {
		return []Match{}, nil
	}
	prefix := "projects/" + s.Policy.ProjectID + "/records/"
	if q.All {
		prefix = "projects/"
	}
	names, err := runGit(ctx, root, nil, 8<<20, "ls-tree", "--name-only", "-r", "-z", head, "--", prefix)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) != 5 || parts[0] != "projects" || !validID(parts[1]) || parts[2] != "records" || !validID(parts[3]) || !strings.HasSuffix(parts[4], ".json") {
			continue
		}
		b, e := runGit(ctx, root, nil, 1<<20, "cat-file", "blob", head+":"+name)
		if e != nil {
			return nil, e
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || r.Version != 1 || r.ProjectID != parts[1] || !component.MatchString(r.Provider) || !validID(r.SessionID) || !relative(r.SourcePath) || !contentDigest.MatchString(r.Digest) || !contentDigest.MatchString(r.OriginalDigest) {
			return nil, errors.New("invalid archive metadata")
		}
		expected := filepath.ToSlash(filepath.Join("projects", r.ProjectID, "sessions", r.Provider, r.SessionID, r.Digest+".md"))
		if r.Content != expected {
			return nil, errors.New("archive metadata points outside its session")
		}
		if provider != "" && (r.Provider != provider || r.SessionID != id) || q.Commit != "" && r.SourceCommit != q.Commit {
			continue
		}
		data, e := runGit(ctx, root, nil, MaxFileBytes, "cat-file", "blob", head+":"+r.Content)
		if e != nil || hash(data) != r.Digest {
			return nil, errors.New("archive content verification failed")
		}
		match := Match{Record: r, Path: filepath.Join(root, filepath.FromSlash(r.Content))}
		if q.Text != "" {
			for line, body := range bytes.Split(data, []byte{'\n'}) {
				if bytes.Contains(body, []byte(q.Text)) {
					match.Lines = append(match.Lines, line+1)
					if len(match.Lines) >= 1000 {
						break
					}
				}
			}
			if len(match.Lines) == 0 {
				continue
			}
		}
		matches = append(matches, match)
		if len(matches) >= q.Limit {
			break
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Record.Created.After(matches[j].Record.Created) })
	if matches == nil {
		matches = []Match{}
	}
	return matches, nil
}

// Readiness includes ignored files in archive mode. Its fingerprint belongs in
// a destructive lifecycle plan and must be refreshed immediately before apply.
type Readiness struct {
	Configured  bool     `json:"configured"`
	Ready       bool     `json:"ready"`
	Fingerprint string   `json:"fingerprint"`
	Pending     []string `json:"pending"`
}

func (s *Service) Readiness(ctx context.Context) (Readiness, error) {
	r := Readiness{Configured: s.Configured, Ready: true, Pending: []string{}}
	if !s.Configured || s.Policy.Mode != "archive" {
		r.Fingerprint = hash(encode(s.Policy))
		return r, nil
	}
	if e := s.validateCapture(ctx); e != nil {
		return r, e
	}
	capture, err := s.captureFingerprint(ctx)
	if err != nil {
		return r, err
	}
	files, err := s.sources(ctx)
	if err != nil {
		return r, err
	}
	var records []planRecord
	entries, e := os.ReadDir(filepath.Join(s.Dir, "plans"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return r, errors.New("archive receipts unavailable")
	}
	if len(entries) > 10000 {
		return r, errors.New("archive receipt inventory exceeds limit")
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		p, e := s.load(ctx, entry.Name())
		if e != nil {
			return r, e
		}
		if p.View.Kind == "artifact_archive" && p.View.Status == "complete" && p.Policy.ProjectID == s.Policy.ProjectID {
			if e = s.verifyReceipt(ctx, p); e != nil {
				continue
			}
			records = append(records, p)
		}
	}
	proof := []string{string(encode(s.Policy)), string(encode(s.Binding)), capture}
	for _, p := range records {
		repo, e := gitx.Discover(ctx, p.Binding.Archive)
		if e != nil {
			return r, e
		}
		id, e := gitx.DirectoryIdentity(repo.GitCommonDir)
		if e != nil {
			return r, e
		}
		proof = append(proof, p.View.ID, p.View.ArchiveCommit, id)
	}
	for _, file := range files {
		data, e := safefile.ReadStablePath(ctx, s.capturePath(file), MaxFileBytes)
		if e != nil {
			return r, errors.New("artifact source cannot be verified before cleanup")
		}
		digest := hash(data)
		proof = append(proof, file, digest)
		found := false
		for _, record := range records {
			for _, snapshot := range record.Snapshots {
				if snapshot.Logical == file && snapshot.Digest == digest {
					found = true
				}
			}
		}
		if !found {
			r.Pending = append(r.Pending, file)
			r.Ready = false
		}
	}
	if current, e := s.captureFingerprint(ctx); e != nil || current != capture {
		return r, ErrStale
	}
	r.Fingerprint = hash([]byte(strings.Join(proof, "\x00")))
	return r, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	result := Status{SchemaVersion: 1, Configured: s.Configured, Policy: s.Policy, PendingFiles: []string{}}
	if !s.Configured {
		result.Notices = []string{"No artifact policy; existing behavior is unchanged."}
		return result, nil
	}
	if s.Binding.Version != 0 {
		copy := s.Binding
		result.Binding = &copy
	}
	args := []string{"ls-files", "-z", "--"}
	for _, path := range s.Policy.Paths {
		args = append(args, ":(literal)"+path)
	}
	if len(s.Policy.Paths) == 0 {
		return result, nil
	}
	files, err := runGit(ctx, s.Root, nil, 8<<20, args...)
	if err != nil {
		return result, err
	}
	for _, name := range strings.Split(string(files), "\x00") {
		if name != "" {
			result.TrackedFiles++
		}
	}
	ready, err := s.Readiness(ctx)
	if err != nil {
		return result, err
	}
	result.PendingFiles = ready.Pending
	if s.Policy.Mode == "archive" && result.TrackedFiles > 0 {
		result.Notices = append(result.Notices, "Tracked history remains in code commits until an explicit untrack migration.")
	}
	result.Notices = append(result.Notices, "Remote synchronization is not queried by status.")
	return result, nil
}

// VerifyArchiveReceipt is used by the existing post-writer lifecycle. The
// stored plan is signed locally; archive Git bytes are verified on each call.
func (s *Service) VerifyArchiveReceipt(ctx context.Context, id string) (string, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return "", e
	}
	if e = s.verifyReceipt(ctx, p); e != nil {
		return "", e
	}
	return p.View.ArchiveCommit, nil
}

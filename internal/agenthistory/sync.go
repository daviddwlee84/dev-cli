package agenthistory

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

func validateRemote(remote string) error {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n\t") {
		return errors.New("invalid Git remote destination")
	}
	if filepath.IsAbs(remote) {
		return nil
	}
	if strings.Contains(remote, " ") {
		return errors.New("encode spaces in remote URLs")
	}
	if strings.Contains(remote, "://") {
		u, e := url.Parse(remote)
		if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "http") {
			return errors.New("use an ordinary HTTPS/SSH Git URL or an absolute local repository path")
		}
		if u.User != nil {
			if u.Scheme != "ssh" {
				return errors.New("use the Git credential helper instead of credentials in remote URLs")
			}
			if _, present := u.User.Password(); present {
				return errors.New("keep credentials out of remote URLs")
			}
		}
		return nil
	}
	left, right, ok := strings.Cut(remote, ":")
	if !ok || left == "" || right == "" || strings.ContainsAny(left, "/\\") || strings.Contains(right, ":") {
		return errors.New("custom Git transports are not supported for archive publication")
	}
	return nil
}
func remoteIdentity(remote string) string {
	if filepath.IsAbs(remote) {
		return filepath.Clean(remote)
	}
	if u, e := url.Parse(remote); e == nil && u.Host != "" {
		return strings.ToLower(u.Hostname()) + "/" + strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}
	left, right, ok := strings.Cut(remote, ":")
	if ok {
		if _, host, yes := strings.Cut(left, "@"); yes {
			left = host
		}
		return strings.ToLower(left) + "/" + strings.TrimSuffix(strings.Trim(right, "/"), ".git")
	}
	return remote
}
func (s *Service) guardRemote(ctx context.Context, remote string) error {
	if e := validateRemote(remote); e != nil {
		return e
	}
	if filepath.IsAbs(remote) {
		if remoteIdentity(remote) == remoteIdentity(s.Root) || remoteIdentity(remote) == remoteIdentity(s.Common) {
			return errors.New("archive publication cannot target the source repository")
		}
	}
	names, e := gitText(ctx, s.Root, "remote")
	if e != nil {
		return e
	}
	for _, name := range strings.Fields(names) {
		for _, flag := range []string{"--all", "--push"} {
			values, e := gitText(ctx, s.Root, "remote", "get-url", flag, name)
			if e != nil {
				return e
			}
			for _, source := range strings.Split(values, "\n") {
				if remoteIdentity(source) == remoteIdentity(remote) {
					return errors.New("archive destination matches a source repository remote")
				}
			}
		}
	}
	return nil
}
func remoteRefs(ctx context.Context, root, remote string) (map[string]string, error) {
	b, e := runGit(ctx, root, nil, 8<<20, "ls-remote", "--refs", "--", remote)
	if e != nil {
		return nil, e
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !objectID.MatchString(fields[0]) || !strings.HasPrefix(fields[1], "refs/") {
			return nil, errors.New("invalid remote ref observation")
		}
		refs[fields[1]] = fields[0]
	}
	return refs, nil
}

func (s *Service) PreviewSync(ctx context.Context, direction, remote, branch string) (Plan, error) {
	p, e := s.newPlan(ctx, "artifact_sync_"+direction)
	if e != nil {
		return p.View, e
	}
	if direction != "push" && direction != "pull" {
		return p.View, errors.New("choose push or pull")
	}
	archive, e := archiveCheckout(ctx, s.Root, s.Binding.Archive)
	if e != nil {
		return p.View, e
	}
	if e = cleanArchive(ctx, archive); e != nil {
		return p.View, e
	}
	if remote == "" {
		remote = "origin"
	}
	if !component.MatchString(remote) {
		return p.View, errors.New("choose a configured archive remote name")
	}
	args := []string{"remote", "get-url", "--all", remote}
	if direction == "push" {
		args = []string{"remote", "get-url", "--push", "--all", remote}
	}
	urls, e := gitText(ctx, archive, args...)
	if e != nil || urls == "" || strings.Contains(urls, "\n") {
		return p.View, errors.New("archive synchronization needs one configured remote URL")
	}
	if e = s.guardRemote(ctx, urls); e != nil {
		return p.View, e
	}
	current, e := gitText(ctx, archive, "symbolic-ref", "--short", "HEAD")
	if e != nil {
		return p.View, errors.New("archive needs a named branch")
	}
	if branch == "" {
		branch = current
	}
	if branch != current {
		return p.View, errors.New("select the archive's currently checked-out branch")
	}
	if _, e = gitText(ctx, archive, "check-ref-format", "refs/heads/"+branch); e != nil {
		return p.View, e
	}
	p.ArchiveToken, p.ArchiveHead, p.ArchiveIndex, e = checkoutToken(ctx, archive)
	if e != nil {
		return p.View, e
	}
	refs, e := remoteRefs(ctx, archive, urls)
	if e != nil {
		return p.View, e
	}
	p.RemoteOID = refs["refs/heads/"+branch]
	if direction == "pull" && p.RemoteOID == "" {
		return p.View, errors.New("remote branch does not exist")
	}
	if direction == "push" {
		if p.ArchiveHead == "unborn" {
			return p.View, errors.New("archive has no commit to publish")
		}
		if p.RemoteOID != "" {
			if _, e = gitText(ctx, archive, "merge-base", "--is-ancestor", p.RemoteOID, p.ArchiveHead); e != nil {
				return p.View, errors.New("push is not a proven fast-forward; fetch and resolve archive history with Git first")
			}
		}
	}
	p.Binding.Archive = archive
	p.View.Archive = archive
	p.RemoteURL = urls
	p.View.Remote = urls
	p.RemoteBranch = branch
	p.RemoteName = remote
	p.View.Notices = []string{"This explicit operation contacts the archive remote. No native agent data or databases are synchronized.", "Pull only fast-forwards a clean checkout. Push uses a pinned ref lease after proving fast-forward ancestry; divergence is never overwritten."}
	if e = s.save(ctx, &p); e != nil {
		return p.View, e
	}
	return p.View, nil
}

func (s *Service) ApplySync(ctx context.Context, id string) (Plan, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return p.View, e
	}
	if p.View.Kind != "artifact_sync_push" && p.View.Kind != "artifact_sync_pull" {
		return p.View, errors.New("plan is not archive synchronization")
	}
	if p.View.Status == "complete" {
		return p.View, nil
	}
	if p.View.Status != "prepared" {
		return p.View, errors.New("inspect partial synchronization before retrying")
	}
	e = lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		root, e := archiveCheckout(ctx, s.Root, p.Binding.Archive)
		if e != nil {
			return e
		}
		repo, e := gitx.Discover(ctx, root)
		if e != nil {
			return e
		}
		return gitx.WithLifecycleLock(ctx, repo.GitCommonDir, func() error {
			if e = s.current(ctx, p); e != nil {
				return e
			}
			if e = s.guardRemote(ctx, p.RemoteURL); e != nil {
				return e
			}
			urlArgs := []string{"remote", "get-url", "--all", p.RemoteName}
			if p.View.Kind == "artifact_sync_push" {
				urlArgs = []string{"remote", "get-url", "--push", "--all", p.RemoteName}
			}
			currentURL, e := gitText(ctx, root, urlArgs...)
			if e != nil || currentURL != p.RemoteURL {
				return ErrStale
			}
			token, _, _, e := checkoutToken(ctx, root)
			if e != nil || token != p.ArchiveToken {
				return ErrStale
			}
			if e = cleanArchive(ctx, root); e != nil {
				return e
			}
			refs, e := remoteRefs(ctx, root, p.RemoteURL)
			if e != nil || refs["refs/heads/"+p.RemoteBranch] != p.RemoteOID {
				return ErrStale
			}
			p.View.Status = "applying"
			if e = s.save(ctx, &p); e != nil {
				return e
			}
			if p.View.Kind == "artifact_sync_push" {
				if p.RemoteOID != "" {
					if _, e = gitText(ctx, root, "merge-base", "--is-ancestor", p.RemoteOID, p.ArchiveHead); e != nil {
						return ErrStale
					}
				}
				ref := "refs/heads/" + p.RemoteBranch
				if _, e = gitText(ctx, root, "push", "--atomic", "--force-with-lease="+ref+":"+p.RemoteOID, "--", p.RemoteURL, p.ArchiveHead+":"+ref); e != nil {
					return errors.New("archive push failed or is unverified; inspect the remote before retrying")
				}
				fresh, e := remoteRefs(ctx, root, p.RemoteURL)
				if e != nil || fresh[ref] != p.ArchiveHead {
					return errors.New("archive push result could not be verified")
				}
			} else {
				if _, e = gitText(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", "--", p.RemoteURL, p.RemoteOID); e != nil {
					return e
				}
				token, _, _, e := checkoutToken(ctx, root)
				if e != nil || token != p.ArchiveToken {
					return ErrStale
				}
				if e = cleanArchive(ctx, root); e != nil {
					return e
				}
				if _, e = gitText(ctx, root, "merge", "--ff-only", p.RemoteOID); e != nil {
					return errors.New("archive cannot fast-forward; resolve it with Git before preparing another sync")
				}
				got, e := gitText(ctx, root, "rev-parse", "HEAD")
				if e != nil || got != p.RemoteOID {
					return errors.New("archive fast-forward result is unverified")
				}
			}
			p.View.Status = "complete"
			return s.save(ctx, &p)
		})
	})
	if e != nil && p.View.Status == "applying" {
		p.View.Status = "partial"
		_ = s.save(context.Background(), &p)
	}
	return p.View, e
}

func bareToken(ctx context.Context, root string) (string, map[string]string, error) {
	id, e := gitx.DirectoryIdentity(root)
	if e != nil {
		return "", nil, e
	}
	bare, e := gitText(ctx, root, "rev-parse", "--is-bare-repository")
	if e != nil || bare != "true" {
		return "", nil, errors.New("migration backup must be a bare Git repository")
	}
	refs, e := frozenRefs(ctx, root)
	if e != nil {
		return "", nil, e
	}
	return hash(append([]byte(id), encode(refs)...)), refs, nil
}
func (s *Service) PreviewBackup(ctx context.Context, migrationID, remote string) (Plan, error) {
	m, e := s.load(ctx, migrationID)
	if e != nil {
		return Plan{}, e
	}
	if m.View.Status != "complete" || (m.View.Kind != "artifact_migrate_untrack" && m.View.Kind != "artifact_migrate_split") {
		return Plan{}, errors.New("select a completed verified migration")
	}
	p, e := s.newPlan(ctx, "artifact_backup")
	if e != nil {
		return p.View, e
	}
	if e = s.guardRemote(ctx, remote); e != nil {
		return p.View, e
	}
	original := filepath.Join(m.View.Output, "original.git")
	p.ArchiveToken, p.Refs, e = bareToken(ctx, original)
	if e != nil {
		return p.View, e
	}
	if !bytes.Equal(encode(p.Refs), encode(m.Refs)) {
		return p.View, errors.New("original backup refs changed")
	}
	refs, e := remoteRefs(ctx, original, remote)
	if e != nil {
		return p.View, e
	}
	if len(refs) != 0 {
		return p.View, errors.New("backup destination must be empty; existing refs are never overwritten")
	}
	p.View.Archive = original
	p.View.Remote = remote
	p.RemoteURL = remote
	p.View.Notices = []string{"Publishes every frozen named Git ref from the verified original, with empty-ref leases. No source ref is changed.", "This is raw, unscanned Git history. Selected uncommitted snapshots remain in the local migration output and are not part of the Git remote backup."}
	if e = s.save(ctx, &p); e != nil {
		return p.View, e
	}
	return p.View, nil
}
func (s *Service) ApplyBackup(ctx context.Context, id string) (Plan, error) {
	p, e := s.load(ctx, id)
	if e != nil {
		return p.View, e
	}
	if p.View.Kind != "artifact_backup" {
		return p.View, errors.New("plan is not backup publication")
	}
	if p.View.Status == "complete" {
		return p.View, nil
	}
	if p.View.Status != "prepared" {
		return p.View, errors.New("inspect partial backup publication before retrying")
	}
	e = lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		if e = s.current(ctx, p); e != nil {
			return e
		}
		if e = s.guardRemote(ctx, p.RemoteURL); e != nil {
			return e
		}
		token, _, e := bareToken(ctx, p.View.Archive)
		if e != nil || token != p.ArchiveToken {
			return ErrStale
		}
		remote, e := remoteRefs(ctx, p.View.Archive, p.RemoteURL)
		if e != nil || len(remote) != 0 {
			return ErrStale
		}
		if _, e = gitText(ctx, p.View.Archive, "fsck", "--full", "--no-reflogs"); e != nil {
			return errors.New("backup object verification failed")
		}
		var names []string
		for name := range p.Refs {
			if name != "HEAD" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 || len(names) > 1024 {
			return errors.New("backup publication requires 1 to 1024 named refs")
		}
		args := []string{"push", "--atomic"}
		for _, name := range names {
			args = append(args, "--force-with-lease="+name+":")
		}
		args = append(args, "--", p.RemoteURL)
		for _, name := range names {
			args = append(args, p.Refs[name]+":"+name)
		}
		p.View.Status = "applying"
		if e = s.save(ctx, &p); e != nil {
			return e
		}
		if _, e = gitText(ctx, p.View.Archive, args...); e != nil {
			return errors.New("backup push failed or is unverified; original local backup is preserved")
		}
		observed, e := remoteRefs(ctx, p.View.Archive, p.RemoteURL)
		if e != nil {
			return e
		}
		for _, name := range names {
			if observed[name] != p.Refs[name] {
				return errors.New("backup ref verification failed")
			}
		}
		p.View.Completed = append(p.View.Completed, names...)
		p.View.Status = "complete"
		return s.save(ctx, &p)
	})
	if e != nil && p.View.Status == "applying" {
		p.View.Status = "partial"
		_ = s.save(context.Background(), &p)
	}
	return p.View, e
}

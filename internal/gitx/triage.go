package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BranchState compares every local head with locally cached upstream evidence.
// Missing tracking configuration is not proof that a commit is unpublished.
type BranchState struct {
	Ref             string    `json:"ref"`
	OID             string    `json:"oid"`
	Upstream        string    `json:"upstream,omitempty"`
	UpstreamOID     string    `json:"upstream_oid,omitempty"`
	Remote          string    `json:"remote,omitempty"`
	RemoteRef       string    `json:"remote_ref,omitempty"`
	PushRemote      string    `json:"push_remote,omitempty"`
	PushRef         string    `json:"push_ref,omitempty"`
	Ahead           int       `json:"ahead"`
	Behind          int       `json:"behind"`
	ComparisonKnown bool      `json:"comparison_known"`
	Error           string    `json:"error,omitempty"`
	CommittedAt     time.Time `json:"committed_at"`
}

func BranchStates(ctx context.Context, dir string) ([]BranchState, error) {
	out, err := runEnv(ctx, dir, []string{"GIT_OPTIONAL_LOCKS=0"}, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(upstream)%00%(upstream:remotename)%00%(upstream:remoteref)%00%(push:remotename)%00%(push:remoteref)%00%(committerdate:unix)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	result := []BranchState{}
	pushDefault, _ := run(ctx, dir, "config", "--get", "push.default")
	if pushDefault == "" {
		pushDefault = "simple"
	}
	pushSpecs := map[string]bool{}

	for _, line := range lines(out) {
		v := strings.Split(line, "\x00")
		if len(v) != 8 {
			return nil, fmt.Errorf("malformed branch observation")
		}
		b := BranchState{Ref: v[0], OID: v[1], Upstream: v[2], Remote: v[3], RemoteRef: v[4], PushRemote: v[5], PushRef: v[6]}
		if b.PushRef == "" && b.PushRemote != "" {
			configured, seen := pushSpecs[b.PushRemote]
			if !seen {
				spec, _ := run(ctx, dir, "config", "--get-all", "remote."+b.PushRemote+".push")
				configured = spec != ""
				pushSpecs[b.PushRemote] = configured
			}
			if !configured {
				switch pushDefault {
				case "simple":
					if b.PushRemote == b.Remote && b.Ref == b.RemoteRef {
						b.PushRef = b.Ref
					}
				case "upstream":
					if b.PushRemote == b.Remote {
						b.PushRef = b.RemoteRef
					}
				case "current", "matching":
					b.PushRef = b.Ref
				}
			}
		}
		if sec, e := strconv.ParseInt(v[7], 10, 64); e == nil {
			b.CommittedAt = time.Unix(sec, 0).UTC()
		}
		if b.Upstream != "" {
			b.UpstreamOID, err = run(ctx, dir, "rev-parse", "--verify", b.Upstream+"^{commit}")
			if err != nil {
				b.Error = "upstream ref unavailable"
			} else {
				counts, e := run(ctx, dir, "rev-list", "--left-right", "--count", b.OID+"..."+b.UpstreamOID, "--")
				if e != nil {
					b.Error = "branch comparison unavailable"
				} else if n, e := fmt.Sscanf(counts, "%d\t%d", &b.Ahead, &b.Behind); e == nil && n == 2 {
					b.ComparisonKnown = true
				} else {
					b.Error = "invalid branch comparison"
				}
			}
		}
		result = append(result, b)
	}
	return result, nil
}

type LocalPath struct {
	Path       string    `json:"path"`
	Bytes      int64     `json:"bytes"`
	Mode       string    `json:"mode"`
	Modified   time.Time `json:"modified"`
	Disposable bool      `json:"disposable"`
}

// CheckoutContents is metadata only. ContentDigest includes actual dirty bytes
// so a deferred finding reappears even when the porcelain path counts match.
type CheckoutContents struct {
	Status      Status      `json:"status"`
	Ignored     []LocalPath `json:"ignored"`
	Nested      []string    `json:"nested_repositories"`
	Fingerprint string      `json:"fingerprint"`
}

// ValidateDisposableDirs deliberately accepts exact relative directory roots,
// never patterns or Git administrative directories.
func ValidateDisposableDirs(dirs []string) error {
	for _, dir := range dirs {
		if dir == "" || dir == "." || filepath.IsAbs(dir) || filepath.ToSlash(filepath.Clean(dir)) != dir || strings.ContainsAny(dir, "\\\x00*?[]:\n\r") || dir == ".." || strings.HasPrefix(dir, "../") {
			return fmt.Errorf("invalid disposable directory %q", dir)
		}
		for _, part := range strings.Split(dir, "/") {
			if part == ".git" {
				return fmt.Errorf("Git administrative paths cannot be disposable")
			}
		}
	}
	return nil
}

func InspectTriageContents(ctx context.Context, dir string, disposable []string) (CheckoutContents, error) {
	var result CheckoutContents
	result.Ignored = []LocalPath{}
	result.Nested = []string{}
	if err := ValidateDisposableDirs(disposable); err != nil {
		return result, err
	}
	status, err := runEnv(ctx, dir, []string{"GIT_OPTIONAL_LOCKS=0"}, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none", "-z")
	if err != nil {
		return result, err
	}
	result.Status = statusFromOutput(dir, status)
	ignored, err := run(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	enc := json.NewEncoder(hash)
	_ = enc.Encode(status)
	_ = enc.Encode(disposable)
	// Porcelain carries HEAD/index object IDs. Stream dirty working files rather
	// than constructing potentially enormous binary diffs in memory.
	for _, rec := range nulLines(status) {
		path := statusPath(rec)
		if strings.HasPrefix(rec, "? ") {
			path = strings.TrimPrefix(rec, "? ")
		}
		if path == "" {
			continue
		}
		if err := digestLocalPath(ctx, dir, path, hash); err != nil && !os.IsNotExist(err) {
			return result, err
		}
	}
	for _, path := range nulLines(ignored) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		full := filepath.Join(dir, filepath.FromSlash(path))
		info, e := os.Lstat(full)
		if e != nil {
			return result, e
		}
		p := LocalPath{Path: path, Bytes: info.Size(), Mode: info.Mode().String(), Modified: info.ModTime().UTC()}
		for _, root := range disposable {
			if strings.HasPrefix(path, root+"/") {
				rootInfo, e := os.Lstat(filepath.Join(dir, filepath.FromSlash(root)))
				if e == nil && rootInfo.IsDir() && rootInfo.Mode()&os.ModeSymlink == 0 {
					p.Disposable = true
				}
			}
		}
		result.Ignored = append(result.Ignored, p)
	}
	// Walk only untracked/ignored directories, pruning tracked project trees.
	other, err := run(ctx, dir, "ls-files", "--others", "--directory", "-z")
	if err != nil {
		return result, err
	}
	for _, rel := range nulLines(other) {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
		info, e := os.Lstat(full)
		if e != nil {
			return result, e
		}
		if !info.IsDir() {
			continue
		}
		e = filepath.WalkDir(full, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if d.Name() == ".git" {
				r, _ := filepath.Rel(dir, filepath.Dir(path))
				result.Nested = append(result.Nested, filepath.ToSlash(r))
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if e != nil {
			return result, e
		}
	}
	sort.Strings(result.Nested)
	_ = enc.Encode(result.Ignored)
	_ = enc.Encode(result.Nested)
	result.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func digestLocalPath(ctx context.Context, root, path string, w interface{ Write([]byte) (int, error) }) error {
	full := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "%q:%s:%d\x00", path, info.Mode(), info.Size())
	if info.Mode()&os.ModeSymlink != 0 {
		target, e := os.Readlink(full)
		if e != nil {
			return e
		}
		_, _ = w.Write([]byte(target))
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, opened, err := safefile.OpenRegular(full)
	if err != nil {
		return err
	}
	defer f.Close()
	if !os.SameFile(info, opened) {
		return safefile.ErrChanged
	}
	buf := make([]byte, 64*1024)
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		n, e := f.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if e != nil {
			if errors.Is(e, io.EOF) {
				after, err := f.Stat()
				if err != nil {
					return err
				}
				if !safefile.SameFileState(opened, after) {
					return safefile.ErrChanged
				}
				return nil
			}
			return e
		}
	}
}

// RunUnattended retains configured credential helpers but never prompts on the
// terminal owned by a TUI. Errors must be redacted before public reporting.
func RunUnattended(ctx context.Context, dir string, args ...string) (string, error) {
	return runEnv(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0"}, args...)
}

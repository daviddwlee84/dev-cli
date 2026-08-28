// Package diskusage measures the logical bytes owned by local checkouts while
// keeping shared Git storage separate from what deleting one checkout reclaims.
package diskusage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/dustin/go-humanize"
)

// Target describes one checkout and the Git storage ownership facts needed to
// interpret its size. WorktreeCount excludes the main checkout.
type Target struct {
	Key           string `json:"key"`
	Checkout      string `json:"checkout"`
	GitDir        string `json:"git_dir,omitempty"`
	CommonDir     string `json:"common_dir,omitempty"`
	MainRoot      string `json:"main_root,omitempty"`
	Bare          bool   `json:"bare"`
	Linked        bool   `json:"linked"`
	WorktreeCount int    `json:"worktree_count"`
	Signature     string `json:"signature,omitempty"`
}

// FromGit builds a measurement target from one discovered checkout.
func FromGit(repository gitx.Repo, worktreeCount int) Target {
	target := Target{
		Checkout: repository.Root, GitDir: repository.GitDir,
		CommonDir: repository.GitCommonDir, MainRoot: repository.MainRoot,
		Bare: repository.Bare, Linked: repository.IsLinkedWorktree,
		WorktreeCount: worktreeCount,
	}
	if target.Bare {
		target.Checkout = repository.MainRoot
	}
	target.Signature = targetSignature(target.Checkout)
	target.Key = target.CacheKey()
	return target
}

// Plain builds a target for a non-Git experiment directory.
func Plain(path string) Target {
	target := Target{Checkout: path, Signature: targetSignature(path)}
	target.Key = target.CacheKey()
	return target
}

// CacheKey is stable for one ownership layout. A linked-worktree count change
// deliberately produces a different key because it changes private/shared Git
// attribution.
func (t Target) CacheKey() string {
	fields := []string{
		filepath.Clean(t.Checkout), filepath.Clean(t.GitDir), filepath.Clean(t.CommonDir),
		filepath.Clean(t.MainRoot), strconv.FormatBool(t.Bare), strconv.FormatBool(t.Linked),
		strconv.Itoa(t.WorktreeCount), t.Signature,
	}
	digest := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(digest[:])
}

func targetSignature(paths ...string) string {
	hash := sha256.New()
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = filepath.Clean(path)
		if path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		info, err := os.Lstat(path)
		if err != nil {
			fmt.Fprintf(hash, "%s\x00missing\x00", path)
			continue
		}
		fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\x00", path,
			info.ModTime().UnixNano(), info.Size(), info.Mode())
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// Usage is one complete or explicitly partial logical-byte measurement.
type Usage struct {
	CheckoutBytes   int64  `json:"checkout_bytes"`
	PrivateGitBytes *int64 `json:"private_git_bytes"`
	SharedGitBytes  *int64 `json:"shared_git_bytes"`
	OwnedBytes      int64  `json:"owned_bytes"`
	TotalBytes      *int64 `json:"total_bytes"`

	Complete          bool      `json:"complete"`
	UnreadableEntries int       `json:"unreadable_entries"`
	MeasuredAt        time.Time `json:"measured_at"`
	Cached            bool      `json:"cached"`
}

// HumanOwned is the table form. Partial measurements use a lower-bound marker.
func (u Usage) HumanOwned() string {
	prefix := ""
	if !u.Complete {
		prefix = "≥"
	}
	return prefix + humanize.IBytes(uint64(max(u.OwnedBytes, 0)))
}

// HumanShared renders optional shared storage without implying ownership.
func (u Usage) HumanShared() string {
	if u.SharedGitBytes == nil {
		return "—"
	}
	return humanize.IBytes(uint64(max(*u.SharedGitBytes, 0))) + " shared"
}

// Validate rejects internally inconsistent cached or scanner results.
func (u Usage) Validate() error {
	if u.CheckoutBytes < 0 || u.OwnedBytes < 0 ||
		(u.PrivateGitBytes != nil && *u.PrivateGitBytes < 0) ||
		(u.SharedGitBytes != nil && *u.SharedGitBytes < 0) ||
		(u.TotalBytes != nil && *u.TotalBytes < 0) {
		return fmt.Errorf("disk usage contains negative byte count")
	}
	private := int64(0)
	if u.PrivateGitBytes != nil {
		private = *u.PrivateGitBytes
	}
	if u.OwnedBytes != u.CheckoutBytes+private {
		return fmt.Errorf("owned bytes %d do not equal checkout %d + private Git %d",
			u.OwnedBytes, u.CheckoutBytes, private)
	}
	if u.SharedGitBytes != nil && u.TotalBytes != nil {
		return fmt.Errorf("total bytes are ambiguous when shared Git storage exists")
	}
	if u.TotalBytes != nil && *u.TotalBytes != u.OwnedBytes {
		return fmt.Errorf("unshared total %d does not equal owned bytes %d", *u.TotalBytes, u.OwnedBytes)
	}
	if u.UnreadableEntries < 0 || u.MeasuredAt.IsZero() {
		return fmt.Errorf("disk usage lacks valid measurement metadata")
	}
	return nil
}

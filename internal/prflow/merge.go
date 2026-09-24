package prflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

var ErrStaleMerge = errors.New("pull request changed since review; preview again")
var ErrUnknownMerge = errors.New("a previous merge outcome is unconfirmed; reconcile before retrying")

type MergePlan struct {
	ID     string `json:"id"`
	Detail Detail `json:"detail"`
	seal   string
}
type MergeResult struct {
	Status      string `json:"status"`
	MergeOID    string `json:"merge_oid,omitempty"`
	ReceiptPath string `json:"receipt_path,omitempty"`
}

func mergeAuthority(d Detail) string {
	payload := struct {
		Reference                                                                                 Reference
		Account, Repository, Head, Base, HeadBranch, BaseBranch, Title, Readiness, Review, Checks string
		Queue, CanMerge, Squash, Delete                                                           bool
	}{d.Reference, d.AccountID, d.RepositoryID, d.HeadOID, d.BaseOID, d.HeadBranch, d.BaseBranch, d.Title, d.Readiness, d.ReviewDecision, d.Checks, d.Queue, d.CanMerge, d.SquashAllowed, d.AutoDeleteBranch}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Service) PlanMerge(ctx context.Context, r Reference) (MergePlan, error) {
	d, err := s.Detail(ctx, r)
	if err != nil {
		return MergePlan{}, err
	}
	if d.Readiness != "ready" {
		return MergePlan{}, fmt.Errorf("squash merge unavailable: %s", d.Reason)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return MergePlan{}, err
	}
	return MergePlan{ID: hex.EncodeToString(id), Detail: d, seal: mergeAuthority(d)}, nil
}

func (s *Service) mergeReceiptDir(r Reference) (string, error) {
	if s.StateDir == "" || !filepath.IsAbs(s.StateDir) {
		return "", errors.New("merge needs an absolute private state directory")
	}
	// The application's shared state root need not itself be mode 0700. Only
	// this service's child contains private provider observations.
	info, err := os.Lstat(s.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		if err = privatefile.EnsureDir(s.StateDir); err != nil {
			return "", err
		}
		info, err = os.Lstat(s.StateDir)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe application state directory")
	}
	if err = checkMergeStateOwner(s.StateDir, info); err != nil {
		return "", err
	}
	root := filepath.Join(s.StateDir, "pr-merges")
	if err := privatefile.EnsureDir(root); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(r.URL()))
	dir := filepath.Join(root, hex.EncodeToString(sum[:]))
	if err := privatefile.EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func writeMergeReceipt(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	root, identity, err := safefile.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = privatefile.Check(dir, identity, true); err != nil {
		return err
	}
	_, err = safefile.CreateNoClobberPrepared(context.Background(), root, filepath.Base(path), append(b, '\n'), 0600, privatefile.ProtectCreatedFile)
	if err != nil {
		return err
	}
	return safefile.VerifyRoot(dir, identity)
}
func readMergeResult(path string) (MergeResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return MergeResult{}, err
	}
	if err = privatefile.Check(path, info, false); err != nil {
		return MergeResult{}, err
	}
	data, err := safefile.ReadStablePath(context.Background(), path, 1<<20)
	if err != nil {
		return MergeResult{}, err
	}
	current, err := os.Lstat(path)
	if err != nil || !safefile.SameFileState(info, current) {
		return MergeResult{}, errors.New("receipt identity changed")
	}
	var result MergeResult
	err = json.Unmarshal(data, &result)
	return result, err
}
func outstandingMerge(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".attempt.json") {
			continue
		}
		path := filepath.Join(dir, strings.TrimSuffix(entry.Name(), ".attempt.json")+".result.json")
		result, err := readMergeResult(path)
		if err != nil || result.Status == "unknown" || result.Status == "pending" {
			return ErrUnknownMerge
		}
		if result.Status == "merged" {
			return errors.New("this request has already been merged")
		}
		if result.Status != "rejected" {
			return ErrUnknownMerge
		}
	}
	return nil
}

func (s *Service) ApplyMerge(ctx context.Context, plan MergePlan) (MergeResult, error) {
	result := MergeResult{Status: "rejected"}
	if len(plan.ID) != 32 || plan.seal == "" || plan.seal != mergeAuthority(plan.Detail) {
		return result, ErrStaleMerge
	}
	if _, err := hex.DecodeString(plan.ID); err != nil {
		return result, ErrStaleMerge
	}
	dir, err := s.mergeReceiptDir(plan.Detail.Reference)
	if err != nil {
		return result, err
	}
	err = lockx.WithDir(ctx, dir, "pull request merge", func() error {
		if err := outstandingMerge(dir); err != nil {
			return err
		}
		fresh, err := s.Detail(ctx, plan.Detail.Reference)
		if err != nil {
			return err
		}
		if mergeAuthority(fresh) != plan.seal {
			return ErrStaleMerge
		}
		attemptPath := filepath.Join(dir, plan.ID+".attempt.json")
		result.ReceiptPath = attemptPath
		// Once this fsynced attempt exists, a crashed process can never make a
		// new plan silently retry an uncertain provider write.
		attempt := struct {
			Plan      MergePlan `json:"plan"`
			Status    string    `json:"status"`
			CreatedAt time.Time `json:"created_at"`
		}{plan, "unknown", time.Now().UTC()}
		if err = writeMergeReceipt(attemptPath, attempt); err != nil {
			return err
		}
		result.Status = "unknown"
		outcome, mergeErr := s.Provider.Merge(ctx, fresh)
		if outcome.Status == "merged" && outcome.MergeOID != "" {
			result.Status = "merged"
			result.MergeOID = outcome.MergeOID
		} else if outcome.Status == "rejected" {
			result.Status = "rejected"
		}
		// Reconciliation is read-only and cannot repeat the write, even when the
		// transport returned a timeout after the provider accepted it.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		observed, readErr := s.Detail(readCtx, plan.Detail.Reference)
		if readErr == nil && observed.State == forge.PRStateMerged && observed.HeadOID == fresh.HeadOID && observed.RepositoryID == fresh.RepositoryID && observed.MergeOID != "" {
			result.Status = "merged"
			result.MergeOID = observed.MergeOID
			mergeErr = nil
		}
		resultPath := filepath.Join(dir, plan.ID+".result.json")
		if err = writeMergeReceipt(resultPath, result); err != nil {
			return errors.Join(mergeErr, err)
		}
		if result.Status == "unknown" {
			return errors.Join(ErrUnknownMerge, mergeErr)
		}
		return mergeErr
	})
	return result, err
}

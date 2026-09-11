// Package configedit applies source-bound local configuration edits with private
// recovery receipts. It does not interpret configuration or contact providers.
package configedit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const maxBytes = 2 << 20
const maxReceiptBytes = 512 << 20
const maxTextBytes = 128 << 20
const maxTransactionBytes = 8 << 20

var ErrStale = errors.New("configuration changed since planning")

type Change struct {
	Path         string `json:"path"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
	Action       string `json:"action"`
	before       []byte
	after        []byte
	info         fs.FileInfo
	mode         fs.FileMode
	metadata     Metadata
	limit        int64
	anchor       string
	anchorInfo   fs.FileInfo
}

type Plan struct {
	Changes       []Change `json:"changes"`
	changes       []Change
	guards        []Change
	beforePublish func(int)
	locks         []string
	portable      bool
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Read resolves no symlinks and captures an unchanged ordinary user-owned file.
func Read(ctx context.Context, path string) ([]byte, error) {
	c, err := observe(ctx, path)
	if c.info == nil {
		return nil, err
	}
	return append([]byte{}, c.before...), err
}

func observe(ctx context.Context, path string) (Change, error) {
	return observeLimit(ctx, path, maxBytes)
}
func observeLimit(ctx context.Context, path string, limit int64) (Change, error) {
	c := Change{Path: path, mode: 0o600, limit: limit}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return c, errors.New("configuration path must be absolute and clean")
	}
	if err := safeParents(filepath.Dir(path), false); err != nil {
		return c, err
	}
	c.anchor = filepath.Dir(path)
	for {
		info, e := os.Lstat(c.anchor)
		if e == nil {
			c.anchorInfo = info
			break
		}
		if !errors.Is(e, fs.ErrNotExist) {
			return c, e
		}
		next := filepath.Dir(c.anchor)
		if next == c.anchor {
			return c, e
		}
		c.anchor = next
	}
	root, held, err := safefile.OpenRoot(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer root.Close()
	b, info, err := safefile.ReadStableRegular(ctx, root, filepath.Base(path), nil, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.metadata, err = captureMetadata(path, info)
	if err != nil {
		return c, err
	}
	if err = safefile.VerifyRoot(filepath.Dir(path), held); err != nil {
		return c, err
	}
	c.before = b
	c.info = info
	c.mode = info.Mode().Perm()
	c.BeforeDigest = Digest(b)
	return c, nil
}

// New captures all authority before prompting. A nil desired value means remove;
// a non-nil empty slice means an empty file. Order is publication order.
func New(ctx context.Context, paths []string, desired [][]byte, guards []string) (Plan, error) {
	return newWithLimits(ctx, paths, desired, guards, maxBytes, maxTransactionBytes, false)
}

// NewTextFile opts into a single large portable text transaction. Legacy callers
// retain their existing limits and platform policy.
func NewTextFile(ctx context.Context, path string, desired []byte) (Plan, error) {
	return newWithLimits(ctx, []string{path}, [][]byte{desired}, nil, maxTextBytes, 2*maxTextBytes, true)
}

func newWithLimits(ctx context.Context, paths []string, desired [][]byte, guards []string, fileLimit, totalLimit int, portable bool) (Plan, error) {
	p := Plan{Changes: []Change{}, portable: portable}
	if len(paths) != len(desired) {
		return p, errors.New("mismatched edit paths")
	}
	seen := map[string]bool{}
	totalBytes := 0
	var deviceAnchor fs.FileInfo
	var devicePath string
	if len(paths) > 256 {
		return p, errors.New("select at most 256 files per transaction")
	}
	for i, path := range paths {
		if seen[path] {
			return p, errors.New("duplicate edit path")
		}
		seen[path] = true
		c, err := observeLimit(ctx, path, int64(fileLimit))
		if err != nil {
			return p, err
		}
		if deviceAnchor == nil {
			deviceAnchor = c.anchorInfo
			devicePath = c.anchor
		} else if !sameDevice(devicePath, c.anchor, deviceAnchor, c.anchorInfo) {
			return p, errors.New("configuration transitions must stay on one filesystem")
		}
		if c.info != nil && !sameDevice(c.Path, c.anchor, c.info, c.anchorInfo) {
			return p, errors.New("mounted configuration file cannot be replaced")
		}
		totalBytes += len(c.before) + len(desired[i])
		if totalBytes > totalLimit {
			return p, errors.New("configuration transaction exceeds byte limit; select fewer files")
		}
		if len(desired[i]) > fileLimit {
			return p, errors.New("configuration exceeds byte limit")
		}
		if desired[i] == nil {
			if c.info == nil {
				continue
			}
			c.Action = "remove"
		} else {
			c.after = append([]byte{}, desired[i]...)
			c.AfterDigest = Digest(c.after)
			if c.info != nil && bytes.Equal(c.before, c.after) {
				continue
			}
			c.Action = "create"
			if c.info != nil {
				c.Action = "update"
			}
		}
		p.changes = append(p.changes, c)
		p.Changes = append(p.Changes, c)
	}
	for _, path := range guards {
		c, err := observe(ctx, path)
		if err != nil {
			return p, err
		}
		p.guards = append(p.guards, c)
	}
	return p, nil
}

// Preview intentionally exposes no file contents. Callers may render their own
// syntax-aware redacted diff; receipts retain original bytes outside Git.
func (p Plan) Preview() []Change { return append([]Change(nil), p.changes...) }
func (p Plan) Empty() bool       { return len(p.changes) == 0 }
func (p Plan) Check(ctx context.Context) error {
	for _, c := range append(append([]Change(nil), p.guards...), p.changes...) {
		if _, err := current(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
func current(ctx context.Context, c Change) (Change, error) {
	if c.anchorInfo != nil {
		if err := safefile.VerifyRoot(c.anchor, c.anchorInfo); err != nil {
			return Change{}, ErrStale
		}
	}
	n, err := observeLimit(ctx, c.Path, c.limit)
	if err != nil {
		return n, err
	}
	if (c.info == nil) != (n.info == nil) || c.BeforeDigest != n.BeforeDigest || c.info != nil && (!safefile.SameFileState(c.info, n.info) || !reflect.DeepEqual(c.metadata, n.metadata)) {
		return n, ErrStale
	}
	return n, nil
}

type image struct {
	Path         string     `json:"path"`
	Before       []byte     `json:"before,omitempty"`
	BeforeExists bool       `json:"before_exists"`
	After        []byte     `json:"after,omitempty"`
	AfterExists  bool       `json:"after_exists"`
	Mode         uint32     `json:"mode"`
	Metadata     Metadata   `json:"metadata"`
	AfterKnown   bool       `json:"after_known"`
	AfterState   *fileState `json:"after_state,omitempty"`
}
type receipt struct {
	Version  int       `json:"version"`
	Created  time.Time `json:"created"`
	Status   string    `json:"status"`
	Images   []image   `json:"images"`
	Locks    []string  `json:"locks,omitempty"`
	Portable bool      `json:"portable,omitempty"`
}
type Result struct {
	Status  string   `json:"status"`
	Receipt string   `json:"receipt,omitempty"`
	Applied []string `json:"applied,omitempty"`
}

func Apply(ctx context.Context, p Plan, recovery string) (Result, error) {
	return ApplyChecked(ctx, p, recovery, nil)
}

// WithLocks records cooperative owner namespaces, also retained by recovery.
func (p Plan) WithLocks(dirs ...string) Plan {
	p.locks = append([]string(nil), dirs...)
	sort.Strings(p.locks)
	return p
}

// ApplyChecked runs a domain validation under the same leases as publication.
func ApplyChecked(ctx context.Context, p Plan, recovery string, check func(context.Context) error) (Result, error) {
	result := Result{Status: "not_run"}
	if err := p.Check(ctx); err != nil {
		return result, err
	}
	if p.Empty() {
		result.Status = "noop"
		return result, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && !(p.portable && runtime.GOOS == "windows") {
		return result, errors.New("configuration writes require a verified macOS/Linux security backend")
	}
	if err := p.Check(ctx); err != nil {
		return result, err
	}
	var leases []*lockx.Lease
	defer func() {
		for i := len(leases) - 1; i >= 0; i-- {
			_ = leases[i].Close()
		}
	}()
	last := ""
	for _, dir := range p.locks {
		if dir == last {
			continue
		}
		last = dir
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return result, errors.New("invalid configuration lease path")
		}
		if e := safeParents(dir, true); e != nil {
			return result, e
		}
		lease, e := lockx.AcquireDir(ctx, dir, "configuration owner")
		if e != nil {
			return result, e
		}
		leases = append(leases, lease)
	}
	if err := prepareRecovery(recovery); err != nil {
		return result, err
	}
	err := lockx.WithDir(ctx, recovery, "local configuration", func() error {
		if err := p.Check(ctx); err != nil {
			return err
		}
		if check != nil {
			if err := check(ctx); err != nil {
				return err
			}
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		result.Receipt = hex.EncodeToString(id[:])
		result.Status = "partial"
		record := receipt{Version: 1, Created: time.Now().UTC(), Status: "pending", Locks: p.locks, Portable: p.portable}
		for _, c := range p.changes {
			record.Images = append(record.Images, image{Path: c.Path, Before: c.before, BeforeExists: c.info != nil, After: c.after, AfterExists: c.Action != "remove", Mode: uint32(c.mode), Metadata: c.metadata})
		}
		recordPath := filepath.Join(recovery, result.Receipt+".json")
		if err := writeReceipt(ctx, recordPath, record); err != nil {
			return err
		}
		defer func() {
			if result.Status != "applied" {
				record.Status = "partial"
				saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = writeReceipt(saveCtx, recordPath, record)
			}
		}()
		published := map[string]bool{}
		for index, c := range p.changes {
			if p.beforePublish != nil {
				p.beforePublish(index)
			}
			for _, guard := range p.guards {
				if !published[guard.Path] {
					if _, e := current(ctx, guard); e != nil {
						return e
					}
				}
			}
			if err := publish(ctx, c); err != nil {
				return fmt.Errorf("configuration apply interrupted; inspect or restore receipt %s: %w", result.Receipt, err)
			}
			observed, e := observeLimit(ctx, c.Path, c.limit)
			if e != nil {
				return e
			}
			if c.Action == "remove" {
				if observed.info != nil {
					return ErrStale
				}
			} else if observed.info == nil || observed.BeforeDigest != c.AfterDigest {
				return ErrStale
			}
			record.Images[index].AfterKnown = true
			record.Images[index].AfterState = stateOf(observed)
			published[c.Path] = true
			result.Applied = append(result.Applied, c.Path)
		}
		record.Status = "applied"
		if err := writeReceipt(ctx, recordPath, record); err != nil {
			return err
		}
		result.Status = "applied"
		return nil
	})
	return result, err
}

func publish(ctx context.Context, c Change) error {
	if err := safeParents(filepath.Dir(c.Path), true); err != nil {
		return err
	}
	n, err := current(ctx, c)
	if err != nil {
		return err
	}
	root, held, err := safefile.OpenRoot(filepath.Dir(c.Path))
	if err != nil {
		return err
	}
	defer root.Close()
	if err = safefile.VerifyRoot(filepath.Dir(c.Path), held); err != nil {
		return err
	}
	name := filepath.Base(c.Path)
	switch c.Action {
	case "create":
		_, err = safefile.CreateNoClobberPrepared(ctx, root, name, c.after, c.mode, c.metadata.prepare)
	case "update":
		_, err = safefile.AtomicReplacePrepared(ctx, root, name, n.info, c.after, c.mode, c.metadata.prepare)
	case "remove":
		_, _, err = safefile.ReadStableRegular(ctx, root, name, n.info, c.limit)
		if err == nil {
			err = root.Remove(name)
		}
	default:
		return errors.New("invalid configuration action")
	}
	if err != nil {
		return err
	}
	return safefile.VerifyRoot(filepath.Dir(c.Path), held)
}

func writeReceipt(ctx context.Context, path string, r receipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > maxReceiptBytes {
		return errors.New("recovery receipt exceeds byte limit")
	}
	c, err := observeLimit(ctx, path, maxReceiptBytes)
	if err != nil {
		return err
	}
	c.after = b
	c.Action = "create"
	if c.info != nil {
		c.Action = "update"
	}
	return publish(ctx, c)
}

// RestorePlan accepts only current bytes from either side of the transaction.
// Unpublished steps are skipped. Restore order reverses publication order.
func RestorePlan(ctx context.Context, recovery, id string) (Plan, error) {
	var p Plan
	if len(id) != 32 {
		return p, errors.New("invalid recovery receipt")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return p, errors.New("invalid recovery receipt")
	}
	if err := validateRecovery(recovery); err != nil {
		return p, err
	}
	record, err := observeLimit(ctx, filepath.Join(recovery, id+".json"), maxReceiptBytes)
	b := record.before
	if err != nil {
		return p, err
	}
	var r receipt
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return p, err
	}
	if r.Version != 1 || len(r.Images) > 1024 {
		return p, errors.New("unsupported recovery receipt")
	}
	var paths []string
	var contents [][]byte
	for i := len(r.Images) - 1; i >= 0; i-- {
		x := r.Images[i]
		limit := int64(maxBytes)
		if r.Portable {
			limit = maxTextBytes
		}
		n, err := observeLimit(ctx, x.Path, limit)
		if err != nil {
			return p, err
		}
		exists := n.info != nil
		if exists == x.BeforeExists && (!exists || bytes.Equal(n.before, x.Before)) {
			continue
		}
		if !x.AfterKnown || exists != x.AfterExists || exists && (!bytes.Equal(n.before, x.After) || !reflect.DeepEqual(stateOf(n), x.AfterState)) {
			return p, fmt.Errorf("%s changed after the operation: %w", x.Path, ErrStale)
		}
		paths = append(paths, x.Path)
		if x.BeforeExists {
			contents = append(contents, append([]byte{}, x.Before...))
		} else {
			contents = append(contents, nil)
		}
	}
	if r.Portable {
		if len(paths) > 1 {
			return p, errors.New("invalid portable receipt")
		}
		p, err = newWithLimits(ctx, paths, contents, nil, maxTextBytes, 2*maxTextBytes, true)
	} else {
		p, err = New(ctx, paths, contents, nil)
	}
	if err != nil {
		return p, err
	}
	for i := range p.changes {
		for _, x := range r.Images {
			if x.Path == p.changes[i].Path && x.BeforeExists {
				if !restorableMode(x.Mode) {
					return p, errors.New("unsafe recovery file mode")
				}
				p.changes[i].mode = fs.FileMode(x.Mode)
				p.changes[i].metadata = x.Metadata
				break
			}
		}
	}
	p = p.WithLocks(r.Locks...)
	return p, nil
}

func safeParents(path string, create bool) error {
	// Root anchors may have platform aliases (/var on macOS). Validate below the
	// resolved filesystem root, rejecting every user-controlled symlink component.
	volume := filepath.VolumeName(path)
	rootPath := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, rootPath), string(filepath.Separator))
	cur := rootPath
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return nil
			}
			if err = makeDirectory(cur); err != nil {
				return err
			}
			info, err = os.Lstat(cur)
		}
		if err != nil {
			return err
		}
		if runtime.GOOS == "darwin" && (cur == "/var" || cur == "/tmp") && info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(cur)
			if e == nil && (target == "private"+cur || target == "/private"+cur) {
				continue
			}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe directory %s", cur)
		}
		if err := checkAncestor(cur, info); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) && !create {
		return nil
	}
	if err != nil {
		return err
	}
	return checkDirectory(path, info)
}
func validateRecovery(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("recovery directory must be absolute")
	}
	if err := safeParents(path, false); err != nil {
		return err
	}
	for parent := path; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			return errors.New("recovery must be outside Git")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if info, err := os.Lstat(path); err == nil && !privateRecoveryMode(path, info) {
		return errors.New("recovery directory must have mode 0700")
	}
	return nil
}
func prepareRecovery(path string) error {
	if err := validateRecovery(path); err != nil {
		return err
	}
	return safeParents(path, true)
}

// RequireContents binds generated output to the exact input bytes it used,
// closing the read/render/capture race before handing a plan to a caller.
func (p Plan) RequireContents(expected map[string][]byte) error {
	for path, data := range expected {
		found := false
		for _, c := range append(append([]Change(nil), p.guards...), p.changes...) {
			if c.Path == path {
				found = true
				if !bytes.Equal(c.before, data) {
					return ErrStale
				}
			}
		}
		if !found {
			return errors.New("uncaptured source dependency")
		}
	}
	return nil
}

// Diff renders a review-only, redacted line comparison without temporary files.
// It is deliberately not an apply-able patch: authority remains in Plan.
func (p Plan) Diff(redact func(string) string) string {
	var out strings.Builder
	for _, c := range p.changes {
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", c.Path, c.Path)
		before := strings.Split(strings.TrimSuffix(string(c.before), "\n"), "\n")
		after := strings.Split(strings.TrimSuffix(string(c.after), "\n"), "\n")
		if c.info == nil {
			before = nil
		}
		if c.Action == "remove" {
			after = nil
		}
		count := len(before)
		if len(after) > count {
			count = len(after)
		}
		for i := 0; i < count; i++ {
			if i < len(before) && i < len(after) && before[i] == after[i] {
				continue
			}
			if i < len(before) {
				fmt.Fprintf(&out, "-%d %s\n", i+1, redact(before[i]))
			}
			if i < len(after) {
				fmt.Fprintf(&out, "+%d %s\n", i+1, redact(after[i]))
			}
		}
	}
	return out.String()
}

// A durable after-state lets undo reject later replacement, metadata changes or
// edits even when another writer happens to restore the same bytes.
type fileState struct {
	Identity string   `json:"identity"`
	Mode     uint32   `json:"mode"`
	Modified int64    `json:"modified"`
	Digest   string   `json:"digest"`
	Metadata Metadata `json:"metadata"`
}

func stateOf(c Change) *fileState {
	if c.info == nil {
		return nil
	}
	return &fileState{fileIdentity(c.Path, c.info), uint32(c.info.Mode()), c.info.ModTime().UnixNano(), c.BeforeDigest, c.metadata}
}

// SourceToken binds private saved plans without exposing metadata or content.
func (p Plan) SourceToken(path string) (string, error) {
	for _, c := range append(append([]Change(nil), p.guards...), p.changes...) {
		if c.Path == path {
			state := stateOf(c)
			anchor := fileIdentity(c.anchor, c.anchorInfo)
			if anchor == "unavailable" || state != nil && state.Identity == "unavailable" {
				return "", ErrStale
			}
			b, err := json.Marshal(struct {
				State  *fileState
				Anchor string
			}{state, anchor})
			if err != nil {
				return "", err
			}
			return Digest(b), nil
		}
	}
	return "", errors.New("uncaptured source")
}

// InspectToken captures source identity without changing files.
func InspectToken(ctx context.Context, path string) (string, error) {
	c, err := observeLimit(ctx, path, maxTextBytes)
	if err != nil {
		return "", err
	}
	return (Plan{changes: []Change{c}}).SourceToken(path)
}

// WritePrivate writes a bounded opaque state record using the same native
// metadata and identity guards, without treating it as a fleet fragment.
func WritePrivate(ctx context.Context, path string, data []byte, overwrite bool) error {
	if len(data) > maxReceiptBytes {
		return errors.New("private record exceeds byte limit")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if err = privatefile.Check(parent, info, true); err != nil {
		return err
	}
	c, err := observeLimit(ctx, path, maxReceiptBytes)
	if err != nil {
		return err
	}
	if c.info != nil {
		if !overwrite {
			return fs.ErrExist
		}
		if err = privatefile.Check(path, c.info, false); err != nil {
			return err
		}
	}
	c.mode = 0o600
	c.after = data
	c.AfterDigest = Digest(data)
	c.Action = "create"
	if c.info != nil {
		c.Action = "update"
	}
	if err = publish(ctx, c); err != nil {
		return err
	}
	current, err := observeLimit(ctx, path, maxReceiptBytes)
	if err != nil || current.BeforeDigest != c.AfterDigest {
		return ErrStale
	}
	return nil
}

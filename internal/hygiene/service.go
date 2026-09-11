package hygiene

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

const MaxFileBytes = 128 << 20
const MaxRecordBytes = 384 << 20

var ErrStale = errors.New("hygiene inputs changed; create a fresh preview")

type Service struct {
	Root         string
	Common       string
	RepoID       string
	Dir          string
	Global       string
	Policy       Policy
	Sources      []string
	policyInputs map[string]string
	key          []byte
	PublicOnly   bool
	Engine       Engine
}
type Options struct {
	Root, StateDir, GlobalPolicy string
	Override                     Policy
	Engine                       Engine
	PublicOnly                   bool
}

func Open(ctx context.Context, o Options) (*Service, error) {
	repo, err := gitx.Discover(ctx, o.Root)
	if err != nil {
		return nil, errors.New("hygiene requires a Git checkout")
	}
	id, err := gitx.DirectoryIdentity(repo.GitCommonDir)
	if err != nil {
		return nil, errors.New("cannot identify repository")
	}
	s := &Service{Root: repo.Root, Common: repo.GitCommonDir, RepoID: digest([]byte(id)), Global: o.GlobalPolicy, Policy: DefaultPolicy(), Engine: o.Engine, policyInputs: map[string]string{}, PublicOnly: o.PublicOnly}
	state, e := pathx.Canonical(o.StateDir)
	if e != nil {
		return nil, errors.New("private hygiene state path unavailable")
	}
	s.Dir = filepath.Join(state, "hygiene", "repos", s.RepoID)
	for parent := s.Dir; ; parent = filepath.Dir(parent) {
		if _, e := os.Lstat(filepath.Join(parent, ".git")); e == nil {
			return nil, errors.New("private hygiene state must be outside Git")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if s.Engine == nil {
		s.Engine = Gitleaks{}
	}
	for _, layer := range []struct {
		label, path string
		private     bool
	}{{"global", o.GlobalPolicy, false}, {"repository", filepath.Join(s.Root, ".dev-cli", "hygiene.toml"), false}, {"local", filepath.Join(s.Dir, "policy.toml"), true}} {
		if layer.path == "" || (o.PublicOnly && layer.label != "repository") {
			continue
		}
		b, err := safefile.ReadStablePath(ctx, layer.path, 1<<20)
		if errors.Is(err, fs.ErrNotExist) {
			s.policyInputs[layer.path] = "absent"
			continue
		}
		if err != nil {
			return nil, errors.New("cannot read hygiene policy safely")
		}
		if layer.private {
			info, e := os.Lstat(layer.path)
			if e != nil {
				return nil, e
			}
			if e = privatefile.Check(layer.path, info, false); e != nil {
				return nil, errors.New("local hygiene policy is not private")
			}
		}
		p, err := DecodePolicy(b)
		if err != nil {
			return nil, err
		}
		s.Policy = MergePolicy(s.Policy, p)
		s.Sources = append(s.Sources, layer.label)
		s.policyInputs[layer.path] = digest(b)
	}
	if o.Override.Secrets != "" || o.Override.Known != "" || o.Override.Generic != "" {
		s.Policy = MergePolicy(s.Policy, o.Override)
		s.Sources = append(s.Sources, "command")
	}
	for _, m := range []Mode{s.Policy.Secrets, s.Policy.Known, s.Policy.Generic} {
		if !validMode(m) {
			return nil, errors.New("invalid hygiene mode")
		}
	}
	return s, nil
}
func (s *Service) checkInputs(ctx context.Context) error {
	id, err := gitx.DirectoryIdentity(s.Common)
	if err != nil || digest([]byte(id)) != s.RepoID {
		return ErrStale
	}
	for p, want := range s.policyInputs {
		b, err := safefile.ReadStablePath(ctx, p, 1<<20)
		if errors.Is(err, fs.ErrNotExist) && want == "absent" {
			continue
		}
		if err != nil || digest(b) != want {
			return ErrStale
		}
	}
	return nil
}
func (s *Service) prepare(ctx context.Context) error {
	if err := privatefile.EnsureDir(s.Dir); err != nil {
		return err
	}
	return lockx.WithDir(ctx, s.Dir, "hygiene", func() error {
		p := filepath.Join(s.Dir, "key")
		b, err := safefile.ReadStablePath(ctx, p, 64)
		if errors.Is(err, fs.ErrNotExist) {
			b = make([]byte, 32)
			if _, err = rand.Read(b); err != nil {
				return err
			}
			if err = fleet.WritePrivateConfigFile(p, b, false); err != nil {
				return errors.New("cannot create private hygiene key")
			}
		} else if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil || len(b) != 32 {
			return errors.New("invalid private hygiene key")
		}
		if err = privatefile.Check(p, info, false); err != nil {
			return err
		}
		s.key = b
		return nil
	})
}
func newID() string { return uuid.NewString() }
func safeRecordID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil && strings.ToLower(id) == id
}
func (s *Service) save(ctx context.Context, id string, v any) error {
	if !safeRecordID(id) {
		return errors.New("invalid hygiene record ID")
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > MaxRecordBytes {
		return errors.New("hygiene record exceeds byte limit")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return fleet.WritePrivateConfigFile(filepath.Join(s.Dir, id+".json"), b, true)
}
func (s *Service) load(ctx context.Context, id string, v any) error {
	if !safeRecordID(id) {
		return errors.New("invalid hygiene record ID")
	}
	p := filepath.Join(s.Dir, id+".json")
	b, err := safefile.ReadStablePath(ctx, p, MaxRecordBytes)
	if err != nil {
		return errors.New("hygiene record unavailable")
	}
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if err = privatefile.Check(p, info, false); err != nil {
		return err
	}
	if err = json.Unmarshal(b, v); err != nil {
		return errors.New("invalid hygiene record")
	}
	return nil
}
func (s *Service) displayPath(p string) string {
	out := filepath.ToSlash(p)
	for _, r := range s.Policy.Rules {
		if r.Kind == "literal" && r.Value != "" {
			out = strings.ReplaceAll(out, r.Value, "[private]")
		}
	}
	for _, r := range s.Policy.Rules {
		if r.Kind == "cidr" {
			c, e := compileRule(r)
			if e == nil {
				ranges := c.indices(filepath.ToSlash(p), []byte(out))
				for i := len(ranges) - 1; i >= 0; i-- {
					m := ranges[i]
					out = out[:m[0]] + "[private]" + out[m[1]:]
				}
			}
		}
	}
	out = strings.ReplaceAll(out, "\n", "[newline]")
	out = strings.ReplaceAll(out, "\r", "[return]")
	out = strings.ReplaceAll(out, "\x1b", "[escape]")
	return feedback.Sanitize(out)
}

type Finding struct {
	ID          string `json:"id"`
	Rule        string `json:"rule"`
	Category    string `json:"category"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Commit      string `json:"commit,omitempty"`
	Occurrences int    `json:"occurrences"`
	Disposition string `json:"disposition"`
	CanRedact   bool   `json:"can_redact"`
}
type Gap struct {
	File string `json:"file,omitempty"`
	Code string `json:"code"`
}
type Report struct {
	SchemaVersion  int               `json:"schema_version"`
	Kind           string            `json:"kind"`
	ID             string            `json:"id"`
	RepoID         string            `json:"repo_id"`
	Scope          string            `json:"scope"`
	Status         string            `json:"status"`
	Created        time.Time         `json:"created"`
	PolicyDigest   string            `json:"policy_digest"`
	Files          int               `json:"files"`
	Bytes          int64             `json:"bytes"`
	Refs           map[string]string `json:"refs,omitempty"`
	Findings       []Finding         `json:"findings"`
	Gaps           []Gap             `json:"gaps,omitempty"`
	Skipped        []Gap             `json:"skipped,omitempty"`
	PrivateRules   int               `json:"private_rules"`
	PublicOnly     bool              `json:"public_only"`
	Audit          bool              `json:"audit"`
	Blocked        int               `json:"blocked"`
	Warnings       int               `json:"warnings"`
	PolicyDisabled bool              `json:"policy_disabled"`
}
type source struct{ Path, Digest, Token string }
type edit struct {
	Finding, Path string
	Start, End    int
	Replacement   string
}
type scanRecord struct {
	Report        Report
	Root          string
	Sources       []source
	Edits         []edit
	ScannerDigest string
}

func (r Report) OK() bool {
	return (r.Status == "complete" || r.Status == "skipped" && r.PolicyDisabled) && r.Blocked == 0
}
func (s *Service) token(ctx context.Context, path string) string {
	t, _ := configedit.InspectToken(ctx, filepath.Join(s.Root, filepath.FromSlash(path)))
	return t
}

func (s *Service) scannerInputs(ctx context.Context, audit bool) ([]byte, []byte, error) {
	cfg := DefaultGitleaks
	ignore := []byte{}
	p := filepath.Join(s.Root, ".gitleaks.toml")
	if b, e := safefile.ReadStablePath(ctx, p, 1<<20); e == nil {
		cfg = b
	} else if !errors.Is(e, fs.ErrNotExist) {
		return nil, nil, errors.New("scanner configuration unavailable")
	}
	if !audit {
		p = filepath.Join(s.Root, ".gitleaksignore")
		if b, e := safefile.ReadStablePath(ctx, p, 1<<20); e == nil {
			ignore = b
		} else if !errors.Is(e, fs.ErrNotExist) {
			return nil, nil, errors.New("scanner exceptions unavailable")
		}
	}
	return cfg, ignore, nil
}
func scannerDigest(cfg, ignore []byte) string { return digest([]byte(digest(cfg) + digest(ignore))) }

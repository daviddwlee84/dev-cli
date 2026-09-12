package agenthistory

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/google/uuid"
)

type Service struct {
	Root, Common, Dir, StateDir string
	Policy                      Policy
	Binding                     Binding
	Configured                  bool
	opts                        Options
	key                         []byte
}

func Open(ctx context.Context, o Options) (*Service, error) {
	if ctx == nil {
		return nil, errors.New("artifact service requires a context")
	}
	repo, err := gitx.Discover(ctx, o.Root)
	if err != nil {
		return nil, errors.New("artifact operation requires a Git checkout")
	}
	root, err := pathx.Canonical(repo.Root)
	if err != nil {
		return nil, err
	}
	common, err := pathx.Canonical(repo.GitCommonDir)
	if err != nil {
		return nil, err
	}
	id, err := gitx.DirectoryIdentity(common)
	if err != nil {
		return nil, errors.New("cannot identify source repository")
	}
	state, err := pathx.Canonical(o.StateDir)
	if err != nil {
		return nil, err
	}
	for parent := state; ; parent = filepath.Dir(parent) {
		if _, e := os.Lstat(filepath.Join(parent, ".git")); e == nil {
			return nil, errors.New("artifact private state must be outside Git")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	s := &Service{Root: root, Common: common, StateDir: state, Dir: filepath.Join(state, "agent-history", "repos", hash([]byte(id))), opts: o}
	b, err := safefile.ReadStablePath(ctx, s.policyPath(), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("artifact policy unavailable")
	}
	s.Policy, err = decodePolicy(b)
	if err != nil {
		return nil, err
	}
	s.Configured = true
	err = readPrivate(ctx, s.bindingPath(), &s.Binding)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("artifact local binding unavailable")
	}
	if s.Binding.Version != 1 || s.Binding.ProjectID != s.Policy.ProjectID {
		return nil, errors.New("artifact binding belongs to another project; run setup")
	}
	if s.Binding.Protection != "off" && s.Binding.Protection != "check" && s.Binding.Protection != "redact" {
		return nil, errors.New("invalid archive protection binding")
	}
	s.Binding.CaptureDir = ""
	if s.Policy.Capture == "external" {
		checkoutID, e := gitx.DirectoryIdentity(root)
		if e != nil {
			return nil, e
		}
		s.Binding.CaptureDir = filepath.Join(state, "agent-history", "capture", s.Policy.ProjectID, hash([]byte(checkoutID))[:16], "history")
	}
	return s, nil
}

func (s *Service) policyPath() string  { return filepath.Join(s.Root, ".dev-cli", "artifacts.toml") }
func (s *Service) bindingPath() string { return filepath.Join(s.Dir, "binding.json") }
func hash(b []byte) string             { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Service) opaque(values ...string) string {
	h := hmac.New(sha256.New, s.key)
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) prepare(ctx context.Context) error {
	if e := safeWriteParent(ctx, s.Dir); e != nil {
		return e
	}
	if err := privatefile.EnsureDir(s.Dir); err != nil {
		return err
	}
	return lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		path := filepath.Join(s.Dir, "key")
		b, err := safefile.ReadStablePath(ctx, path, 64)
		if errors.Is(err, os.ErrNotExist) {
			b = make([]byte, 32)
			if _, err = rand.Read(b); err != nil {
				return err
			}
			if err = configedit.WritePrivate(ctx, path, b, false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || len(b) != 32 {
			return errors.New("invalid artifact signing key")
		}
		if err = privatefile.Check(path, info, false); err != nil {
			return err
		}
		s.key = b
		return nil
	})
}
func readPrivate(ctx context.Context, path string, value any) error {
	b, err := safefile.ReadStablePath(ctx, path, maxRecordBytes)
	if err != nil {
		return err
	}
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err = privatefile.Check(path, i, false); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(value)
}
func (s *Service) planDir(id string) string { return filepath.Join(s.Dir, "plans", id) }
func (s *Service) save(ctx context.Context, p *planRecord) error {
	p.Signature = ""
	p.Signature = s.opaque(string(encode(p)))
	data := encode(p)
	if len(data) > maxRecordBytes {
		return errors.New("artifact record exceeds size limit")
	}
	return configedit.WritePrivate(ctx, filepath.Join(s.planDir(p.View.ID), "plan.json"), data, true)
}
func (s *Service) load(ctx context.Context, id string) (planRecord, error) {
	var p planRecord
	if !validID(id) {
		return p, errors.New("invalid artifact plan ID")
	}
	if err := s.readKey(ctx); err != nil {
		return p, err
	}
	if err := readPrivate(ctx, filepath.Join(s.planDir(id), "plan.json"), &p); err != nil {
		return p, errors.New("artifact plan unavailable")
	}
	sig := p.Signature
	p.Signature = ""
	if p.View.ID != id || !hmac.Equal([]byte(sig), []byte(s.opaque(string(encode(p))))) {
		return p, errors.New("artifact plan integrity check failed")
	}
	p.Signature = sig
	return p, nil
}

// Read-only lifecycle inspection must not take the archive mutation lock:
// taskflow may already hold the source Git lock, in the opposite acquisition
// order to archive apply. Plans only load an existing key; they never create it.
func (s *Service) readKey(ctx context.Context) error {
	path := filepath.Join(s.Dir, "key")
	b, e := safefile.ReadStablePath(ctx, path, 64)
	if e != nil || len(b) != 32 {
		return errors.New("artifact signing key unavailable")
	}
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if e = privatefile.Check(path, info, false); e != nil {
		return e
	}
	s.key = b
	return nil
}
func (s *Service) ReadPlan(ctx context.Context, id string) (Plan, error) {
	p, e := s.load(ctx, id)
	return p.View, e
}

// Git output is bounded and never included in errors. Hook/askpass execution
// remains native Git behavior only for an explicit mutation/network operation.
func runGit(ctx context.Context, root string, input []byte, limit int, args ...string) ([]byte, error) {
	var unset []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			unset = append(unset, key)
		}
	}
	argv := append([]string{"-c", "core.quotepath=false", "-c", "core.longpaths=true"}, args...)
	r, err := (sshhost.ExecRunner{}).Run(ctx, sshhost.RunRequest{Name: "git", Dir: root, Args: argv, Stdin: input, UnsetEnv: unset, Env: []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}, StdoutLimit: limit, Display: "artifact Git operation"})
	if err != nil || r.ExitCode != 0 || r.StdoutTruncated {
		return nil, errors.New("artifact Git operation failed or exceeded its output limit")
	}
	return r.Stdout, nil
}
func gitText(ctx context.Context, root string, args ...string) (string, error) {
	b, e := runGit(ctx, root, nil, 1<<20, args...)
	return strings.TrimSpace(string(b)), e
}

func checkoutToken(ctx context.Context, root string) (token, head, index string, err error) {
	id, e := gitx.DirectoryIdentity(root)
	if e != nil {
		return "", "", "", e
	}
	repo, e := gitx.Discover(ctx, root)
	if e != nil {
		return "", "", "", e
	}
	common, e := gitx.DirectoryIdentity(repo.GitCommonDir)
	if e != nil {
		return "", "", "", e
	}
	head, e = gitText(ctx, root, "rev-parse", "--verify", "HEAD")
	if e != nil {
		branch, be := gitText(ctx, root, "symbolic-ref", "-q", "HEAD")
		refs, re := gitText(ctx, root, "for-each-ref", "--format=%(refname)", branch)
		if be != nil || re != nil || refs != "" {
			return "", "", "", e
		}
		head = "unborn"
	}
	branch, _ := gitText(ctx, root, "symbolic-ref", "-q", "HEAD")
	indexBytes, e := runGit(ctx, root, nil, 8<<20, "ls-files", "--stage", "-z")
	if e != nil {
		return "", "", "", e
	}
	index = hash(indexBytes)
	return hash([]byte(id + "\x00" + common + "\x00" + head + "\x00" + branch + "\x00" + index)), head, index, nil
}
func (s *Service) newPlan(ctx context.Context, kind string) (planRecord, error) {
	var p planRecord
	if err := s.prepare(ctx); err != nil {
		return p, err
	}
	token, _, _, err := checkoutToken(ctx, s.Root)
	if err != nil {
		return p, err
	}
	p = planRecord{Root: s.Root, Common: s.Common, RootToken: token, Policy: s.Policy, Binding: s.Binding, View: Plan{SchemaVersion: 1, ID: uuid.NewString(), Kind: kind, Status: "prepared", Files: []FileSummary{}, ProjectID: s.Policy.ProjectID, Source: s.Policy.Source, Protection: s.Binding.Protection}}
	p.PolicyToken, err = configedit.InspectToken(ctx, s.policyPath())
	if err != nil {
		return p, err
	}
	p.CaptureToken, err = s.captureFingerprint(ctx)
	if err != nil {
		return p, err
	}
	p.BindingToken, err = configedit.InspectToken(ctx, s.bindingPath())
	if err != nil {
		return p, err
	}
	if err = safeWriteParent(ctx, s.planDir(p.View.ID)); err != nil {
		return p, err
	}
	if err = privatefile.EnsureDir(s.planDir(p.View.ID)); err != nil {
		return p, err
	}
	return p, nil
}

func safeWriteParent(ctx context.Context, dir string) error {
	_, e := configedit.InspectToken(ctx, filepath.Join(dir, ".dev-agent-history-write-guard"))
	if e != nil {
		return errors.New("artifact destination contains unsafe parents or filesystem metadata")
	}
	return nil
}
func (s *Service) current(ctx context.Context, p planRecord) error {
	if capture, e := s.captureFingerprint(ctx); e != nil || capture != p.CaptureToken {
		return ErrStale
	}
	if s.Root != p.Root || s.Common != p.Common {
		return ErrStale
	}
	token, _, _, err := checkoutToken(ctx, s.Root)
	if err != nil || token != p.RootToken {
		return ErrStale
	}
	for path, want := range map[string]string{s.policyPath(): p.PolicyToken, s.bindingPath(): p.BindingToken} {
		got, e := configedit.InspectToken(ctx, path)
		if e != nil || got != want {
			return ErrStale
		}
	}
	return nil
}
func (s *Service) protector(ctx context.Context) (*hygiene.Service, error) {
	return hygiene.Open(ctx, hygiene.Options{Root: s.Root, StateDir: s.StateDir, GlobalPolicy: s.opts.GlobalHygiene, Engine: s.opts.Engine})
}

func archiveCheckout(ctx context.Context, source, destination string) (string, error) {
	if !filepath.IsAbs(destination) {
		return "", errors.New("archive must be an absolute Git checkout path")
	}
	path, e := pathx.Canonical(destination)
	if e != nil {
		return "", e
	}
	if path != filepath.Clean(destination) {
		return "", errors.New("archive path must use its canonical location")
	}
	if inside, _ := pathx.Contains(source, path); inside {
		return "", errors.New("archive must be outside the source checkout")
	}
	if inside, _ := pathx.Contains(path, source); inside {
		return "", errors.New("source checkout must not be inside its archive")
	}
	repo, e := gitx.Discover(ctx, path)
	if e != nil || repo.Root != path {
		return "", errors.New("archive must be the root of an existing Git checkout; create it with git init first")
	}
	sourceRepo, e := gitx.Discover(ctx, source)
	if e != nil || sourceRepo.GitCommonDir == repo.GitCommonDir {
		return "", errors.New("archive must be a separate repository")
	}
	if _, e = gitx.DirectoryIdentity(path); e != nil {
		return "", e
	}
	if op, active, e := gitx.InProgress(path); e != nil || active {
		_ = op
		return "", errors.New("archive has an active or unknown Git operation")
	}
	return path, nil
}

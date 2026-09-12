package agenthistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

func (s *Service) PreviewSetup(ctx context.Context, o SetupOptions) (Plan, error) {
	p, err := s.newPlan(ctx, "artifact_setup")
	if err != nil {
		return p.View, err
	}
	policy := s.Policy
	if !s.Configured {
		policy = Policy{Version: 1, ProjectID: uuid.NewString(), Capture: "project"}
	}
	if o.Mode != "" {
		policy.Mode = o.Mode
	}
	if policy.Mode == "" {
		return p.View, errors.New("choose --mode track, archive or unmanaged")
	}
	if o.Source != "" {
		policy.Source = o.Source
	}
	if policy.Source == "" {
		if policy.Mode == "unmanaged" {
			policy.Source = "files"
		} else {
			if i, e := os.Lstat(filepath.Join(s.Root, ".specstory", "history")); e == nil && i.IsDir() {
				policy.Source = "specstory"
			} else {
				return p.View, errors.New("choose --source specstory or files")
			}
		}
	}
	if o.Capture != "" {
		policy.Capture = o.Capture
	}
	if len(o.Paths) > 0 {
		policy.Paths = o.Paths
	}
	if len(policy.Paths) == 0 && policy.Source == "specstory" {
		policy.Paths = []string{".specstory/history"}
	}
	policy.ExportIgnore = o.ExportIgnore
	if err = policy.validate(); err != nil {
		return p.View, err
	}
	binding := s.Binding
	binding.Version = 1
	binding.ProjectID = policy.ProjectID
	if o.Protection != "" {
		binding.Protection = o.Protection
	}
	if binding.Protection == "" {
		binding.Protection = "check"
	}
	if binding.Protection != "off" && binding.Protection != "check" && binding.Protection != "redact" {
		return p.View, errors.New("protection must be off, check or redact")
	}
	if o.Archive != "" {
		binding.Archive = o.Archive
	}
	if policy.Mode == "archive" {
		binding.Archive, err = archiveCheckout(ctx, s.Root, binding.Archive)
		if err != nil {
			return p.View, err
		}
		p.ArchiveToken, p.ArchiveHead, p.ArchiveIndex, err = checkoutToken(ctx, binding.Archive)
		if err != nil {
			return p.View, err
		}
	}
	if policy.Capture == "external" {
		id, e := gitx.DirectoryIdentity(s.Root)
		if e != nil {
			return p.View, e
		}
		binding.CaptureDir = filepath.Join(s.StateDir, "agent-history", "capture", policy.ProjectID, hash([]byte(id))[:16], "history")
		path := filepath.Join(s.Root, ".specstory", "cli", "config.toml")
		tracked, _ := gitText(ctx, s.Root, "ls-files", "--", ".specstory/cli/config.toml")
		if tracked != "" {
			return p.View, errors.New("external capture needs local SpecStory config; review and untrack the existing shared config first")
		}
		data, e := readOptional(ctx, path)
		if e != nil {
			return p.View, e
		}
		desired, e := specstoryOutput(data, binding.CaptureDir)
		if e != nil {
			return p.View, e
		}
		if err = s.change(ctx, &p, path, desired); err != nil {
			return p.View, err
		}
		p.View.Notices = append(p.View.Notices, "SpecStory output_dir is local to this checkout; run setup in each new checkout. No agent is started.")
	} else {
		binding.CaptureDir = ""
	}
	var b strings.Builder
	if err = toml.NewEncoder(&b).Encode(policy); err != nil {
		return p.View, err
	}
	if err = s.change(ctx, &p, s.policyPath(), []byte(b.String())); err != nil {
		return p.View, err
	}
	persistedBinding := binding
	persistedBinding.CaptureDir = ""
	if err = s.change(ctx, &p, s.bindingPath(), encode(persistedBinding)); err != nil {
		return p.View, err
	}
	ignore := []string{}
	if policy.Mode == "archive" {
		for _, path := range policy.Paths {
			ignore = append(ignore, "/"+escapePattern(path))
		}
	}
	if policy.Capture == "external" {
		ignore = append(ignore, "/.specstory/cli/config.toml")
	}
	if err = s.managedBlock(ctx, &p, ".gitignore", ignore); err != nil {
		return p.View, err
	}
	attributes := []string{}
	if policy.ExportIgnore {
		paths := policy.Paths
		if policy.Source == "specstory" {
			paths = []string{".specstory"}
		}
		for _, path := range paths {
			attributes = append(attributes, strconv.Quote("/"+escapePattern(path))+" export-ignore", strconv.Quote("/"+escapePattern(path)+"/**")+" export-ignore")
		}
	}
	if err = s.managedBlock(ctx, &p, ".gitattributes", attributes); err != nil {
		return p.View, err
	}
	p.Policy = policy
	p.Binding = binding
	p.View.ProjectID = policy.ProjectID
	p.View.Source = policy.Source
	p.View.Protection = binding.Protection
	p.View.Archive = binding.Archive
	p.View.Notices = append(p.View.Notices, "Setup does not untrack existing files, commit, push, install tools, or change source hygiene policy.")
	if binding.Protection == "off" {
		p.View.Notices = append(p.View.Notices, "Archive snapshots will be stored without scanning; source commit hooks remain unchanged.")
	}
	if err = s.save(ctx, &p); err != nil {
		return p.View, err
	}
	return p.View, nil
}

func (s *Service) change(ctx context.Context, p *planRecord, path string, desired []byte) error {
	editor, err := configedit.NewTextFile(ctx, path, desired)
	if err != nil {
		return errors.New("configuration cannot be safely edited; inspect its ownership, links and permissions")
	}
	if editor.Empty() {
		return nil
	}
	token, err := editor.SourceToken(path)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf("config-%03d", len(p.Changes))
	if err = configedit.WritePrivate(ctx, filepath.Join(s.planDir(p.View.ID), payload), desired, false); err != nil {
		return err
	}
	p.Changes = append(p.Changes, fileChange{Path: path, Token: token, Desired: payload, Digest: hash(desired)})
	label := path
	if rel, e := filepath.Rel(s.Root, path); e == nil && relative(filepath.ToSlash(rel)) {
		label = filepath.ToSlash(rel)
	} else if path == s.bindingPath() {
		label = "<private>/binding.json"
	}
	p.View.Files = append(p.View.Files, FileSummary{File: label, Bytes: int64(len(desired)), Digest: s.opaque(hash(desired))})
	return nil
}
func readOptional(ctx context.Context, path string) ([]byte, error) {
	b, e := safefile.ReadStablePath(ctx, path, 1<<20)
	if errors.Is(e, os.ErrNotExist) {
		return []byte{}, nil
	}
	return b, e
}
func escapePattern(s string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]", "*", "\\*", "?", "\\?", " ", "\\ ").Replace(s)
}
func (s *Service) managedBlock(ctx context.Context, p *planRecord, relativePath string, lines []string) error {
	path := filepath.Join(s.Root, relativePath)
	before, err := readOptional(ctx, path)
	if err != nil {
		return err
	}
	const start = "# BEGIN dev agent-history\n"
	const end = "# END dev agent-history\n"
	text := string(before)
	a := strings.Index(text, start)
	z := strings.Index(text, end)
	if (a < 0) != (z < 0) || a >= 0 && (z < a || strings.Count(text, start) != 1 || strings.Count(text, end) != 1) {
		return errors.New("malformed owned agent-history configuration block")
	}
	body := ""
	if len(lines) > 0 {
		body = start + strings.Join(lines, "\n") + "\n" + end
	}
	if a >= 0 {
		text = text[:a] + body + text[z+len(end):]
	} else if body != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += body
	}
	if string(before) == text {
		return nil
	}
	return s.change(ctx, p, path, []byte(text))
}

// Edit one TOML scalar while retaining all unrelated settings and comments.
func specstoryOutput(data []byte, output string) ([]byte, error) {
	if strings.Contains(string(data), "\"\"\"") || strings.Contains(string(data), "'''") {
		return nil, errors.New("review multiline SpecStory configuration manually before routing capture")
	}
	var parsed map[string]any
	if _, e := toml.Decode(string(data), &parsed); e != nil {
		return nil, errors.New("invalid SpecStory config; review it locally")
	}
	if local, ok := parsed["local_sync"].(map[string]any); ok {
		if enabled, ok := local["enabled"].(bool); ok && !enabled {
			return nil, errors.New("SpecStory local Markdown writing is disabled; enable it explicitly before external capture")
		}
	}
	lines := strings.Split(string(data), "\n")
	inSection, foundSection, foundKey := false, false, false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			inSection = strings.TrimSpace(strings.SplitN(trim, "#", 2)[0]) == "[local_sync]"
			foundSection = foundSection || inSection
		}
		if inSection && strings.HasPrefix(trim, "output_dir") {
			key, _, ok := strings.Cut(trim, "=")
			if !ok || strings.TrimSpace(key) != "output_dir" {
				return nil, errors.New("ambiguous SpecStory output_dir; review config locally")
			}
			lines[i] = "output_dir = " + strconv.Quote(output) + inlineTOMLComment(line)
			foundKey = true
		}
	}
	if !foundKey {
		if !foundSection {
			lines = append(lines, "[local_sync]", "output_dir = "+strconv.Quote(output))
		} else {
			for i, line := range lines {
				if strings.TrimSpace(strings.SplitN(line, "#", 2)[0]) == "[local_sync]" {
					lines = append(lines[:i+1], append([]string{"output_dir = " + strconv.Quote(output)}, lines[i+1:]...)...)
					break
				}
			}
		}
	}
	result := []byte(strings.Join(lines, "\n") + "\n")
	var verify struct {
		Local struct {
			Output string `toml:"output_dir"`
		} `toml:"local_sync"`
	}
	if _, err := toml.Decode(string(result), &verify); err != nil || verify.Local.Output != output {
		return nil, errors.New("cannot preserve SpecStory configuration safely")
	}
	return result, nil
}

func inlineTOMLComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == '#' {
			return " " + line[i:]
		}
	}
	return ""
}

func (s *Service) ApplySetup(ctx context.Context, id string) (Plan, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return p.View, err
	}
	if p.View.Kind != "artifact_setup" {
		return p.View, errors.New("plan is not artifact setup")
	}
	if p.View.Status == "complete" {
		return p.View, nil
	}
	if p.View.Status != "prepared" {
		return p.View, errors.New("inspect partial artifact setup before retrying")
	}
	err = lockx.WithDir(ctx, s.Dir, "agent history", func() error {
		return gitx.WithLifecycleLock(ctx, s.Common, func() error {
			if err := s.current(ctx, p); err != nil {
				return err
			}
			if p.Policy.Mode == "archive" {
				token, _, _, e := checkoutToken(ctx, p.Binding.Archive)
				if e != nil || token != p.ArchiveToken {
					return ErrStale
				}
			}
			var editors []configedit.Plan
			for _, c := range p.Changes {
				data, e := safefile.ReadStablePath(ctx, filepath.Join(s.planDir(id), c.Desired), 1<<20)
				if e != nil || hash(data) != c.Digest {
					return ErrStale
				}
				editor, e := configedit.NewTextFile(ctx, c.Path, data)
				if e != nil {
					return ErrStale
				}
				token, e := editor.SourceToken(c.Path)
				if e != nil || token != c.Token {
					return ErrStale
				}
				editors = append(editors, editor)
			}
			p.View.Status = "applying"
			if e := s.save(ctx, &p); e != nil {
				return e
			}
			if p.Binding.CaptureDir != "" {
				if e := safeWriteParent(ctx, p.Binding.CaptureDir); e != nil {
					return e
				}
				if e := privatefile.EnsureDir(p.Binding.CaptureDir); e != nil {
					return e
				}
			}
			for i, editor := range editors {
				for _, remaining := range editors[i:] {
					if e := remaining.Check(ctx); e != nil {
						return ErrStale
					}
				}
				r, e := configedit.Apply(ctx, editor, filepath.Join(s.Dir, "recovery"))
				if r.Receipt != "" {
					p.View.Recovery = append(p.View.Recovery, r.Receipt)
				}
				if e != nil {
					return errors.New("artifact setup interrupted; inspect private recovery")
				}
				p.View.Completed = append(p.View.Completed, p.View.Files[i].File)
				if e = s.save(ctx, &p); e != nil {
					return e
				}
			}
			p.View.Status = "complete"
			return s.save(ctx, &p)
		})
	})
	if err != nil && p.View.Status == "applying" {
		p.View.Status = "partial"
		_ = s.save(context.Background(), &p)
	}
	return p.View, err
}

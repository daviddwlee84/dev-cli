package hygiene

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type ScanOptions struct {
	Range   string
	Scope   string
	Timeout time.Duration
	Files   []string
	Audit   bool
}
type scanBuilder struct {
	s        *Service
	record   scanRecord
	findings map[string]int
	compiled []compiledRule
	gapSet   map[string]bool
}

func gitBytes(ctx context.Context, root string, input []byte, args ...string) ([]byte, error) {
	return runGitBytes(ctx, root, input, false, args...)
}
func snapshotGitBytes(ctx context.Context, root string, input []byte, args ...string) ([]byte, error) {
	return runGitBytes(ctx, root, input, true, args...)
}
func runGitBytes(ctx context.Context, root string, input []byte, isolated bool, args ...string) ([]byte, error) {
	base := []string{"-c", "core.fsmonitor=false", "-C", root}
	if len(args) == 0 || args[0] != "config" {
		base = append(base, "-c", "core.hooksPath="+os.DevNull)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	if isolated {
		cmd.Env = isolatedGitEnvironment()
	}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = &bytes.Buffer{}
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("Git observation failed")
	}
	return out, nil
}
func validRelative(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && filepath.ToSlash(filepath.Clean(p)) == p && !strings.HasPrefix(p, "../") && !strings.Contains(p, "\x00") && p != ".git" && !strings.HasPrefix(p, ".git/")
}
func (s *Service) Scan(ctx context.Context, o ScanOptions) (Report, error) {
	if o.Scope == "" {
		o.Scope = "worktree"
	}
	if o.Range != "" && o.Scope != "history" {
		return Report{}, errors.New("--range requires history scope")
	}
	if o.Scope != "worktree" && o.Scope != "staged" && o.Scope != "history" {
		return Report{}, errors.New("scope must be staged, worktree or history")
	}
	if o.Timeout == 0 {
		o.Timeout = 20 * time.Minute
	}
	if o.Timeout < 0 {
		return Report{}, errors.New("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if err := s.prepare(ctx); err != nil {
		return Report{}, err
	}
	b := &scanBuilder{s: s, findings: map[string]int{}, gapSet: map[string]bool{}}
	b.record = scanRecord{Root: s.Root, Report: Report{SchemaVersion: 1, Kind: "hygiene_scan", ID: newID(), RepoID: s.RepoID, Scope: o.Scope, Status: "complete", Created: time.Now().UTC(), PolicyDigest: keyedID(s.key, policyDigest(s.Policy)), Findings: []Finding{}, PrivateRules: len(s.Policy.Rules), PublicOnly: s.PublicOnly, Audit: o.Audit}}
	if s.Policy.Secrets == Off && s.Policy.Known == Off && s.Policy.Generic == Off {
		b.record.Report.Status = "skipped"
		b.record.Report.PolicyDisabled = true
		if err := s.save(ctx, b.record.Report.ID, b.record); err != nil {
			return b.record.Report, err
		}
		return b.record.Report, nil
	}
	for _, r := range s.Policy.Rules {
		c, e := compileRule(r)
		if e != nil {
			return b.record.Report, e
		}
		b.compiled = append(b.compiled, c)
	}
	privateDir := filepath.Join(s.Dir, "scan-"+newID())
	if err := privatefile.MakeDir(privateDir); err != nil {
		return b.record.Report, err
	}
	defer os.RemoveAll(privateDir)
	config, ignore, inputErr := s.scannerInputs(ctx, o.Audit)
	if inputErr != nil {
		b.gap("", "scanner_config_unreadable")
	}
	b.record.ScannerDigest = scannerDigest(config, ignore)
	var err error
	if inputErr != nil {
		err = inputErr
	} else if o.Scope == "history" {
		err = b.history(ctx, o, privateDir, config, ignore)
	} else {
		err = b.current(ctx, o, privateDir, config, ignore)
	}
	if err != nil {
		b.gap("", "observation_failed")
	}
	if ctx.Err() != nil {
		b.gap("", "canceled_or_timed_out")
	}
	if e := s.checkInputs(ctx); e != nil {
		b.gap("", "policy_changed")
	}
	if cfg, ig, e := s.scannerInputs(ctx, o.Audit); e != nil || scannerDigest(cfg, ig) != b.record.ScannerDigest {
		b.gap("", "scanner_config_changed")
	}

	r := &b.record.Report
	for _, f := range r.Findings {
		if f.Disposition == string(Block) {
			r.Blocked++
		}
		if f.Disposition == string(Warn) {
			r.Warnings++
		}
	}
	if len(r.Gaps) > 0 {
		r.Status = "partial"
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, c := r.Findings[i], r.Findings[j]
		if a.File != c.File {
			return a.File < c.File
		}
		if a.Line != c.Line {
			return a.Line < c.Line
		}
		return a.Rule < c.Rule
	})
	// A timeout still produces a private partial receipt and a failing exit.
	saveCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	if e := s.save(saveCtx, r.ID, b.record); e != nil {
		return *r, errors.New("cannot save hygiene scan receipt")
	}
	if err != nil {
		return *r, err
	}
	if r.Status != "complete" {
		return *r, errors.New("hygiene scan incomplete; inspect coverage gaps")
	}
	return *r, nil
}
func (b *scanBuilder) gap(file, code string) {
	key := file + "\x00" + code
	if !b.gapSet[key] {
		b.record.Report.Gaps = append(b.record.Report.Gaps, Gap{b.s.displayPath(file), code})
		b.gapSet[key] = true
	}
}
func (b *scanBuilder) add(rule, category, file, commit string, line int, value string, mode Mode, can bool) string {
	if mode == Off {
		return ""
	}
	id := keyedID(b.s.key, category, rule, file, commit, value)
	disposition := string(mode)
	for _, e := range b.s.Policy.Exceptions {
		if b.record.Report.Audit {
			break
		}
		if e.Finding == id {
			disposition = "accepted"
			break
		}
		if e.Rule == rule && pathMatches(e.Path, file) && e.Pattern != "" {
			re, _ := regexp.Compile(e.Pattern)
			if re != nil && re.MatchString(value) {
				disposition = "accepted"
				break
			}
		}
	}
	if i, ok := b.findings[id]; ok {
		b.record.Report.Findings[i].Occurrences++
		return id
	}
	if len(b.record.Report.Findings) >= 50000 {
		b.gap(file, "finding_limit")
		return ""
	}
	b.findings[id] = len(b.record.Report.Findings)
	b.record.Report.Findings = append(b.record.Report.Findings, Finding{id, b.s.displayPath(rule), category, b.s.displayPath(file), line, commit, 1, disposition, can && disposition != "accepted"})
	return id
}
func (b *scanBuilder) native(file, commit string, data []byte, can bool) {
	candidates := append([]compiledRule{}, b.compiled...)
	if b.s.Policy.Generic != Off {
		for _, r := range genericRules {
			c, _ := compileRule(r)
			candidates = append(candidates, c)
		}
	}
	for i, c := range candidates {
		category := "known"
		mode := b.s.Policy.Known
		if i >= len(b.compiled) {
			category = "generic"
			mode = b.s.Policy.Generic
		}
		if mode == Off {
			continue
		}
		if c.rule.Action != "" {
			mode = c.rule.Action
		}
		if mode == Off {
			continue
		}
		matches := c.indices(file, data)
		line, last := 1, 0
		for _, m := range matches {
			line += bytes.Count(data[last:m[0]], []byte{'\n'})
			last = m[0]
			id := b.add(c.rule.ID, category, file, commit, line, string(data[m[0]:m[1]]), mode, can)
			if id != "" && can {
				replacement := c.rule.Replacement
				if replacement == "" {
					replacement = "[REDACTED:" + c.rule.ID + "]"
				}
				b.record.Edits = append(b.record.Edits, edit{id, file, m[0], m[1], replacement})
			}
		}
	}
}
func (b *scanBuilder) current(ctx context.Context, o ScanOptions, privateDir string, config, ignore []byte) error {
	var paths []string
	var selectionBefore []byte
	if o.Scope == "staged" {
		raw, err := gitBytes(ctx, b.s.Root, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--name-only", "--diff-filter=ACMR", "-z")
		if err != nil {
			return err
		}
		paths = strings.Split(string(raw), "\x00")
		selectionBefore, err = gitBytes(ctx, b.s.Root, nil, "ls-files", "--stage", "-z")
		if err != nil {
			return err
		}
	} else {
		raw, err := gitBytes(ctx, b.s.Root, nil, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		if err != nil {
			return err
		}
		paths = strings.Split(string(raw), "\x00")
		selectionBefore = raw
	}
	if len(o.Files) > 0 {
		selected := map[string]bool{}
		for _, p := range paths {
			selected[p] = true
		}
		paths = nil
		for _, p := range o.Files {
			if !selected[p] {
				return errors.New("selected file is outside scan scope")
			}
			paths = append(paths, p)
		}
	}
	snapshot := filepath.Join(privateDir, "snapshot")
	if err := privatefile.MakeDir(snapshot); err != nil {
		return err
	}
	init := exec.CommandContext(ctx, "git", "-c", "init.templateDir=", "init", "--quiet", snapshot)
	init.Env = isolatedGitEnvironment()
	if init.Run() != nil {
		return errors.New("cannot prepare scanner snapshot")
	}
	seen := map[string]bool{}
	var index bytes.Buffer
	dataMap := map[string][]byte{}
	for _, file := range paths {
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		if !validRelative(file) {
			b.gap("", "unsafe_path")
			continue
		}
		var data []byte
		var err error
		token := ""
		if o.Scope == "staged" {
			mode, e := gitBytes(ctx, b.s.Root, nil, "ls-files", "--stage", "--", file)
			if e != nil {
				return e
			}
			if !bytes.HasPrefix(mode, []byte("100")) {
				b.skip(file, "nonregular_index_entry")
				continue
			}
			size, e := gitBytes(ctx, b.s.Root, nil, "cat-file", "-s", ":"+file)
			if e != nil {
				b.gap(file, "index_unreadable")
				continue
			}
			n, e := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
			if e != nil || n > MaxFileBytes {
				b.gap(file, "file_limit")
				continue
			}
			data, err = gitBytes(ctx, b.s.Root, nil, "show", ":"+file)
		} else {
			p := filepath.Join(b.s.Root, filepath.FromSlash(file))
			info, e := os.Lstat(p)
			if e == nil && (info.Mode()&os.ModeSymlink != 0 || info.IsDir()) {
				b.skip(file, "nonregular_worktree_entry")
				continue
			}
			data, err = safefile.ReadStablePath(ctx, p, MaxFileBytes)
			if err == nil {
				token = b.s.token(ctx, file)
			}
		}
		if err != nil {
			b.gap(file, "unreadable_or_changed")
			continue
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			b.binary(file)
			continue
		}
		b.record.Report.Files++
		b.record.Report.Bytes += int64(len(data))
		can := o.Scope == "worktree" && token != ""
		b.record.Sources = append(b.record.Sources, source{file, digest(data), token})
		b.native(file, "", data, can)
		if b.s.Policy.Secrets != Off {
			oid, e := snapshotGitBytes(ctx, snapshot, data, "hash-object", "-w", "--stdin")
			if e != nil {
				return e
			}
			fmt.Fprintf(&index, "100644 %s\t%s%c", strings.TrimSpace(string(oid)), file, 0)
			dataMap[file] = data
		}
	}
	if b.s.Policy.Secrets != Off && len(dataMap) > 0 {
		if _, err := snapshotGitBytes(ctx, snapshot, index.Bytes(), "update-index", "-z", "--index-info"); err != nil {
			return err
		}
		findings, err := b.s.Engine.Scan(ctx, EngineRequest{Root: snapshot, PrivateDir: privateDir, Config: config, Ignore: ignore, Audit: o.Audit})
		if err != nil {
			b.gap("", "secret_engine_failed")
			return err
		}
		tokens := map[string]bool{}
		for _, s := range b.record.Sources {
			tokens[s.Path] = s.Token != ""
		}
		for _, d := range findings {
			file := filepath.ToSlash(d.File)
			data, ok := dataMap[file]
			if !ok {
				return errors.New("scanner returned a file outside its snapshot")
			}
			spans := secretSpans(data, d)
			can := o.Scope == "worktree" && tokens[file] && len(spans) > 0
			id := b.add(d.RuleID, "secret", file, "", d.StartLine, d.Secret, b.s.Policy.Secrets, can)
			if id != "" && can {
				for _, m := range spans {
					b.record.Edits = append(b.record.Edits, edit{id, file, m[0], m[1], "[REDACTED:" + d.RuleID + "]"})
				}
			}
		}
	}
	if o.Scope == "staged" {
		now, err := gitBytes(ctx, b.s.Root, nil, "ls-files", "--stage", "-z")
		if err != nil || !bytes.Equal(now, selectionBefore) {
			b.gap("", "index_changed")
		}
	}
	for _, s := range b.record.Sources {
		if o.Scope == "worktree" {
			now, e := safefile.ReadStablePath(ctx, filepath.Join(b.s.Root, filepath.FromSlash(s.Path)), MaxFileBytes)
			if e != nil || digest(now) != s.Digest {
				b.gap(s.Path, "source_changed")
			}
		}
	}
	return nil
}
func secretSpans(data []byte, d Detection) [][2]int {
	endLine := d.EndLine
	if endLine < d.StartLine {
		endLine = d.StartLine
	}
	start, end, line := 0, len(data), 1
	for i, c := range data {
		if c == '\n' {
			line++
			if line == d.StartLine {
				start = i + 1
			}
			if line > endLine {
				end = i
				break
			}
		}
	}
	if d.StartLine > line || start > end {
		return nil
	}
	part := data[start:end]
	needle := []byte(d.Secret)
	var out [][2]int
	for offset := 0; offset < len(part); {
		i := bytes.Index(part[offset:], needle)
		if i < 0 {
			break
		}
		a := start + offset + i
		out = append(out, [2]int{a, a + len(needle)})
		offset += i + len(needle)
	}
	return out
}

func (b *scanBuilder) skip(file, code string) {
	key := "skip:" + file + ":" + code
	if !b.gapSet[key] {
		b.record.Report.Skipped = append(b.record.Report.Skipped, Gap{b.s.displayPath(file), code})
		b.gapSet[key] = true
	}
}
func (b *scanBuilder) binary(file string) {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".woff", ".woff2", ".ttf", ".zip", ".gz", ".exe", ".dll", ".so", ".db", ".sqlite":
		b.skip(file, "binary_content")
	default:
		b.gap(file, "unsupported_text_encoding")
	}
}

// A hook may export GIT_DIR/GIT_INDEX_FILE and Git config parameters. They
// belong to the caller's read scope, never the private snapshot's mutations.
func gitEnvironmentNames() []string {
	var names []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			names = append(names, key)
		}
	}
	return names
}
func isolatedGitEnvironment() []string {
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_COUNT=0", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
}

package hygiene

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// SnapshotResult checks caller-owned immutable bytes under the source repo's
// policy and logical path. Data is private, never part of a public report.
type SnapshotResult struct {
	Report       Report `json:"report"`
	InputDigest  string `json:"input_digest"`
	Cached       bool   `json:"cached"`
	Replacements int    `json:"replacements"`
	Data         []byte `json:"-"`
}

type snapshotCache struct {
	Key       string
	Record    scanRecord
	Signature string
}

// SnapshotInputs binds scanner implementation, executable bytes, configuration,
// exceptions and private policy. A file observation is never a writer proof.
func (s *Service) SnapshotInputs(ctx context.Context) (string, error) {
	if err := s.prepare(ctx); err != nil {
		return "", err
	}
	if err := s.checkInputs(ctx); err != nil {
		return "", err
	}
	cfg, ig, err := s.scannerInputs(ctx, false)
	if err != nil {
		return "", err
	}
	var extended struct {
		Extend struct {
			Path string `toml:"path"`
			URL  string `toml:"url"`
		} `toml:"extend"`
	}
	if _, err = toml.Decode(string(cfg), &extended); err != nil {
		return "", errors.New("invalid snapshot scanner configuration")
	}
	if extended.Extend.Path != "" || extended.Extend.URL != "" {
		return "", errors.New("snapshot scanning needs inline gitleaks rules or useDefault; external rule includes cannot be bound safely")
	}
	policy, err := policyBytes(s.Policy)
	if err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", errors.New("cannot identify snapshot implementation")
	}
	selfHash, err := executableDigest(ctx, self)
	if err != nil {
		return "", err
	}
	engineHash := "disabled"
	if s.Policy.Secrets != Off {
		switch engine := s.Engine.(type) {
		case Gitleaks:
			name := engine.Binary
			if name == "" {
				name = "gitleaks"
			}
			path, e := exec.LookPath(name)
			if e != nil {
				return "", errors.New("gitleaks is required by the selected protection policy")
			}
			engineHash, err = executableDigest(ctx, path)
		case interface{ SnapshotIdentity() string }:
			engineHash = engine.SnapshotIdentity()
		default:
			return "", errors.New("scanner cannot supply snapshot identity")
		}
		if err != nil {
			return "", err
		}
	}
	var environment []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "GITLEAKS_") {
			environment = append(environment, entry)
		}
	}
	sort.Strings(environment)
	return keyedID(s.key, "snapshot-v1", selfHash, engineHash, digest(policy), scannerDigest(cfg, ig), strings.Join(environment, "\x00")), nil
}

func executableDigest(ctx context.Context, path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("scanner executable unavailable")
	}
	data, err := safefile.ReadStablePath(ctx, real, 256<<20)
	if err != nil {
		return "", errors.New("cannot identify scanner executable")
	}
	return digest(data), nil
}

// InspectSnapshot never edits source files, the caller's index, or Git history.
// Redaction creates new bytes and then checks those bytes again. Cached entries
// are authenticated local scan evidence, not trusted data read from an archive.
func (s *Service) InspectSnapshot(ctx context.Context, file string, data []byte, redact bool) (SnapshotResult, error) {
	result := SnapshotResult{Data: data}
	if !validRelative(file) || len(data) > MaxFileBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return result, errors.New("snapshot requires a supported UTF-8 text file of at most 128 MiB")
	}
	inputs, err := s.SnapshotInputs(ctx)
	if err != nil {
		return result, err
	}
	result.InputDigest = inputs
	if s.Policy.Secrets == Off && s.Policy.Known == Off && s.Policy.Generic == Off {
		result.Report = Report{SchemaVersion: 1, Kind: "hygiene_snapshot", Scope: "snapshot", Status: "skipped", PolicyDisabled: true, Findings: []Finding{}}
		return result, nil
	}
	key := keyedID(s.key, inputs, file, digest(data))
	dir := filepath.Join(s.Dir, "snapshots")
	if err = privatefile.EnsureDir(dir); err != nil {
		return result, err
	}
	cachePath := filepath.Join(dir, key+".json")
	var cache snapshotCache
	if raw, e := safefile.ReadStablePath(ctx, cachePath, MaxRecordBytes); e == nil {
		info, e := os.Lstat(cachePath)
		if e == nil {
			e = privatefile.Check(cachePath, info, false)
		}
		if e != nil || json.Unmarshal(raw, &cache) != nil {
			return result, errors.New("invalid private snapshot cache")
		}
		sig := cache.Signature
		cache.Signature = ""
		encoded, _ := json.Marshal(cache)
		if cache.Key != key || !hmac.Equal([]byte(sig), []byte(keyedID(s.key, string(encoded)))) || cache.Record.Report.Status != "complete" {
			return result, errors.New("snapshot cache integrity check failed")
		}
		result.Cached = true
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, errors.New("snapshot cache unavailable")
	}
	if !result.Cached {
		record, e := s.scanSnapshot(ctx, file, data)
		if e != nil {
			record.Report.Status = "partial"
			record.Report.Gaps = append(record.Report.Gaps, Gap{Code: "snapshot_failed"})
			result.Report = record.Report
			return result, e
		}
		result.Report = record.Report
		if fresh, e := s.SnapshotInputs(ctx); e != nil || fresh != inputs {
			return result, ErrStale
		}
		cache = snapshotCache{Key: key, Record: record}
		encoded, _ := json.Marshal(cache)
		cache.Signature = keyedID(s.key, string(encoded))
		encoded, _ = json.Marshal(cache)
		if e = configedit.WritePrivate(ctx, cachePath, encoded, true); e != nil {
			return result, e
		}
	}
	result.Report = cache.Record.Report
	if err = s.save(ctx, cache.Record.Report.ID, cache.Record); err != nil {
		return result, err
	}
	if redact {
		result.Data, result.Replacements, err = snapshotReplacement(data, cache.Record)
		if err != nil {
			return result, err
		}
		if result.Replacements > 0 {
			checked, e := s.InspectSnapshot(ctx, file, result.Data, false)
			if e != nil {
				return result, e
			}
			result.Report = checked.Report
		}
	}
	if result.Report.Blocked > 0 {
		return result, errors.New("snapshot has blocking hygiene findings")
	}
	if fresh, e := s.SnapshotInputs(ctx); e != nil || fresh != inputs {
		return result, ErrStale
	}
	return result, nil
}

func (s *Service) scanSnapshot(ctx context.Context, file string, data []byte) (scanRecord, error) {
	b := &scanBuilder{s: s, findings: map[string]int{}, gapSet: map[string]bool{}}
	b.record = scanRecord{Root: s.Root, Report: Report{SchemaVersion: 1, Kind: "hygiene_snapshot", ID: newID(), RepoID: s.RepoID, Scope: "snapshot", Status: "complete", Created: time.Now().UTC(), Files: 1, Bytes: int64(len(data)), Findings: []Finding{}, PrivateRules: len(s.Policy.Rules), PublicOnly: s.PublicOnly, PolicyDigest: keyedID(s.key, policyDigest(s.Policy))}}
	for _, rule := range s.Policy.Rules {
		c, err := compileRule(rule)
		if err != nil {
			return b.record, err
		}
		b.compiled = append(b.compiled, c)
	}
	b.native(file, "", data, true)
	if s.Policy.Secrets != Off {
		base := filepath.Join(filepath.Dir(filepath.Dir(s.Dir)), "scans")
		if err := privatefile.EnsureDir(base); err != nil {
			return b.record, err
		}
		temporary := filepath.Join(base, newID())
		if err := privatefile.MakeDir(temporary); err != nil {
			return b.record, err
		}
		defer os.RemoveAll(temporary)
		root := filepath.Join(temporary, "snapshot")
		if err := privatefile.MakeDir(root); err != nil {
			return b.record, err
		}
		if _, err := snapshotGitBytes(ctx, root, nil, "-c", "init.templateDir=", "init", "--quiet"); err != nil {
			return b.record, err
		}
		oid, err := snapshotGitBytes(ctx, root, data, "hash-object", "-w", "--stdin")
		if err != nil {
			return b.record, err
		}
		entry := []byte(fmt.Sprintf("100644 %s\t%s%c", bytes.TrimSpace(oid), file, 0))
		if _, err = snapshotGitBytes(ctx, root, entry, "update-index", "-z", "--index-info"); err != nil {
			return b.record, err
		}
		cfg, ignore, err := s.scannerInputs(ctx, false)
		if err != nil {
			return b.record, err
		}
		rows, err := s.Engine.Scan(ctx, EngineRequest{Root: root, PrivateDir: temporary, Config: cfg, Ignore: ignore})
		if err != nil {
			b.record.Report.Status = "partial"
			return b.record, errors.New("snapshot scanner failed")
		}
		for _, d := range rows {
			if filepath.ToSlash(d.File) != file {
				return b.record, errors.New("scanner returned a path outside the snapshot")
			}
			spans := secretSpans(data, d)
			id := b.add(d.RuleID, "secret", file, "", d.StartLine, d.Secret, s.Policy.Secrets, len(spans) > 0)
			for _, m := range spans {
				b.record.Edits = append(b.record.Edits, edit{id, file, m[0], m[1], "[REDACTED:" + d.RuleID + "]"})
			}
		}
	}
	for _, f := range b.record.Report.Findings {
		if f.Disposition == string(Block) {
			b.record.Report.Blocked++
		}
		if f.Disposition == string(Warn) {
			b.record.Report.Warnings++
		}
	}
	if len(b.record.Report.Gaps) > 0 {
		b.record.Report.Status = "partial"
		return b.record, errors.New("snapshot scan has coverage gaps")
	}
	return b.record, nil
}

func snapshotReplacement(data []byte, record scanRecord) ([]byte, int, error) {
	findings := map[string]Finding{}
	for _, f := range record.Report.Findings {
		findings[f.ID] = f
	}
	var candidates []edit
	for _, e := range record.Edits {
		f, ok := findings[e.Finding]
		if ok && f.CanRedact && f.Disposition != "accepted" {
			candidates = append(candidates, e)
		}
	}
	priority := func(e edit) int {
		switch findings[e.Finding].Category {
		case "secret":
			return 0
		case "known":
			return 1
		}
		return 2
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if priority(candidates[i]) != priority(candidates[j]) {
			return priority(candidates[i]) < priority(candidates[j])
		}
		return candidates[i].End-candidates[i].Start > candidates[j].End-candidates[j].Start
	})
	var chosen []edit
	for _, e := range candidates {
		if e.Start < 0 || e.End > len(data) || e.Start >= e.End {
			return nil, 0, errors.New("invalid snapshot replacement")
		}
		overlap := false
		for _, prior := range chosen {
			if e.Start < prior.End && prior.Start < e.End {
				overlap = true
				break
			}
		}
		if !overlap {
			chosen = append(chosen, e)
		}
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Start < chosen[j].Start })
	if len(chosen) == 0 {
		return data, 0, nil
	}
	var output bytes.Buffer
	last := 0
	for _, e := range chosen {
		output.Write(data[last:e.Start])
		output.WriteString(e.Replacement)
		last = e.End
		if output.Len() > MaxFileBytes {
			return nil, 0, errors.New("redacted snapshot exceeds file limit")
		}
	}
	output.Write(data[last:])
	if output.Len() > MaxFileBytes {
		return nil, 0, errors.New("redacted snapshot exceeds file limit")
	}
	return output.Bytes(), len(chosen), nil
}

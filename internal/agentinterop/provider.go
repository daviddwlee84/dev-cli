package agentinterop

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/google/uuid"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const SkillsProviderVersion = "1.5.23"

type skillLockEntry struct {
	Source          string `json:"source"`
	SourceURL       string `json:"sourceUrl,omitempty"`
	SourceType      string `json:"sourceType"`
	Ref             string `json:"ref,omitempty"`
	SkillPath       string `json:"skillPath,omitempty"`
	ComputedHash    string `json:"computedHash,omitempty"`
	SkillFolderHash string `json:"skillFolderHash,omitempty"`
}

func lockLocation(ref ArtifactRef) (Location, error) {
	if ref.Scope == "project" {
		return Location{ref.Root, "skills-lock.json"}, nil
	}
	p := agentskill.GlobalLockPath()
	if p == "" {
		return Location{}, errors.New("global lock location unavailable")
	}
	bound, err := absoluteArtifact(ref, p)
	return location(bound), err
}

func readLock(data []byte, scope, name string) (skillLockEntry, json.RawMessage, error) {
	v, err := jsonDocument(data, false)
	if err != nil {
		return skillLockEntry{}, nil, err
	}
	root, err := jsonMap(&v)
	if err != nil {
		return skillLockEntry{}, nil, err
	}
	var version int
	want := 1
	if scope == "user" {
		want = 3
	}
	if json.Unmarshal(root["version"], &version) != nil || version != want {
		return skillLockEntry{}, nil, errors.New("unsupported skills lock schema; preserve the lock and update the compatibility profile")
	}
	entries, err := jsonMap(v.Find("/skills"))
	if err != nil {
		return skillLockEntry{}, nil, err
	}
	raw := entries[name]
	if len(raw) == 0 {
		return skillLockEntry{}, nil, errors.New("selected skill has no exact upstream lock entry")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return skillLockEntry{}, nil, errors.New("invalid skill lock entry")
	}
	allowed := map[string]bool{}
	for _, key := range []string{"source", "sourceUrl", "sourceType", "ref", "skillPath", "computedHash", "skillFolderHash", "installedAt", "updatedAt", "pluginName", "sourceBaseUrl", "wellKnownDigest", "subagents"} {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return skillLockEntry{}, nil, errors.New("unknown skills lock entry field; mutation requires a newer compatibility profile")
		}
	}
	var entry skillLockEntry
	if json.Unmarshal(raw, &entry) != nil || entry.Source == "" || entry.SourceType == "" {
		return entry, nil, errors.New("incomplete skill lock entry")
	}
	if len(fields["subagents"]) > 0 || len(fields["pluginName"]) > 0 {
		return entry, nil, errors.New("plugin/subagent skill ownership requires its native installer")
	}
	return entry, raw, nil
}

func sourceArgument(entry skillLockEntry) (string, error) {
	if entry.SourceType != "github" && entry.SourceType != "git" && entry.SourceType != "gitlab" {
		return "", errors.New("source is not reproducible through the supported Git provider; select copy explicitly")
	}
	source := entry.SourceURL
	if source == "" && entry.SourceType == "github" {
		parts := strings.Split(strings.Trim(entry.Source, "/"), "/")
		if len(parts) < 2 {
			return "", ErrUnsupported
		}
		source = "https://github.com/" + parts[0] + "/" + parts[1] + ".git"
	}
	u, err := url.Parse(source)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return "", errors.New("upstream source needs a credential-free HTTPS or SSH URL")
	}
	if !safeText(source) || !safeText(entry.Ref) || strings.HasPrefix(entry.Ref, "-") || strings.ContainsAny(entry.Ref, "#@") {
		return "", errors.New("invalid upstream source/ref")
	}
	if len(entry.Ref) == 40 || len(entry.Ref) == 64 {
		if _, err := hex.DecodeString(entry.Ref); err == nil {
			return "", errors.New("skills 1.5.23 cannot clone arbitrary commit IDs with --branch; use a verified tag or an independent copy")
		}
	}
	if entry.Ref != "" {
		source += "#" + entry.Ref
	}
	return source, nil
}

func skillHashes(entries []treeEntry) (string, string, error) {
	files := []treeEntry{}
	for _, e := range entries {
		if e.img.Kind != "file" {
			continue
		}
		for _, r := range e.rel {
			if r > 127 {
				return "", "", errors.New("provider hash ordering for non-ASCII paths is unverifiable")
			}
		}
		files = append(files, e)
	}
	c := collate.New(language.English)
	sort.Slice(files, func(i, j int) bool {
		n := c.CompareString(filepath.ToSlash(files[i].rel), filepath.ToSlash(files[j].rel))
		if n == 0 {
			return files[i].rel < files[j].rel
		}
		return n < 0
	})
	h := sha256.New()
	for _, e := range files {
		_, _ = h.Write([]byte(filepath.ToSlash(e.rel)))
		_, _ = h.Write(e.data)
	}
	objectHash := func(kind string, data []byte) []byte {
		h := sha1.New()
		_, _ = fmt.Fprintf(h, "%s %d\x00", kind, len(data))
		_, _ = h.Write(data)
		return h.Sum(nil)
	}
	var tree func(string) []byte
	tree = func(prefix string) []byte {
		type member struct {
			name string
			mode string
			hash []byte
			dir  bool
		}
		members := []member{}
		dirs := map[string]bool{}
		for _, e := range files {
			p := filepath.ToSlash(e.rel)
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			rest := strings.TrimPrefix(p, prefix)
			if head, _, ok := strings.Cut(rest, "/"); ok {
				dirs[head] = true
			} else {
				mode := "100644"
				if e.img.Mode&0o111 != 0 {
					mode = "100755"
				}
				members = append(members, member{rest, mode, objectHash("blob", e.data), false})
			}
		}
		for name := range dirs {
			members = append(members, member{name, "40000", tree(prefix + name + "/"), true})
		}
		sort.Slice(members, func(i, j int) bool {
			a, z := members[i].name, members[j].name
			if members[i].dir {
				a += "/"
			}
			if members[j].dir {
				z += "/"
			}
			return a < z
		})
		var data bytes.Buffer
		for _, m := range members {
			fmt.Fprintf(&data, "%s %s\x00", m.mode, m.name)
			data.Write(m.hash)
		}
		return objectHash("tree", data.Bytes())
	}
	return hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(tree("")), nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 8192 - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func providerEnvironment(staging string, forbidden ...string) []string {
	var env []string
	var paths []string
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(p) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		safe := !strings.Contains(filepath.ToSlash(resolved), "/node_modules/")
		for _, root := range forbidden {
			if pathInside(root, resolved) {
				safe = false
			}
		}
		if safe {
			paths = append(paths, resolved)
		}
	}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key != "PATH" && key != "XDG_STATE_HOME" && key != "DISABLE_TELEMETRY" && key != "DO_NOT_TRACK" {
			env = append(env, e)
		}
	}
	return append(env, "PATH="+strings.Join(paths, string(os.PathListSeparator)), "XDG_STATE_HOME="+filepath.Join(staging, "state"), "DISABLE_TELEMETRY=1", "DO_NOT_TRACK=1")
}

// Prepare is the explicit network boundary. The pinned provider runs only in a
// private staging checkout; agent files are published later by a separate plan.
func (s Service) Prepare(ctx context.Context, request TransferRequest) (out Plan, err error) {
	request.Mode = "install"
	req, err := normalize(request)
	if err != nil {
		return out, err
	}
	if req.Kind != "skill" {
		return out, ErrUnsupported
	}
	err = s.withLock(ctx, func() error {
		st, e := s.open(true)
		if e != nil {
			return e
		}
		defer st.close()
		b := newBuilder(ctx, st, req)
		defer b.close()
		lock, e := lockLocation(req.From)
		if e != nil {
			return e
		}
		_, data, e := b.read(lock)
		if e != nil {
			return e
		}
		entry, _, e := readLock(data, req.From.Scope, req.Name)
		if e != nil {
			return e
		}
		source, e := sourceArgument(entry)
		if e != nil {
			return e
		}
		loc, e := resolveSkillSource(b, location(req.From))
		if e != nil {
			return e
		}
		original, e := b.tree(loc)
		if e != nil {
			return e
		}
		if e = validateSkill(original); e != nil {
			return e
		}
		originalHash, originalTree, e := skillHashes(original)
		if e != nil {
			return e
		}
		if req.From.Scope == "project" && originalHash != entry.ComputedHash || req.From.Scope == "user" && originalTree != entry.SkillFolderHash {
			return errors.New("installed source differs from its upstream lock; choose copy explicitly or restore it first")
		}
		provider := agentskill.MutationProviderStatusFor(req.From.Root)
		if !provider.Available {
			return errors.New(provider.Detail)
		}
		if pathInside(req.To.Root, provider.Path) {
			return errors.New("skills provider is inside destination checkout")
		}
		stageName := uuid.NewString() + ".stage"
		if e = st.root.Mkdir(stageName, 0o700); e != nil {
			return e
		}
		defer st.root.RemoveAll(stageName)
		stage := filepath.Join(s.StateDir, stageName)
		limited, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		run := func(args ...string) (string, error) {
			var stdout, stderr boundedOutput
			cmd := exec.CommandContext(limited, provider.Path, args...)
			cmd.Dir = stage
			cmd.Env = providerEnvironment(stage, req.From.Root, req.To.Root)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if cmd.Run() != nil {
				return "", errors.New("skills provider failed; no agent files changed")
			}
			return strings.TrimSpace(stdout.String()), nil
		}
		version, e := run("--version")
		if e != nil {
			return e
		}
		version = strings.TrimPrefix(version, "skills ")
		if version != SkillsProviderVersion {
			return errors.New("unsupported skills executable version; install skills@1.5.23 or update the compatibility profile")
		}
		if _, e = run("add", source, "--skill", req.Name, "--agent", "universal", "--yes"); e != nil {
			return e
		}
		stageRoot, e := filepath.EvalSymlinks(stage)
		if e != nil {
			return e
		}
		probe := newBuilder(ctx, st, req)
		defer probe.close()
		entries, e := probe.tree(Location{stageRoot, filepath.Join(".agents", "skills", req.Name)})
		if e != nil {
			return errors.New("provider did not produce the selected regular skill tree")
		}
		if e = validateSkill(entries); e != nil {
			return e
		}
		hash, tree, e := skillHashes(entries)
		if e != nil {
			return e
		}
		if hash != originalHash || tree != originalTree {
			return errors.New("upstream content drifted from the selected installation; no agent files changed")
		}
		_, lockBytes, e := probe.read(Location{stageRoot, "skills-lock.json"})
		if e != nil {
			return e
		}
		generated, raw, e := readLock(lockBytes, "project", req.Name)
		if e != nil {
			return e
		}
		if generated.ComputedHash != hash || generated.SourceType != entry.SourceType || generated.Ref != entry.Ref || generated.SkillPath != entry.SkillPath {
			return errors.New("provider lock provenance differs from the selected source")
		}
		generatedSource, e := sourceArgument(generated)
		if e != nil || generatedSource != source {
			return errors.New("provider changed upstream source identity")
		}
		for _, item := range entries {
			img := item.img
			img.Identity = ""
			if img.Kind == "file" {
				img.Blob, e = st.put(ctx, item.data)
				if e != nil {
					return e
				}
				img.Digest = digest(st.key, item.data)
			}
			b.r.Payload = append(b.r.Payload, payload{Path: item.rel, Image: img})
		}
		blob, e := st.put(ctx, raw)
		if e != nil {
			return e
		}
		b.r.ProviderLock = &image{Kind: "file", Blob: blob, Digest: digest(st.key, raw), Mode: 0o600}
		b.r.Status = "prepared"
		b.r.Notes = append(b.r.Notes, "Prepared with skills@1.5.23; source, lock and content verified. Create an install plan with --prepared "+b.r.ID+".")
		out, e = b.save()
		return e
	})
	return out, err
}

func planPreparedSkill(b *builder) error {
	req := b.r.Request
	if req.Prepared == "" {
		return errors.New("run skill transfer prepare first, then pass its --prepared ID; plan never runs an installer")
	}
	p, err := b.st.load(b.ctx, req.Prepared)
	if err != nil {
		return err
	}
	if p.Status != "prepared" || p.Request.Kind != "skill" || p.Request.Name != req.Name || p.Request.From != req.From {
		return errors.New("preparation does not match the selected source")
	}
	for _, g := range p.Guards {
		v, _, e := b.read(g.Location)
		if e != nil || !sameImage(v, g.Image) {
			return ErrStale
		}
	}
	entries := []treeEntry{}
	for _, item := range p.Payload {
		if item.Path != "." {
			if err = validLocation(Location{Root: req.To.Root, Path: item.Path}); err != nil {
				return err
			}
		}
		data, e := b.st.bytes(b.ctx, item.Image)
		if e != nil {
			return e
		}
		entries = append(entries, treeEntry{item.Path, item.Image, data})
	}
	if err = validateSkill(entries); err != nil {
		return err
	}
	canonical := req.To
	if req.To.Scope == "project" {
		canonical.Path = filepath.Join(".agents", "skills", req.TargetName)
	} else {
		home, _ := os.UserHomeDir()
		canonical, err = absoluteArtifact(req.To, filepath.Join(home, ".agents", "skills", req.TargetName))
		if err != nil {
			return err
		}
	}
	if req.TargetName != req.Name {
		return errors.New("upstream installs retain their locked name; use copy to fork/rename")
	}
	target := location(canonical)
	v, _, err := b.read(target)
	if err != nil {
		return err
	}
	if v.Kind != "absent" {
		return conflict("upstream install target")
	}
	for _, entry := range entries {
		at := Location{target.Root, filepath.Join(target.Path, entry.rel)}
		if entry.img.Kind == "dir" {
			if err = b.parents(at); err != nil {
				return err
			}
			if err = b.change(at, image{Kind: "dir", Mode: entry.img.Mode}, nil); err != nil {
				return err
			}
		} else if err = b.write(at, entry.data, entry.img.Mode, false); err != nil {
			return err
		}
	}
	if p.ProviderLock == nil {
		return errors.New("preparation has no provider lock proof")
	}
	raw, err := b.st.bytes(b.ctx, *p.ProviderLock)
	if err != nil {
		return err
	}
	var entry map[string]any
	if json.Unmarshal(raw, &entry) != nil {
		return errors.New("invalid prepared lock")
	}
	lock, err := lockLocation(req.To)
	if err != nil {
		return err
	}
	observed, data, err := b.read(lock)
	if err != nil {
		return err
	}
	version := 1
	if req.To.Scope == "user" {
		version = 3
		_, tree, e := skillHashes(entries)
		if e != nil {
			return e
		}
		delete(entry, "computedHash")
		entry["skillFolderHash"] = tree
		now := time.Now().UTC().Format(time.RFC3339)
		entry["installedAt"], entry["updatedAt"] = now, now
	}
	if observed.Kind == "absent" {
		data = []byte(fmt.Sprintf("{\"version\":%d,\"skills\":{}}\n", version))
	} else {
		v, e := jsonDocument(data, false)
		if e != nil {
			return e
		}
		root, e := jsonMap(&v)
		if e != nil {
			return e
		}
		var actual int
		if json.Unmarshal(root["version"], &actual) != nil || actual != version {
			return errors.New("destination lock schema is unsupported")
		}
		if v.Find(pointer("skills", req.Name)) != nil {
			return conflict("destination lock membership")
		}
	}
	patched, err := patchJSON(data, false, []string{"skills", req.Name}, entry, false)
	if err != nil {
		return err
	}
	if err = b.write(lock, patched, 0o600, true); err != nil {
		return err
	}
	if location(req.To) != target {
		rel, e := filepath.Rel(filepath.Dir(filepath.Join(req.To.Root, req.To.Path)), filepath.Join(target.Root, target.Path))
		if e != nil {
			return e
		}
		if e = b.link(location(req.To), rel, false); e != nil {
			return e
		}
	}
	b.r.Notes = append(b.r.Notes, "Published verified provider content and native lock membership; no installer runs during apply.")
	return nil
}

// Keep source paths portable and credential-free before including them in a
// provider profile. This is also used by portable recipe validation.
func portableSourcePath(p string) bool {
	return utf8.ValidString(p) && safeText(p) && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && path.Clean(p) == p && !strings.HasPrefix(p, "../")
}

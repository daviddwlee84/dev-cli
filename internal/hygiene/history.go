package hygiene

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func refSnapshot(ctx context.Context, root string) (map[string]string, []byte, error) {
	raw, err := gitBytes(ctx, root, nil, "for-each-ref", "--format=%(refname)%00%(objectname)")
	if err != nil {
		return nil, nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) == 2 {
			refs[parts[0]] = parts[1]
		}
	}
	head, err := gitBytes(ctx, root, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, nil, err
	}
	refs["HEAD"] = strings.TrimSpace(string(head))
	return refs, append(raw, head...), nil
}
func (b *scanBuilder) history(ctx context.Context, o ScanOptions, privateDir string, config, ignore []byte) error {
	if len(o.Files) > 0 {
		return errors.New("history scan does not accept file selections")
	}
	refs, before, err := refSnapshot(ctx, b.s.Root)
	if err != nil {
		return err
	}
	b.record.Report.Refs = map[string]string{}
	tips := []string{}
	unique := map[string]bool{}
	for name, oid := range refs {
		b.record.Report.Refs[b.s.displayPath(name)] = oid
		if !unique[oid] {
			tips = append(tips, oid)
			unique[oid] = true
		}
	}
	sort.Strings(tips)
	if o.Range != "" {
		parts := regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})\.\.([a-f0-9]{40}|[a-f0-9]{64})$`).FindStringSubmatch(o.Range)
		if parts == nil {
			return errors.New("history range must be two full commit OIDs separated by ..")
		}
		for _, oid := range parts[1:] {
			if _, e := gitBytes(ctx, b.s.Root, nil, "cat-file", "-e", oid+"^{commit}"); e != nil {
				return errors.New("range commit is unavailable locally")
			}
		}
		tips = []string{o.Range}
		b.record.Report.Refs = map[string]string{"range_from": parts[1], "range_to": parts[2]}
	}
	shallow, err := gitBytes(ctx, b.s.Root, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(shallow)) == "true" {
		b.gap("", "shallow_history")
	}
	if b.s.Policy.Secrets != Off {
		rows, err := b.s.Engine.Scan(ctx, EngineRequest{Root: b.s.Root, PrivateDir: privateDir, Config: config, History: true, Refs: tips, Ignore: ignore, Audit: o.Audit})
		if err != nil {
			b.gap("", "secret_engine_failed")
			return err
		}
		for _, d := range rows {
			file := filepath.ToSlash(d.File)
			if !validRelative(file) {
				b.gap("", "invalid_engine_path")
				continue
			}
			b.add(d.RuleID, "secret", file, d.Commit, d.StartLine, d.Secret, b.s.Policy.Secrets, false)
		}
	}
	commits, err := gitBytes(ctx, b.s.Root, []byte(strings.Join(tips, "\n")+"\n"), "rev-list", "--stdin")
	if err != nil {
		return err
	}
	// Enumerating trees also covers merge-only blobs, removed files and paths
	// now ignored. Each blob/path pair is read once across the frozen history.
	seen := map[string]bool{}
	for _, commit := range strings.Fields(string(commits)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed := map[string]bool{}
		if o.Range != "" {
			paths, e := gitBytes(ctx, b.s.Root, nil, "diff-tree", "--root", "--no-commit-id", "--name-only", "--no-renames", "--diff-merges=first-parent", "-r", "-z", commit)
			if e != nil {
				return e
			}
			for _, p := range bytes.Split(paths, []byte{0}) {
				if len(p) > 0 {
					changed[string(p)] = true
				}
			}
		}
		tree, err := gitBytes(ctx, b.s.Root, nil, "ls-tree", "-r", "-l", "-z", commit)
		if err != nil {
			b.gap("", "history_tree_unreadable")
			return err
		}
		for _, entry := range bytes.Split(tree, []byte{0}) {
			pieces := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(pieces) != 2 {
				continue
			}
			fields := strings.Fields(string(pieces[0]))
			file := string(pieces[1])
			if o.Range != "" && !changed[file] {
				continue
			}
			if len(fields) != 4 || !validRelative(file) {
				b.gap("", "history_entry_invalid")
				continue
			}
			oid := fields[2]
			key := oid + "\x00" + file
			if seen[key] {
				continue
			}
			seen[key] = true
			if fields[0] != "100644" && fields[0] != "100755" {
				b.skip(file, "nonregular_history_entry")
				continue
			}
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size > MaxFileBytes {
				b.gap(file, "history_file_limit")
				continue
			}
			data, err := gitBytes(ctx, b.s.Root, nil, "cat-file", "blob", oid)
			if err != nil {
				b.gap(file, "history_blob_unreadable")
				return err
			}
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				b.binary(file)
				continue
			}
			b.record.Report.Files++
			b.record.Report.Bytes += int64(len(data))
			b.native(file, commit, data, false)
		}
	}
	_, after, err := refSnapshot(ctx, b.s.Root)
	if o.Range == "" && (err != nil || !bytes.Equal(before, after)) {
		b.gap("", "refs_changed")
	}
	return nil
}

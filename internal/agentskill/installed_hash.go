package agentskill

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type installedFile struct {
	name string
	data []byte
	mode fs.FileMode
}

// installedHashes fails closed for links inside a skill and oversized trees.
// Registry-level aliases are resolved by the caller and recorded separately.
func installedHashes(ctx context.Context, root string) (map[string]string, error) {
	var files []installedFile
	total := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if path != root && ignoredHashPath(entry.Name()) {
				return errors.New("skill contains private Git/dependency data; inspect individually")
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("skill contains a link or special file; inspect individually")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !isASCII(relative) {
			return errors.New("local hash ordering cannot be verified for non-ASCII paths")
		}
		data, err := safefile.ReadRegular(ctx, path, 16<<20)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 64<<20 || len(files) >= 10000 {
			return errors.New("installed skill exceeds inspection limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, installedFile{filepath.ToSlash(relative), data, info.Mode()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	c := collate.New(language.English)
	sort.Slice(files, func(i, j int) bool {
		if n := c.CompareString(files[i].name, files[j].name); n != 0 {
			return n < 0
		}
		return files[i].name < files[j].name
	})
	folder := sha256.New()
	snapshot := sha256.New()
	skillFile := ""
	for _, file := range files {
		folder.Write([]byte(file.name))
		folder.Write(file.data)
		fmt.Fprintf(snapshot, "%s\x00%d\x00%d\x00", file.name, file.mode, len(file.data))
		snapshot.Write(file.data)
		if file.name == "SKILL.md" {
			h := sha256.Sum256(file.data)
			skillFile = hex.EncodeToString(h[:])
		}
	}
	if skillFile == "" {
		return nil, errors.New("installed skill has no SKILL.md")
	}
	object := func(kind string, data []byte) []byte {
		h := sha1.New()
		fmt.Fprintf(h, "%s %d\x00", kind, len(data))
		h.Write(data)
		return h.Sum(nil)
	}
	var tree func(string) []byte
	tree = func(prefix string) []byte {
		type member struct {
			name, mode string
			hash       []byte
			dir        bool
		}
		var members []member
		dirs := map[string]bool{}
		for _, file := range files {
			if !strings.HasPrefix(file.name, prefix) {
				continue
			}
			rest := strings.TrimPrefix(file.name, prefix)
			if head, _, ok := strings.Cut(rest, "/"); ok {
				dirs[head] = true
			} else {
				mode := "100644"
				if file.mode&0111 != 0 {
					mode = "100755"
				}
				members = append(members, member{rest, mode, object("blob", file.data), false})
			}
		}
		for name := range dirs {
			members = append(members, member{name, "40000", tree(prefix + name + "/"), true})
		}
		sort.Slice(members, func(i, j int) bool {
			a, b := members[i].name, members[j].name
			if members[i].dir {
				a += "/"
			}
			if members[j].dir {
				b += "/"
			}
			return a < b
		})
		var data bytes.Buffer
		for _, m := range members {
			fmt.Fprintf(&data, "%s %s\x00", m.mode, m.name)
			data.Write(m.hash)
		}
		return object("tree", data.Bytes())
	}
	return map[string]string{"sha256-folder": hex.EncodeToString(folder.Sum(nil)), "git-tree": hex.EncodeToString(tree("")), "sha256-skill-file": skillFile, "snapshot": hex.EncodeToString(snapshot.Sum(nil))}, nil
}

func installedFingerprint(ctx context.Context, row Skill, checkDrift bool) (string, error) {
	if row.Lock == nil {
		return "", errors.New("skill is not lock-managed")
	}
	metadata := *row.Lock
	normalizeRecordedHash(&metadata)
	paths := map[string]bool{}
	for _, installation := range row.Installations {
		for _, p := range installation.LogicalPaths {
			paths[p] = true
		}
		if installation.Path != "" {
			paths[installation.Path] = true
		}
	}
	if len(paths) == 0 && row.Path != "" {
		paths[row.Path] = true
	}
	if row.Presence == PresenceMissing {
		return "missing", nil
	}
	if len(paths) == 0 {
		return "", errors.New("installed skill has no verifiable paths")
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	h := sha256.New()
	physical := map[string]map[string]string{}
	for _, path := range names {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return "", errors.New("skill path is not a directory")
		}
		hashes, ok := physical[real]
		if !ok {
			hashes, err = installedHashes(ctx, real)
			if err != nil {
				return "", err
			}
			physical[real] = hashes
		}
		if checkDrift && (metadata.RecordedHash == "" || !strings.EqualFold(hashes[metadata.HashKind], metadata.RecordedHash)) {
			return "", errors.New("installed files differ from the lock or cannot be verified; inspect local changes individually")
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", path, real, hashes["snapshot"])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

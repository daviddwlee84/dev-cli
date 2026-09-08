package agentinterop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

const maxBytes int64 = 8 << 20
const maxTotalBytes = 32 << 20
const maxFiles = 256

func digest(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Persistent identity excludes directory mtime: adding a reviewed child must
// not invalidate its held parent. Content/mode are checked separately.
func fileIdentity(info fs.FileInfo) (string, error) {
	if info == nil || info.Sys() == nil {
		return "", ErrUnsupported
	}
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if v.Kind() != reflect.Struct {
		return "", ErrUnsupported
	}
	dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
	if dev.IsValid() && ino.IsValid() {
		return fmt.Sprint(dev.Interface(), ":", ino.Interface()), nil
	}
	creation := v.FieldByName("CreationTime")
	if creation.IsValid() {
		return fmt.Sprint(creation.Interface()), nil
	}
	return "", errors.New("filesystem has no supported persistent identity")
}

func validLocation(l Location) error {
	if !filepath.IsAbs(l.Root) || filepath.Clean(l.Root) != l.Root {
		return errors.New("transfer root must be canonical and absolute")
	}
	if l.Path == "." {
		return nil
	}
	if err := pathx.ValidatePortableSlashPath(filepath.ToSlash(l.Path), pathx.PortablePathLimits{MaxPathBytes: 4096, MaxComponentBytes: 255, MaxDepth: 64}); err != nil {
		return errors.New("unsafe transfer path")
	}
	return nil
}

// openParent holds every traversed directory and rejects links in parents.
// The caller owns the root. Missing parents are a missing observation, never
// permission to follow a newly substituted parent when applying.
func openParent(root *os.Root, rel string) (*os.Root, string, func(), error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	current := root
	var held []*os.Root
	closeAll := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].Close()
		}
	}
	for _, part := range parts[:len(parts)-1] {
		next, _, err := safefile.OpenChildRoot(current, part)
		if err != nil {
			closeAll()
			return nil, "", func() {}, err
		}
		held = append(held, next)
		current = next
	}
	return current, parts[len(parts)-1], closeAll, nil
}

func snapshot(ctx context.Context, root *os.Root, rel string, key []byte) (image, []byte, error) {
	var parent *os.Root
	var leaf string
	var closeAll func()
	var err error
	if rel == "." {
		parent, leaf, closeAll = root, ".", func() {}
	} else {
		parent, leaf, closeAll, err = openParent(root, rel)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return image{Kind: "absent"}, nil, nil
	}
	if err != nil {
		return image{}, nil, err
	}
	defer closeAll()
	info, err := parent.Lstat(leaf)
	if errors.Is(err, fs.ErrNotExist) {
		return image{Kind: "absent"}, nil, nil
	}
	if err != nil {
		return image{}, nil, err
	}
	id, err := fileIdentity(info)
	if err != nil {
		return image{}, nil, err
	}
	out := image{Mode: info.Mode().Perm(), Identity: id}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		out.Kind = "link"
		out.Link, err = parent.Readlink(leaf)
		out.Digest = digest(key, []byte(out.Link))
	case info.IsDir():
		out.Kind = "dir"
		child, e := parent.OpenRoot(leaf)
		if e != nil {
			return image{}, nil, e
		}
		defer child.Close()
		f, e := child.Open(".")
		if e != nil {
			return image{}, nil, e
		}
		defer f.Close()
		entries, e := f.ReadDir(maxFiles + 1)
		if e != nil && !errors.Is(e, io.EOF) {
			return image{}, nil, e
		}
		if len(entries) > maxFiles {
			return image{}, nil, errors.New("directory entry limit exceeded")
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		b, _ := json.Marshal(names)
		out.Digest = digest(key, b)
		return out, b, nil
	case info.Mode().IsRegular():
		out.Kind = "file"
		b, _, e := safefile.ReadStableRegular(ctx, parent, leaf, info, maxBytes)
		if e != nil {
			return image{}, nil, e
		}
		out.Digest = digest(key, b)
		return out, b, nil
	default:
		return image{}, nil, errors.New("special files cannot be transferred")
	}
	return out, nil, err
}

func sameImage(a, b image) bool {
	return a.Kind == b.Kind && a.Identity == b.Identity && a.Mode == b.Mode && a.Digest == b.Digest && a.Link == b.Link
}
func sameContent(a, b image) bool {
	return a.Kind == b.Kind && a.Mode == b.Mode && a.Digest == b.Digest && a.Link == b.Link
}

// publish changes exactly one directory entry; recursive removal is never used.
// Directory cleanup fails if an unreviewed file has appeared.
func publish(ctx context.Context, root *os.Root, e effect, data []byte) error {
	parent, leaf, done, err := openParent(root, e.Path)
	if err != nil {
		return err
	}
	defer done()
	switch e.After.Kind {
	case "absent":
		return parent.Remove(leaf)
	case "dir":
		return parent.Mkdir(leaf, e.After.Mode)
	case "link":
		if e.Before.Kind == "absent" {
			return parent.Symlink(e.After.Link, leaf)
		}
		if e.Before.Kind != "file" && e.Before.Kind != "link" {
			return ErrConflict
		}
		tmp := ".dev-interop-" + uuid.NewString()
		if err = parent.Symlink(e.After.Link, tmp); err != nil {
			return err
		}
		defer parent.Remove(tmp)
		return parent.Rename(tmp, leaf)
	case "file":
		if e.Before.Kind == "absent" {
			_, err = safefile.CreateNoClobber(ctx, parent, leaf, data, e.After.Mode)
			return err
		}
		if e.Before.Kind == "link" {
			tmp := ".dev-interop-" + uuid.NewString()
			if _, err = safefile.CreateNoClobber(ctx, parent, tmp, data, e.After.Mode); err != nil {
				return err
			}
			defer parent.Remove(tmp)
			return parent.Rename(tmp, leaf)
		}
		if e.Before.Kind != "file" {
			return ErrConflict
		}
		observed, err := parent.Lstat(leaf)
		if err != nil {
			return err
		}
		_, err = safefile.AtomicReplace(ctx, parent, leaf, observed, data, e.After.Mode)
		return err
	}
	return ErrUnsupported
}

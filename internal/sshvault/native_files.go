package sshvault

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type nativePathEntry struct {
	path string
	info fs.FileInfo
	link string
	acl  nativeACLObservation
}

type nativePathRef struct {
	requested string
	canonical string
	entries   []nativePathEntry
}

type nativeFileRef struct {
	path   nativePathRef
	digest [32]byte
}

type nativeTool struct {
	entry       nativeFileRef
	runtime     *nativeFileRef
	manifest    *nativeFileRef
	bin         *nativePathRef
	binName     string
	packageRoot *nativePathRef
	version     string
}

func captureNativePath(path string, directory, private bool) (nativePathRef, error) {
	ref := nativePathRef{requested: path}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !validText(path, 4096) {
		return ref, ErrNativeContext
	}
	root := filepath.VolumeName(path) + string(os.PathSeparator)
	current := root
	pending := strings.Split(strings.TrimPrefix(path, root), string(os.PathSeparator))
	entry, err := captureNativeComponent(root, true)
	if err != nil {
		return ref, err
	}
	ref.entries = append(ref.entries, entry)
	links := 0
	for steps := 0; len(pending) > 0; steps++ {
		if steps >= 512 || len(pending) > 1024 {
			return ref, ErrNativeContext
		}
		component := pending[0]
		pending = pending[1:]
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			current = filepath.Dir(current)
			entry, err := captureNativeComponent(current, true)
			if err != nil {
				return ref, err
			}
			ref.entries = append(ref.entries, entry)
			continue
		}
		candidate := filepath.Join(current, component)
		entry, err := captureNativeComponent(candidate, len(pending) != 0)
		if err != nil {
			return ref, err
		}
		ref.entries = append(ref.entries, entry)
		if entry.info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return ref, ErrNativeContext
			}
			target := entry.link
			if filepath.IsAbs(target) {
				current = filepath.VolumeName(target) + string(os.PathSeparator)
				rootEntry, err := captureNativeComponent(current, true)
				if err != nil {
					return ref, err
				}
				ref.entries = append(ref.entries, rootEntry)
				target = strings.TrimPrefix(target, current)
			}
			// Do not Clean or Join the target before walking it: that would
			// erase unsafe components before ".." and intermediate symlinks.
			pending = append(strings.Split(target, string(os.PathSeparator)), pending...)
			continue
		}
		current = candidate
	}
	final, err := captureNativeComponent(current, directory)
	if err != nil || directory != final.info.IsDir() || !directory && !final.info.Mode().IsRegular() || !nativeOwned(final.info, private) || !validText(current, 4096) {
		return ref, ErrNativeContext
	}
	ref.entries = append(ref.entries, final)
	ref.canonical = current
	// Bind every traversed component, including intermediate link targets,
	// before returning a coherent observation of the complete resolution path.
	for _, expected := range ref.entries {
		actual, err := captureNativeComponent(expected.path, expected.info.IsDir())
		if err != nil || !sameNativeComponent(expected, actual) {
			return ref, ErrStale
		}
	}
	return ref, nil
}

func captureNativeComponent(path string, directory bool) (nativePathEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nativePathEntry{}, ErrNativeContext
	}
	entry := nativePathEntry{path: path, info: info}
	if info.Mode()&os.ModeSymlink != 0 {
		if !nativeLinkOwned(info) {
			return entry, ErrNativeContext
		}
		entry.link, err = os.Readlink(path)
		if err != nil || entry.link == "" || !validText("path:"+entry.link, 4101) {
			return entry, ErrNativeContext
		}
	} else if !nativeOwned(info, false) || directory && !info.IsDir() || !info.IsDir() && !info.Mode().IsRegular() {
		return entry, ErrNativeContext
	}
	entry.acl, err = captureNativeACL(path, info)
	if err != nil || !entry.acl.safe {
		return entry, ErrNativeContext
	}
	return entry, nil
}

func sameNativeComponent(left, right nativePathEntry) bool {
	return left.path == right.path && left.link == right.link && os.SameFile(left.info, right.info) && left.info.Mode() == right.info.Mode() && nativeSameOwner(left.info, right.info) && left.acl == right.acl
}

func sameNativePath(a, b nativePathRef) bool {
	if a.requested != b.requested || a.canonical != b.canonical || len(a.entries) != len(b.entries) {
		return false
	}
	for i, left := range a.entries {
		right := b.entries[i]
		if !sameNativeComponent(left, right) {
			return false
		}
	}
	return true
}

func captureNativeFile(ctx context.Context, path string, limit int64) (nativeFileRef, []byte, error) {
	ref, err := captureNativePath(path, false, false)
	if err != nil {
		return nativeFileRef{}, nil, ErrEntrypoint
	}
	file, err := os.Open(ref.canonical)
	if err != nil {
		return nativeFileRef{}, nil, ErrEntrypoint
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nativeFileRef{}, nil, ErrEntrypoint
	}
	expected, err := os.Lstat(ref.canonical)
	if err != nil || !os.SameFile(before, expected) || !nativeOwned(before, false) {
		return nativeFileRef{}, nil, ErrEntrypoint
	}
	hash := sha256.New()
	var prefix []byte
	var total int64
	buffer := make([]byte, 32<<10)
	for {
		if ctx.Err() != nil {
			return nativeFileRef{}, nil, ctx.Err()
		}
		n, readErr := file.Read(buffer)
		total += int64(n)
		if total > limit {
			return nativeFileRef{}, nil, ErrEntrypoint
		}
		_, _ = hash.Write(buffer[:n])
		if len(prefix) < 64<<10 {
			prefix = append(prefix, buffer[:min(n, (64<<10)-len(prefix))]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nativeFileRef{}, nil, ErrEntrypoint
		}
	}
	after, err := file.Stat()
	current, pathErr := captureNativePath(path, false, false)
	if err != nil || pathErr != nil || !sameNativePath(ref, current) || !os.SameFile(before, after) || before.Size() != total || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || !nativeSameOwner(before, after) {
		return nativeFileRef{}, nil, ErrStale
	}
	result := nativeFileRef{path: ref}
	copy(result.digest[:], hash.Sum(nil))
	return result, prefix, nil
}

func nativeBinary(prefix []byte) bool {
	if len(prefix) < 4 {
		return false
	}
	switch string(prefix[:4]) {
	case "\x7fELF", "\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe", "\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe", "\xca\xfe\xba\xbe", "\xbe\xba\xfe\xca", "\xca\xfe\xba\xbf", "\xbf\xba\xfe\xca":
		return true
	default:
		return false
	}
}

func parentTraversal(path string) bool {
	for _, component := range strings.Split(path, string(os.PathSeparator)) {
		if component == ".." {
			return true
		}
	}
	return false
}

func frozenLookPath(name, path, directory string) (string, error) {
	for _, part := range filepath.SplitList(path) {
		if part == "" || part == "." {
			part = directory
		} else {
			// Do not lexically erase resolution components from the PATH used
			// to choose a native executable. Ambiguous dot traversal is refused.
			if filepath.Clean(part) != part || parentTraversal(part) {
				return "", ErrEntrypoint
			}
			if !filepath.IsAbs(part) {
				part = filepath.Join(directory, part)
			}
		}
		candidate := filepath.Join(part, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", ErrEntrypoint
}

func captureNativeTool(ctx context.Context, path, directory string) (nativeTool, error) {
	requested, err := frozenLookPath("bw", path, directory)
	if err != nil {
		return nativeTool{}, err
	}
	entry, prefix, err := captureNativeFile(ctx, requested, 512<<20)
	if err != nil {
		return nativeTool{}, err
	}
	tool := nativeTool{entry: entry}
	if nativeBinary(prefix) {
		return tool, nil
	}
	line, _, _ := strings.Cut(string(prefix), "\n")
	line = strings.TrimSuffix(line, "\r")
	if line != "#!/usr/bin/env node" {
		return nativeTool{}, ErrEntrypoint
	}
	runtimePath, err := frozenLookPath("node", path, directory)
	if err != nil {
		return nativeTool{}, err
	}
	runtimeRef, runtimePrefix, err := captureNativeFile(ctx, runtimePath, 512<<20)
	if err != nil || !nativeBinary(runtimePrefix) {
		return nativeTool{}, ErrEntrypoint
	}
	tool.runtime = &runtimeRef
	current := filepath.Dir(entry.path.canonical)
	for depth := 0; depth < 8; depth++ {
		manifestPath := filepath.Join(current, "package.json")
		if _, err := os.Lstat(manifestPath); err == nil {
			manifest, body, err := captureNativeFile(ctx, manifestPath, 64<<10)
			if err != nil {
				return nativeTool{}, ErrEntrypoint
			}
			var info struct {
				Name    string          `json:"name"`
				Version string          `json:"version"`
				Bin     json.RawMessage `json:"bin"`
			}
			if json.Unmarshal(body, &info) != nil || info.Name != "@bitwarden/cli" || info.Version != BitwardenSchemaVersion {
				return nativeTool{}, ErrEntrypoint
			}
			var bin string
			if json.Unmarshal(info.Bin, &bin) != nil {
				var bins map[string]string
				if json.Unmarshal(info.Bin, &bins) != nil {
					return nativeTool{}, ErrEntrypoint
				}
				bin = bins["bw"]
			}
			rawBin := bin
			// A leading ./ names the already bound package directory; removing
			// only that prefix cannot erase an intervening filesystem component.
			for strings.HasPrefix(bin, "./") {
				bin = strings.TrimPrefix(bin, "./")
			}
			bin = filepath.FromSlash(bin)
			if bin == "" || filepath.IsAbs(bin) || strings.ContainsAny(bin, "\\\x00\r\n") || filepath.Clean(bin) != bin || parentTraversal(bin) {
				return nativeTool{}, ErrEntrypoint
			}
			binRef, err := captureNativePath(filepath.Join(current, bin), false, false)
			if err != nil || binRef.canonical != entry.path.canonical {
				return nativeTool{}, ErrEntrypoint
			}
			root, err := captureNativePath(current, true, false)
			if err != nil {
				return nativeTool{}, ErrEntrypoint
			}
			tool.manifest, tool.packageRoot, tool.version = &manifest, &root, info.Version
			tool.bin, tool.binName = &binRef, rawBin
			return tool, nil
		} else if !os.IsNotExist(err) {
			return nativeTool{}, ErrEntrypoint
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nativeTool{}, ErrEntrypoint
}

func (tool nativeTool) executable() string {
	if tool.runtime != nil {
		return tool.runtime.path.canonical
	}
	return tool.entry.path.canonical
}

func sameNativeTool(a, b nativeTool) bool {
	if !sameNativePath(a.entry.path, b.entry.path) || a.entry.digest != b.entry.digest || (a.runtime == nil) != (b.runtime == nil) || (a.manifest == nil) != (b.manifest == nil) || (a.bin == nil) != (b.bin == nil) || a.binName != b.binName || (a.packageRoot == nil) != (b.packageRoot == nil) || a.version != b.version {
		return false
	}
	if a.runtime != nil && (!sameNativePath(a.runtime.path, b.runtime.path) || a.runtime.digest != b.runtime.digest) {
		return false
	}
	if a.bin != nil && !sameNativePath(*a.bin, *b.bin) {
		return false
	}
	return a.manifest == nil || a.bin != nil && a.packageRoot != nil && sameNativePath(a.manifest.path, b.manifest.path) && a.manifest.digest == b.manifest.digest && sameNativePath(*a.packageRoot, *b.packageRoot)
}

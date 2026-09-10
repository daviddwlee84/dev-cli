package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

const manifestName = ".dev-cli-install.json"

type installManifest struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// Status compares installed content with this binary, without network or writes.
type Status struct {
	Installed bool
	Current   bool
	Legacy    bool
	Changed   []string
	Modified  []string
}

func Check(dir string) (Status, error) {
	var status Status
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return status, err
	}
	root, err := openSkillRoot(dir)
	if err != nil {
		return status, err
	}
	defer root.Close()
	manifest, err := readManifest(root)
	if err != nil {
		return status, err
	}
	status.Legacy = manifest == nil
	if status.Legacy {
		if err := validateFilePath(root, "SKILL.md"); err != nil {
			return status, err
		}
		body, err := root.ReadFile("SKILL.md")
		if errors.Is(err, os.ErrNotExist) {
			return status, nil
		}
		if err != nil {
			return status, err
		}
		front, _, ok := strings.Cut(strings.TrimPrefix(string(body), "---\n"), "\n---")
		if !strings.HasPrefix(string(body), "---\n") || !ok || !slices.Contains(strings.Split(front, "\n"), "name: "+Name) {
			return status, fmt.Errorf("%s is not a recognized dev-cli skill", dir)
		}
	}
	status.Installed = true
	all, err := Files()
	if err != nil {
		return status, err
	}
	for _, path := range sortedPaths(all) {
		if err := validateFilePath(root, path); err != nil {
			return status, err
		}
		body, err := root.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return status, err
		}
		if err != nil || string(body) != string(all[path]) {
			status.Changed = append(status.Changed, path)
		}
	}
	if manifest != nil {
		for _, path := range sortedPaths(manifest.Files) {
			if err := validateFilePath(root, path); err != nil {
				return status, err
			}
			body, err := root.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue // Missing generated files can be restored.
			}
			if err != nil {
				return status, err
			}
			if digest(body) != manifest.Files[path] {
				status.Modified = append(status.Modified, path)
			}
			if _, exists := all[path]; !exists {
				status.Changed = append(status.Changed, path)
			}
		}
	}
	status.Current = len(status.Changed) == 0
	return status, nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func sortedPaths[T any](files map[string]T) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func openSkillRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill directory must be a real directory: %s", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fmt.Errorf("skill directory changed while opening: %s", dir)
	}
	return root, nil
}

func validateFilePath(root *os.Root, path string) error {
	if !fs.ValidPath(path) || strings.Contains(path, "\\") || path == "." {
		return fmt.Errorf("invalid installed skill path: %q", path)
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("skill path is not a regular file/directory: %s", name)
		}
	}
	return nil
}

func writeSkillFile(root *os.Root, path string, body []byte) error {
	if err := validateFilePath(root, path); err != nil {
		return err
	}
	if existing, err := root.ReadFile(path); err == nil && string(existing) == string(body) {
		return nil
	}
	if err := root.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Atomic replacement also avoids writing through hard links. The install
	// lease serializes this fixed temporary name; O_EXCL rejects foreign files.
	tmp := path + ".dev-cli-tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, writeErr := f.Write(body)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return root.Rename(tmp, path)
}

func readManifest(root *os.Root) (*installManifest, error) {
	if err := validateFilePath(root, manifestName); err != nil {
		return nil, err
	}
	body, err := root.ReadFile(manifestName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest installManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("invalid skill installation manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Files) == 0 || manifest.Files["SKILL.md"] == "" {
		return nil, errors.New("unsupported skill installation manifest")
	}
	for path, hash := range manifest.Files {
		if !fs.ValidPath(path) || path == "." || path == manifestName || strings.Contains(path, "\\") {
			return nil, fmt.Errorf("invalid skill manifest path: %q", path)
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("invalid skill manifest hash for %s", path)
		}
	}
	return &manifest, nil
}

func writeManifest(root *os.Root, all map[string][]byte) error {
	manifest := installManifest{Version: 1, Files: map[string]string{}}
	for path, body := range all {
		manifest.Files[path] = digest(body)
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeSkillFile(root, manifestName, append(body, '\n'))
}

// UninstallPlan exposes the exact files/links approved for removal. Authority
// stays private; Apply re-reads it under the same lease used by installation.
type UninstallPlan struct {
	Dir   string
	Files []string
	Links []string
	info  os.FileInfo
	hash  string
}

func PlanUninstall(dir string) (UninstallPlan, error) {
	dir, err := filepath.Abs(dir)
	plan := UninstallPlan{Dir: dir}
	if err != nil {
		return plan, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	root, err := openSkillRoot(dir)
	if err != nil {
		return plan, err
	}
	defer root.Close()
	plan.info, err = root.Stat(".")
	if err != nil {
		return plan, err
	}
	manifest, err := readManifest(root)
	if err != nil {
		return plan, err
	}
	if manifest == nil {
		return plan, errors.New("legacy skill has no ownership manifest; run dev skill install with the same --dir first, then retry uninstall")
	}
	body, err := root.ReadFile(manifestName)
	if err != nil {
		return plan, err
	}
	plan.hash = digest(body)
	for _, path := range sortedPaths(manifest.Files) {
		if err := validateFilePath(root, path); err != nil {
			return plan, err
		}
		body, err := root.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return plan, err
		}
		if digest(body) != manifest.Files[path] {
			return plan, fmt.Errorf("preserving locally modified skill file %s; move your edits out before uninstalling", path)
		}
		plan.Files = append(plan.Files, path)
	}
	plan.Files = append(plan.Files, manifestName)
	for _, base := range LinkDirs() {
		path := filepath.Join(base, Name)
		if target, err := os.Readlink(path); err == nil && resolveLink(base, target) == dir {
			plan.Links = append(plan.Links, path)
		}
	}
	return plan, nil
}

func ApplyUninstall(plan UninstallPlan) error {
	if plan.info == nil {
		return nil
	}
	return lockx.WithFile(context.Background(), filepath.Join(filepath.Dir(plan.Dir), "."+filepath.Base(plan.Dir)+".install.lock"), "bundled skill", func() error {
		fresh, err := PlanUninstall(plan.Dir)
		if err != nil {
			return err
		}
		if fresh.info == nil || !os.SameFile(plan.info, fresh.info) || fresh.hash != plan.hash || !slices.Equal(fresh.Files, plan.Files) || !slices.Equal(fresh.Links, plan.Links) {
			return errors.New("skill uninstall preview is stale; preview again")
		}
		root, err := openSkillRoot(plan.Dir)
		if err != nil {
			return err
		}
		defer root.Close()
		for _, link := range plan.Links {
			if target, err := os.Readlink(link); err != nil || resolveLink(filepath.Dir(link), target) != plan.Dir {
				return errors.New("skill link changed since preview")
			}
			if err := os.Remove(link); err != nil {
				return err
			}
		}
		for _, path := range plan.Files {
			if err := root.Remove(path); err != nil {
				return err
			}
		}
		// Remove empty generated directories only; never recursively delete extras.
		for _, path := range plan.Files {
			for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
				_ = root.Remove(dir)
			}
		}
		root.Close()
		_ = os.Remove(plan.Dir)
		return nil
	})
}

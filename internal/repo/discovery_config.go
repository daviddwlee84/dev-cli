package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type DiscoveryScope string

const (
	DiscoveryExact  DiscoveryScope = "repo_paths"
	DiscoveryParent DiscoveryScope = "scan_roots"
)

// DiscoveryCovered follows discovery's depth/ignore/alias policy, not a lexical
// prefix test. Errors remain unknown rather than becoming an outside result.
func DiscoveryCovered(cfg config.Config, root string) (bool, error) {
	candidate, err := pathx.Canonical(root)
	if err != nil {
		return false, err
	}
	for _, exact := range cfg.RepoPaths() {
		p, err := pathx.Canonical(exact)
		if err != nil {
			return false, err
		}
		if p == candidate {
			return true, nil
		}
	}
	for _, scan := range cfg.ScanRoots() {
		covered, err := PathDiscoverableFromRoot(scan, candidate, DefaultOptions())
		if err != nil {
			return false, err
		}
		if covered {
			return true, nil
		}
	}
	return false, nil
}

// DiscoveryRegistrationPlan keeps publication authority private and immutable.
// A Blocked plan is a manual-edit preview only and cannot be applied.
type DiscoveryRegistrationPlan struct {
	rootInfo, commonInfo                   fs.FileInfo
	File, Field, Path, RepoPath, CommonDir string
	Before, After, Blocked                 string
	files                                  configedit.Plan
	root, common                           string
	ready                                  bool
}

func PlanDiscoveryRegistration(ctx context.Context, file, root, common string, scope DiscoveryScope) (DiscoveryRegistrationPlan, error) {
	p := DiscoveryRegistrationPlan{File: config.Expand(file), Field: string(scope), RepoPath: root, CommonDir: common}
	if scope != DiscoveryExact && scope != DiscoveryParent {
		return p, errors.New("unknown discovery scope")
	}
	if err := checkDiscoveryIdentity(ctx, root, common); err != nil {
		return p, err
	}
	var err error
	p.rootInfo, err = os.Stat(root)
	if err != nil {
		return p, err
	}
	p.commonInfo, err = os.Stat(common)
	if err != nil {
		return p, err
	}
	p.Path = root
	if scope == DiscoveryParent {
		p.Path = filepath.Dir(root)
	}
	// Store literal absolute paths. Environment expansion in config must not
	// reinterpret a legal filename containing '$'. Such paths need manual config.
	if strings.Contains(p.Path, "$") {
		return p, errors.New("path contains an environment-expansion marker; configure it manually")
	}
	p.After = fmt.Sprintf("Append %q to paths.%s, preserving existing entries", filepath.ToSlash(p.Path), p.Field)
	before, err := configedit.Read(ctx, p.File)
	if err != nil {
		p.Blocked = err.Error()
		return p, nil
	}
	cfg, err := config.Parse(before)
	if err != nil {
		p.Blocked = err.Error()
		return p, nil
	}
	covered, err := DiscoveryCovered(cfg, root)
	if err != nil {
		return p, err
	}
	if covered {
		return p, errors.New("repository is already covered by discovery; refresh REPOS")
	}
	if scope == DiscoveryParent {
		covered, err = PathDiscoverableFromRoot(p.Path, root, DefaultOptions())
		if err != nil {
			return p, err
		}
		if !covered {
			return p, errors.New("the parent scan cannot reach this repository; add the exact repository instead")
		}
	}
	values := cfg.Paths.RepoPaths
	if scope == DiscoveryParent {
		values = cfg.Paths.ScanRoots
	}
	setting := func(values []string) (string, error) {
		var encoded bytes.Buffer
		err := toml.NewEncoder(&encoded).Encode(map[string][]string{p.Field: values})
		return strings.TrimSpace(encoded.String()), err
	}
	p.Before, err = setting(values)
	if err != nil {
		return p, err
	}
	p.After, err = setting(append(append([]string(nil), values...), filepath.ToSlash(p.Path)))
	if err != nil {
		return p, err
	}
	after, err := config.PatchDiscoveryPath(before, p.Field, filepath.ToSlash(p.Path))
	if err != nil {
		p.Blocked = err.Error()
		return p, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		p.Blocked = "automatic configuration writes require macOS or Linux"
		return p, nil
	}
	p.files, err = configedit.New(ctx, []string{p.File}, [][]byte{after}, []string{p.File})
	if err != nil {
		return p, err
	}
	if err = p.files.RequireContents(map[string][]byte{p.File: before}); err != nil {
		return p, err
	}
	p.root, p.common, p.ready = root, common, true
	return p, nil
}

func ApplyDiscoveryRegistration(ctx context.Context, p DiscoveryRegistrationPlan) (configedit.Result, error) {
	if !p.ready {
		return configedit.Result{}, errors.New("discovery registration has no writable reviewed plan")
	}
	recovery := filepath.Join(config.DataHome(), "dev", "config-recovery")
	return configedit.ApplyChecked(ctx, p.files, recovery, func(ctx context.Context) error {
		rootInfo, err := os.Stat(p.root)
		if err != nil {
			return err
		}
		commonInfo, err := os.Stat(p.common)
		if err != nil {
			return err
		}
		if !os.SameFile(p.rootInfo, rootInfo) || !os.SameFile(p.commonInfo, commonInfo) {
			return errors.New("startup repository was replaced since preview")
		}
		return checkDiscoveryIdentity(ctx, p.root, p.common)
	})
}

func checkDiscoveryIdentity(ctx context.Context, root, common string) error {
	if !filepath.IsAbs(root) || common == "" {
		return errors.New("missing startup repository identity")
	}
	found, err := gitx.Discover(ctx, root)
	if err != nil {
		return err
	}
	actual, err := pathx.Canonical(found.GitCommonDir)
	if err != nil {
		return err
	}
	main, err := pathx.Canonical(found.MainRoot)
	if err != nil {
		return err
	}
	if found.Bare || actual != common || main != root {
		return errors.New("startup repository identity changed; reopen the dashboard")
	}
	return nil
}

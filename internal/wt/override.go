package wt

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
)

// RepoOverride is a per-repository provisioning override, committed at the
// repo root as .dev.toml. Global config sets a sensible default for every
// project; a repo that needs something specific (a Makefile bootstrap target,
// an extra env file) says so next to its code, where a teammate on another
// machine picks it up automatically.
//
//	# .dev.toml
//	[worktree]
//	include     = [".env", "config/local.json"]
//	link        = ["node_modules"]
//	post_create = ["make bootstrap"]
type RepoOverride struct {
	Worktree struct {
		Include    []string           `toml:"include"`
		Link       []string           `toml:"link"`
		PostCreate *config.PostCreate `toml:"post_create"`
	} `toml:"worktree"`
}

// OverrideFilename is the per-repo config file dev looks for.
const OverrideFilename = ".dev.toml"

// LoadRepoOverride reads <repoPath>/.dev.toml if present. A malformed file is
// reported as absent rather than fatal: a broken override in someone else's
// repo must not stop you creating a worktree.
func LoadRepoOverride(repoPath string) (RepoOverride, bool) {
	path := filepath.Join(repoPath, OverrideFilename)
	if _, err := os.Stat(path); err != nil {
		return RepoOverride{}, false
	}
	var o RepoOverride
	if _, err := toml.DecodeFile(path, &o); err != nil {
		return RepoOverride{}, false
	}
	return o, true
}

// applyOverride replaces only the fields the repo actually set, so a repo that
// pins post_create still inherits the user's global include list.
// ApplyOverride is the exported form used by `dev wt provision`.
func (p *Provisioner) ApplyOverride(o RepoOverride) {
	if len(o.Worktree.Include) > 0 {
		p.Include = o.Worktree.Include
	}
	if len(o.Worktree.Link) > 0 {
		p.Link = o.Worktree.Link
	}
	if o.Worktree.PostCreate != nil {
		p.Cmds = *o.Worktree.PostCreate
	}
}

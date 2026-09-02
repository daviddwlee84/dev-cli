package sshhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Paths is the complete local SSH host filesystem surface. NewPaths should be
// preferred to struct literals because Service validates that every path is the
// expected direct child of Home.
type Paths struct {
	Home       string `json:"home"`
	SSHDir     string `json:"ssh_dir"`
	RootConfig string `json:"root_config"`
	ManagedDir string `json:"managed_dir"`
}

// DefaultPaths resolves the current user's home and derives ~/.ssh paths.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}
	return NewPaths(home)
}

// NewPaths derives the fixed user root and dev-owned namespace from home.
func NewPaths(home string) (Paths, error) {
	if strings.TrimSpace(home) == "" {
		return Paths{}, fmt.Errorf("SSH home is empty: %w", ErrUnsafePath)
	}
	absolute, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("make SSH home absolute: %w", err)
	}
	absolute = filepath.Clean(absolute)
	paths := Paths{
		Home:       absolute,
		SSHDir:     filepath.Join(absolute, ".ssh"),
		RootConfig: filepath.Join(absolute, ".ssh", "config"),
		ManagedDir: filepath.Join(absolute, ".ssh", "dev.d"),
	}
	return paths, paths.Validate()
}

// Validate rejects custom path layouts; v1 owns exactly ~/.ssh/dev.d.
func (p Paths) Validate() error {
	if !filepath.IsAbs(p.Home) || filepath.Clean(p.Home) != p.Home {
		return fmt.Errorf("home %q must be absolute and clean: %w", p.Home, ErrUnsafePath)
	}
	wantSSH := filepath.Join(p.Home, ".ssh")
	wantRoot := filepath.Join(wantSSH, "config")
	wantManaged := filepath.Join(wantSSH, "dev.d")
	if filepath.Clean(p.SSHDir) != wantSSH || filepath.Clean(p.RootConfig) != wantRoot || filepath.Clean(p.ManagedDir) != wantManaged {
		return fmt.Errorf("SSH paths must be rooted at %s: %w", wantSSH, ErrUnsafePath)
	}
	return nil
}

// ManagedPath returns the only path a managed alias may own.
func (p Paths) ManagedPath(alias string) (string, error) {
	if err := ValidateManagedAlias(alias); err != nil {
		return "", err
	}
	name := alias + ".conf"
	if err := pathx.ValidateComponent(name); err != nil {
		return "", fmt.Errorf("managed filename: %w", err)
	}
	return filepath.Join(p.ManagedDir, name), nil
}

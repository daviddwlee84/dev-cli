package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
)

const ConfigSchemaVersion = 1

type PasswordSource struct {
	Type  string `toml:"type" json:"type"`
	Value string `toml:"value" json:"-"`
	Item  string `toml:"item" json:"item,omitempty"`
}

type Defaults struct {
	ConnectTimeout devconfig.Duration `toml:"connect_timeout"`
	CommandTimeout devconfig.Duration `toml:"command_timeout"`
	CacheTTL       devconfig.Duration `toml:"cache_ttl"`
	MaxParallel    int                `toml:"max_parallel"`
	DevPath        string             `toml:"dev_path"`
}

type Host struct {
	Name         string `toml:"name" json:"name"`
	SSHAlias     string `toml:"ssh_alias" json:"ssh_alias,omitempty"`
	Hostname     string `toml:"hostname" json:"hostname,omitempty"`
	User         string `toml:"user" json:"user,omitempty"`
	Port         int    `toml:"port" json:"port,omitempty"`
	IdentityFile string `toml:"identity_file" json:"identity_file,omitempty"`
	DevPath      string `toml:"dev_path" json:"dev_path,omitempty"`

	ConnectTimeout devconfig.Duration `toml:"connect_timeout" json:"-"`
	CommandTimeout devconfig.Duration `toml:"command_timeout" json:"-"`

	SSHLoginPasswordSource PasswordSource `toml:"ssh_login_password_source" json:"ssh_login_password_source"`
}

type Config struct {
	SchemaVersion int      `toml:"schema_version"`
	Defaults      Defaults `toml:"defaults"`
	Hosts         []Host   `toml:"hosts"`
	Source        string   `toml:"-"`
}

func DefaultConfig() Config {
	return Config{
		SchemaVersion: ConfigSchemaVersion,
		Defaults: Defaults{
			ConnectTimeout: devconfig.Duration{Duration: 15 * time.Second},
			CommandTimeout: devconfig.Duration{Duration: 5 * time.Minute},
			CacheTTL:       devconfig.Duration{Duration: 15 * time.Minute},
			MaxParallel:    4,
			DevPath:        "auto",
		},
	}
}

func ConfigFile() string {
	return filepath.Join(devconfig.ConfigHome(), "dev", "remotes.toml")
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = ConfigFile()
	}
	metadata, err := toml.DecodeFile(path, &cfg)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, nil
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return cfg, fmt.Errorf("read %s: unknown field(s): %v", path, undecoded)
	}
	cfg.Source = path
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) ApplyDefaults() {
	for index := range c.Hosts {
		host := &c.Hosts[index]
		if host.DevPath == "" {
			host.DevPath = c.Defaults.DevPath
		}
		if host.ConnectTimeout.Duration == 0 {
			host.ConnectTimeout = c.Defaults.ConnectTimeout
		}
		if host.CommandTimeout.Duration == 0 {
			host.CommandTimeout = c.Defaults.CommandTimeout
		}
	}
}

func (c Config) Validate() error {
	if c.SchemaVersion != ConfigSchemaVersion {
		return fmt.Errorf("remotes schema_version %d: want %d", c.SchemaVersion, ConfigSchemaVersion)
	}
	if c.Defaults.MaxParallel <= 0 {
		return errors.New("defaults.max_parallel must be positive")
	}
	if c.Defaults.ConnectTimeout.Duration <= 0 || c.Defaults.CommandTimeout.Duration <= 0 {
		return errors.New("default timeouts must be positive")
	}
	seen := map[string]bool{}
	for index, host := range c.Hosts {
		if strings.TrimSpace(host.Name) == "" || host.Name != strings.TrimSpace(host.Name) {
			return fmt.Errorf("hosts[%d].name is required and must be trimmed", index)
		}
		if seen[host.Name] {
			return fmt.Errorf("duplicate host name %q", host.Name)
		}
		seen[host.Name] = true
		if host.SSHAlias == "" && host.Hostname == "" {
			return fmt.Errorf("host %q must define ssh_alias or hostname", host.Name)
		}
		if host.Port < 0 || host.Port > 65535 {
			return fmt.Errorf("host %q port %d is invalid", host.Name, host.Port)
		}
		if host.ConnectTimeout.Duration <= 0 || host.CommandTimeout.Duration <= 0 {
			return fmt.Errorf("host %q timeouts must be positive", host.Name)
		}
		if host.DevPath != "" && host.DevPath != "auto" && !filepath.IsAbs(devconfig.Expand(host.DevPath)) {
			return fmt.Errorf("host %q dev_path must be auto or absolute", host.Name)
		}
		if err := validatePasswordSource(host.Name, host.SSHLoginPasswordSource); err != nil {
			return err
		}
	}
	return nil
}

func validatePasswordSource(host string, source PasswordSource) error {
	kind := strings.ToLower(strings.TrimSpace(source.Type))
	if kind == "" {
		kind = "none"
	}
	switch kind {
	case "none", "prompt":
		return nil
	case "plain":
		if source.Value == "" {
			return fmt.Errorf("host %q plain ssh_login_password_source requires value", host)
		}
	case "bitwarden":
		if source.Item == "" {
			return fmt.Errorf("host %q bitwarden ssh_login_password_source requires item", host)
		}
	default:
		return fmt.Errorf("host %q ssh_login_password_source.type %q: want none, plain, prompt or bitwarden", host, source.Type)
	}
	return nil
}

func (h Host) PasswordKind() string {
	kind := strings.ToLower(strings.TrimSpace(h.SSHLoginPasswordSource.Type))
	if kind == "" {
		return "none"
	}
	return kind
}

func (h Host) Destination() string {
	if h.SSHAlias != "" {
		return h.SSHAlias
	}
	if h.User != "" {
		return h.User + "@" + h.Hostname
	}
	return h.Hostname
}

func CheckPrivateMode(path string, cfg Config) error {
	needsPrivate := false
	for _, host := range cfg.Hosts {
		if host.PasswordKind() == "plain" {
			needsPrivate = true
			break
		}
	}
	if !needsPrivate {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s contains a plaintext SSH password and must have mode 0600", path)
	}
	return nil
}

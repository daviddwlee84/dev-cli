package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
)

const (
	ConfigSchemaVersion = 1
	RemoteOSPOSIX       = "posix"
	RemoteOSWindows     = "windows"
)

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
	RemoteOS     string `toml:"remote_os" json:"remote_os"`

	ConnectTimeout devconfig.Duration `toml:"connect_timeout" json:"-"`
	CommandTimeout devconfig.Duration `toml:"command_timeout" json:"-"`

	SSHLoginPasswordSource PasswordSource `toml:"ssh_login_password_source" json:"ssh_login_password_source"`

	origin  string
	managed bool
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

// Origin reports the file that supplied this host. It is deliberately omitted
// from TOML and JSON output.
func (h Host) Origin() string { return h.origin }

// Managed reports whether this host came from a dev-owned managed fragment. It
// is deliberately omitted from TOML and JSON output.
func (h Host) Managed() bool { return h.managed }

// EffectiveRemoteOS preserves compatibility with configurations written before
// remote_os existed: an empty value always means a POSIX target.
func (h Host) EffectiveRemoteOS() string {
	if h.RemoteOS == "" {
		return RemoteOSPOSIX
	}
	return h.RemoteOS
}

func LoadConfig(primaryPath string) (Config, error) {
	cfg := DefaultConfig()
	if primaryPath == "" {
		primaryPath = ConfigFile()
	}

	metadata, err := toml.DecodeFile(primaryPath, &cfg)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A missing primary is valid. Managed fragments still participate.
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", primaryPath, err)
	default:
		if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
			return cfg, fmt.Errorf("read %s: unknown field(s): %v", primaryPath, undecoded)
		}
		cfg.Source = primaryPath
		for index := range cfg.Hosts {
			cfg.Hosts[index].origin = primaryPath
		}
	}

	managed, err := loadManagedFragments(primaryPath)
	if err != nil {
		return cfg, err
	}
	cfg.Hosts = append(cfg.Hosts, managed...)
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
		if host.RemoteOS == "" {
			host.RemoteOS = RemoteOSPOSIX
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
	type aliasOwner struct {
		name    string
		managed bool
	}
	seenNames := map[string]bool{}
	seenAliases := map[string]aliasOwner{}
	for index, host := range c.Hosts {
		if strings.TrimSpace(host.Name) == "" || host.Name != strings.TrimSpace(host.Name) {
			return fmt.Errorf("hosts[%d].name is required and must be trimmed", index)
		}
		if seenNames[host.Name] {
			return fmt.Errorf("duplicate host name %q", host.Name)
		}
		seenNames[host.Name] = true
		if host.SSHAlias == "" && host.Hostname == "" {
			return fmt.Errorf("host %q must define ssh_alias or hostname", host.Name)
		}
		if host.Port < 0 || host.Port > 65535 {
			return fmt.Errorf("host %q port %d is invalid", host.Name, host.Port)
		}
		if host.ConnectTimeout.Duration <= 0 || host.CommandTimeout.Duration <= 0 {
			return fmt.Errorf("host %q timeouts must be positive", host.Name)
		}
		remoteOS := host.EffectiveRemoteOS()
		if remoteOS != RemoteOSPOSIX && remoteOS != RemoteOSWindows {
			return fmt.Errorf("host %q remote_os %q: want posix or windows", host.Name, host.RemoteOS)
		}
		if err := validateDevPath(host, remoteOS); err != nil {
			return err
		}
		if err := validatePasswordSource(host.Name, host.SSHLoginPasswordSource); err != nil {
			return err
		}
		if host.SSHAlias != "" {
			aliasKey := strings.ToLower(host.SSHAlias)
			if previous, ok := seenAliases[aliasKey]; ok && (previous.managed || host.managed) {
				return fmt.Errorf("ssh_alias %q is shared by host %q and managed host %q", host.SSHAlias, previous.name, host.Name)
			}
			if _, ok := seenAliases[aliasKey]; !ok {
				seenAliases[aliasKey] = aliasOwner{name: host.Name, managed: host.managed}
			}
		}
	}
	return nil
}

func validateDevPath(host Host, remoteOS string) error {
	if host.DevPath == "" || host.DevPath == "auto" {
		return nil
	}
	if host.managed && remoteOS == RemoteOSWindows {
		return fmt.Errorf("managed Windows host %q requires dev_path = auto", host.Name)
	}
	valid := false
	switch remoteOS {
	case RemoteOSPOSIX:
		valid = path.IsAbs(host.DevPath) && !strings.ContainsRune(host.DevPath, '\x00')
	case RemoteOSWindows:
		valid = isWindowsAbs(host.DevPath)
	}
	if !valid {
		return fmt.Errorf("host %q dev_path must be auto or an absolute %s path", host.Name, remoteOS)
	}
	return nil
}

func isWindowsAbs(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return false
	}
	isSeparator := func(value byte) bool { return value == '\\' || value == '/' }
	isLetter := func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
	}
	if len(value) >= 3 && isLetter(value[0]) && value[1] == ':' && isSeparator(value[2]) {
		return true
	}
	normalized := strings.ReplaceAll(value, "/", "\\")
	if strings.HasPrefix(normalized, `\\.\`) {
		return false
	}
	extendedUNC := `\\?\UNC\`
	if len(normalized) >= len(extendedUNC) && strings.EqualFold(normalized[:len(extendedUNC)], extendedUNC) {
		normalized = `\\` + normalized[len(extendedUNC):]
	} else if strings.HasPrefix(normalized, `\\?\`) {
		tail := strings.TrimPrefix(normalized, `\\?\`)
		return len(tail) >= 3 && isLetter(tail[0]) && tail[1] == ':' && tail[2] == '\\'
	}
	if !strings.HasPrefix(normalized, `\\`) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(normalized, `\\`), `\`)
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
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
	return checkPrivateConfigFile(path)
}

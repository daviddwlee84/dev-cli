package hygiene

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

//go:embed assets/gitleaks.toml
var DefaultGitleaks []byte

type Detection struct {
	RuleID, File, Secret, Commit string
	StartLine, EndLine           int
}
type EngineRequest struct {
	Root, PrivateDir string
	Config, Ignore   []byte
	History, Audit   bool
	Refs             []string
}
type Engine interface {
	Scan(context.Context, EngineRequest) ([]Detection, error)
}
type Gitleaks struct{ Binary string }

func supportedScannerVersion(value string) bool {
	parts := regexp.MustCompile(`^v?(8)\.(\d+)\.(\d+)$`).FindStringSubmatch(strings.TrimSpace(value))
	if len(parts) != 4 {
		return false
	}
	minor, err := strconv.Atoi(parts[2])
	return err == nil && minor >= 30
}

// CheckVersion rejects versions that can silently ignore scoped allowlists.
// This explicit execution is used by setup/scan, not passive status/inventory.
func (g Gitleaks) CheckVersion(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	name := g.Binary
	if name == "" {
		name = "gitleaks"
	}
	r, err := (sshhost.ExecRunner{}).Run(ctx, sshhost.RunRequest{Name: name, Args: []string{"version"}, UnsetEnv: gitEnvironmentNames(), Display: "hygiene scanner version"})
	if err != nil || r.ExitCode != 0 || r.StdoutTruncated || !supportedScannerVersion(string(r.Stdout)) {
		return errors.New("gitleaks 8.30.0 or newer compatible 8.x is required; check the installed scanner")
	}
	return nil
}

func (g Gitleaks) Scan(ctx context.Context, q EngineRequest) ([]Detection, error) {
	if err := g.CheckVersion(ctx); err != nil {
		return nil, err
	}
	configPath := filepath.Join(q.PrivateDir, "engine.toml")
	reportPath := filepath.Join(q.PrivateDir, "engine.json")
	ignorePath := filepath.Join(q.PrivateDir, "ignore")
	for p, b := range map[string][]byte{configPath: q.Config, ignorePath: q.Ignore} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return nil, errors.New("cannot prepare private scanner input")
		}
	}
	args := []string{"git", q.Root, "--config", configPath, "--gitleaks-ignore-path", ignorePath, "--report-format=json", "--report-path", reportPath, "--exit-code=10", "--no-banner", "--no-color", "--redact=100"}
	if q.Audit {
		args = append(args, "--ignore-gitleaks-allow")
	}
	if q.History {
		args = append(args, "--log-opts=--full-history --diff-merges=first-parent --no-ext-diff --no-textconv "+strings.Join(q.Refs, " "))
	} else {
		args = append(args, "--staged")
	}
	// Machine output must retain secret bytes for exact-span plans. It is never
	// forwarded to stdout/stderr and lives only in this private, removed directory.
	args = append(args, "--redact=0")
	name := g.Binary
	if name == "" {
		name = "gitleaks"
	}
	r, err := (sshhost.ExecRunner{}).Run(ctx, sshhost.RunRequest{Name: name, Args: args, UnsetEnv: gitEnvironmentNames(), Env: []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.longpaths", "GIT_CONFIG_VALUE_0=true", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_PAGER=cat", "GITLEAKS_CONFIG=", "GITLEAKS_CONFIG_TOML="}, Display: "hygiene scanner"})
	if err != nil {
		return nil, errors.New("gitleaks unavailable, canceled or failed to launch")
	}
	if r.ExitCode != 0 && r.ExitCode != 10 {
		return nil, errors.New("gitleaks scan failed (exit " + strconv.Itoa(r.ExitCode) + "); result is incomplete")
	}
	b, err := safefile.ReadStablePath(ctx, reportPath, MaxRecordBytes)
	if err != nil {
		return nil, errors.New("gitleaks report missing or oversized")
	}
	var rows []Detection
	if len(b) == 0 || json.Unmarshal(b, &rows) != nil {
		return nil, errors.New("gitleaks report invalid; result is incomplete")
	}
	for _, d := range rows {
		if d.RuleID == "" || d.File == "" || d.StartLine < 1 || d.Secret == "" {
			return nil, errors.New("gitleaks finding is incomplete")
		}
	}
	return rows, nil
}

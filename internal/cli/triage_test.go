package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func TestTriageCLIReadOnlyJSONAndText(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	t.Chdir(r.Root)
	r.Git("branch", "local-only")
	r.Write("forgotten", "preserve")
	cfg := config.Default()
	cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Paths.ScanRoots = []string{r.Root}
	cfg.Paths.RepoPaths = nil
	cfg.Paths.TriesRoot = filepath.Join(t.TempDir(), "tries")
	cfg.Runtime.Backend = "none"
	var encoded bytes.Buffer
	if e := toml.NewEncoder(&encoded).Encode(cfg); e != nil {
		t.Fatal(e)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if e := os.WriteFile(configPath, encoded.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	for _, format := range []string{"--json", "--report"} {
		var out, errOut bytes.Buffer
		root := NewRootCommandWithIO(&out, &errOut)
		root.SetArgs([]string{"--config", configPath, "--no-runtime", "triage", format})
		if e := root.Execute(); e != nil {
			t.Fatalf("%v: %s", e, errOut.String())
		}
		if format == "--json" {
			var report triage.Report
			if e := json.Unmarshal(out.Bytes(), &report); e != nil {
				t.Fatal(e)
			}
			if report.SchemaVersion != 1 || report.Complete {
				t.Fatalf("report=%+v", report)
			}
			if !strings.Contains(out.String(), "refs/heads/local-only") {
				t.Fatal("un-checked-out branch missing")
			}
		} else if !strings.Contains(out.String(), "uncommitted") {
			t.Fatal("missing text finding")
		}
		cmd, _, e := root.Find([]string{"triage"})
		if e != nil || !passiveCommandSkipsNudge(cmd) {
			t.Fatal("triage could refresh release data")
		}
	}
	if _, e := os.Stat(cfg.Paths.StateDir); !os.IsNotExist(e) {
		t.Fatalf("report wrote state: %v", e)
	}
	if data, e := os.ReadFile(filepath.Join(r.Root, "forgotten")); e != nil || string(data) != "preserve" {
		t.Fatal("uncommitted work changed")
	}
}

func TestTriageRejectsInvalidSelectors(t *testing.T) {
	for _, args := range [][]string{{"--kind", "mystery"}, {"--stale-days", "0"}} {
		cmd := newTriageCmd(&App{})
		if e := cmd.ParseFlags(args); e != nil {
			t.Fatal(e)
		}
		if e := cmd.RunE(cmd, nil); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
)

type triesJSONFixture struct {
	Identity struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"identity"`
	Experiment struct {
		Phase   string  `json:"phase"`
		Started *string `json:"started"`
	} `json:"experiment"`
	Location *struct {
		State       string `json:"state"`
		CurrentPath string `json:"current_path"`
		RestorePath string `json:"restore_path"`
	} `json:"location"`
	Live struct {
		Present bool `json:"present"`
	} `json:"live"`
	Metadata struct {
		Tags []string `json:"tags"`
		Note string   `json:"note"`
	} `json:"metadata"`
	Size *struct {
		CheckoutBytes int64  `json:"checkout_bytes"`
		OwnedBytes    int64  `json:"owned_bytes"`
		TotalBytes    *int64 `json:"total_bytes"`
		Complete      bool   `json:"complete"`
		Cached        bool   `json:"cached"`
	} `json:"size"`
}

func decodeTriesJSON(t *testing.T, body string) []triesJSONFixture {
	t.Helper()
	var rows []triesJSONFixture
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("invalid tries JSON: %v\n%s", err, body)
	}
	return rows
}

func enableTryScanning(t *testing.T, h *harness) {
	t.Helper()
	body, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	old := `scan_roots = ["` + filepath.ToSlash(h.scanRoot) + `"]`
	triesRoot := filepath.Join(h.home, "tries")
	replacement := `scan_roots = ["` + filepath.ToSlash(h.scanRoot) + `", "` + filepath.ToSlash(triesRoot) + `"]`
	updated := strings.Replace(string(body), old, replacement, 1)
	if updated == string(body) {
		t.Fatalf("scan_roots line not found in config:\n%s", body)
	}
	if err := os.WriteFile(h.configPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTriesLifecycleCommandsAndStableJSON(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "lifecycle", "--no-git")

	rows := decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if len(rows) != 1 || rows[0].Identity.ID == "" || rows[0].Identity.Kind != "try" ||
		rows[0].Experiment.Phase != "active" || rows[0].Experiment.Started == nil ||
		rows[0].Location == nil || rows[0].Location.State != "present" || !rows[0].Live.Present {
		t.Fatalf("initial tries JSON = %+v", rows)
	}
	id := rows[0].Identity.ID

	h.mustRun("tries", "mark", id, "--add", "API", "--add", "api", "--add", "Prototype", "--note", "keep this")
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if got := strings.Join(rows[0].Metadata.Tags, ","); got != "api,prototype" || rows[0].Metadata.Note != "keep this" {
		t.Fatalf("marked metadata = %+v", rows[0].Metadata)
	}

	h.mustRun("tries", "abandon", id)
	if rows := decodeTriesJSON(t, h.mustRun("tries", "list", "--json")); len(rows) != 0 {
		t.Fatalf("deprecated Try leaked into default list: %+v", rows)
	}
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--all", "--json"))
	if len(rows) != 1 || rows[0].Experiment.Phase != "deprecated" || rows[0].Location.State != "present" {
		t.Fatalf("deprecated history = %+v", rows)
	}

	h.mustRun("tries", "archive", id)
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--all", "--json"))
	if len(rows) != 1 || rows[0].Location.State != "archived" || rows[0].Location.RestorePath == "" || rows[0].Live.Present {
		t.Fatalf("archived history = %+v", rows)
	}
	if _, _, err := h.run("tries", "open", id); err == nil {
		t.Fatal("archived Try opened")
	}

	h.mustRun("tries", "reactivate", id)
	h.mustRun("tries", "restore", id)
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if len(rows) != 1 || rows[0].Experiment.Phase != "active" || rows[0].Location.State != "present" || !rows[0].Live.Present {
		t.Fatalf("restored active Try = %+v", rows)
	}

	oldPath := rows[0].Location.CurrentPath
	attachedPath := filepath.Join(filepath.Dir(oldPath), "attached-lifecycle")
	if err := os.Rename(oldPath, attachedPath); err != nil {
		t.Fatal(err)
	}
	h.mustRun("tries", "attach", id, attachedPath)
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if len(rows) != 1 || rows[0].Identity.ID != id || rows[0].Location.CurrentPath != attachedPath {
		t.Fatalf("attached Try did not retain identity: %+v", rows)
	}

	h.mustRun("tries", "mark", id, "--remove", "API", "--note=")
	rows = decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if got := strings.Join(rows[0].Metadata.Tags, ","); got != "prototype" || rows[0].Metadata.Note != "" {
		t.Fatalf("cleared metadata = %+v", rows[0].Metadata)
	}
	if out := h.mustRun("tries", "--help"); !strings.Contains(out, "archive") || !strings.Contains(out, "graduate") {
		t.Fatalf("tries help does not register lifecycle commands:\n%s", out)
	}
}

func TestTriesGraduateUsesSharedBehaviorAndLegacyTryGrammar(t *testing.T) {
	h := newHarness(t)
	// The singular command must continue treating a management-looking word as a
	// positional experiment name.
	h.mustRun("try", "archive", "--no-git")
	if entries, err := os.ReadDir(filepath.Join(h.home, "tries")); err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-archive") {
		t.Fatalf("dev try archive grammar changed: %v, %v", entries, err)
	}

	out := h.mustRun("tries", "graduate", "archive", "--category", "Labs")
	if !strings.Contains(out, "is now a project") || !strings.Contains(out, "dev start archive") {
		t.Fatalf("nested graduate output differs from legacy behavior:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(h.scanRoot, "Labs", "archive")); err != nil {
		t.Fatalf("nested graduate destination: %v", err)
	}
}

type repoJSONFixture struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Size *struct {
		CheckoutBytes int64  `json:"checkout_bytes"`
		OwnedBytes    int64  `json:"owned_bytes"`
		TotalBytes    *int64 `json:"total_bytes"`
		Complete      bool   `json:"complete"`
		Cached        bool   `json:"cached"`
	} `json:"size"`
	Recovery struct {
		NoRemote          bool     `json:"no_remote"`
		MultipleRemotes   bool     `json:"multiple_remotes"`
		MultipleUpstreams bool     `json:"multiple_upstreams"`
		LocalOnlyBranches []string `json:"local_only_branches"`
		UpstreamRemotes   []string `json:"upstream_remotes"`
	} `json:"recovery"`
	Asset *struct {
		ID    string   `json:"id"`
		Kind  string   `json:"kind"`
		Phase string   `json:"phase"`
		Tags  []string `json:"tags"`
		Note  string   `json:"note"`
	} `json:"asset"`
}

func decodeRepoJSON(t *testing.T, body string) []repoJSONFixture {
	t.Helper()
	var rows []repoJSONFixture
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("invalid repo JSON: %v\n%s", err, body)
	}
	return rows
}

func findRepoJSON(t *testing.T, rows []repoJSONFixture, name string) repoJSONFixture {
	t.Helper()
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("repository %q not found in %+v", name, rows)
	return repoJSONFixture{}
}

func TestRepoMarkAndTrySuppressionWithGraduatedReappearance(t *testing.T) {
	h := newHarness(t)
	enableTryScanning(t, h)

	h.mustRun("repo", "mark", "demo", "--add", "API", "--add", "api", "--add", "backend", "--note", "primary repo")
	rows := decodeRepoJSON(t, h.mustRun("repo", "list", "--json"))
	demo := findRepoJSON(t, rows, "demo")
	if demo.Kind != "repository" || demo.Asset == nil || strings.Join(demo.Asset.Tags, ",") != "api,backend" || demo.Asset.Note != "primary repo" {
		t.Fatalf("marked repository JSON = %+v", demo)
	}
	h.mustRun("repo", "mark", "demo", "--remove", "API", "--note=")
	demo = findRepoJSON(t, decodeRepoJSON(t, h.mustRun("repo", "list", "--json")), "demo")
	if demo.Asset == nil || strings.Join(demo.Asset.Tags, ",") != "backend" || demo.Asset.Note != "" {
		t.Fatalf("repository clear-note result = %+v", demo)
	}

	h.mustRun("try", "hidden-repo")
	tries := decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	tryID := tries[0].Identity.ID
	defaultRows := decodeRepoJSON(t, h.mustRun("repo", "list", "--json"))
	for _, row := range defaultRows {
		if strings.Contains(row.Name, "hidden-repo") || row.Kind == "try" {
			t.Fatalf("active Try leaked into default repo list: %+v", defaultRows)
		}
	}
	if plain := h.mustRun("repo", "list", "--include-tries"); !strings.Contains(plain, "KIND") || !strings.Contains(plain, "try") {
		t.Fatalf("plain --include-tries did not identify Try rows:\n%s", plain)
	}
	included := decodeRepoJSON(t, h.mustRun("repo", "list", "--include-tries", "--json"))
	var sawTry bool
	for _, row := range included {
		if row.Asset != nil && row.Asset.ID == tryID {
			sawTry = row.Kind == "try" && row.Asset.Kind == "try"
		}
	}
	if !sawTry {
		t.Fatalf("--include-tries did not mark the Try: %+v", included)
	}

	h.mustRun("tries", "deprecate", tryID)
	for _, row := range decodeRepoJSON(t, h.mustRun("repo", "list", "--json")) {
		if row.Kind == "try" {
			t.Fatalf("deprecated Try leaked into default repo list: %+v", row)
		}
	}
	h.mustRun("tries", "graduate", tryID)
	graduated := findRepoJSON(t, decodeRepoJSON(t, h.mustRun("repo", "list", "--json")), "hidden-repo")
	if graduated.Kind != "repository" || graduated.Asset == nil || graduated.Asset.Phase != "graduated" {
		t.Fatalf("graduated repository did not reappear: %+v", graduated)
	}
}

func TestRemoteJSONIdentifiesCatalogedTryForCachedAndLiveMatching(t *testing.T) {
	h := newHarness(t)
	enableTryScanning(t, h)
	h.mustRun("try", "remote-try")
	tries := decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	path := tries[0].Location.CurrentPath
	command := exec.Command("git", "remote", "add", "origin", "https://github.com/owner/remote-try.git")
	command.Dir = path
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, output)
	}
	// A reconcile refreshes both raw origin provenance and normalized identity.
	h.mustRun("tries", "list", "--all")
	cachePath := filepath.Join(config.CacheHome(), "dev", "remotes.json")
	if err := forge.SaveCache(cachePath, []forge.RemoteRepo{{
		Forge: forge.GitHub, Name: "remote-try", FullName: "owner/remote-try",
		URL: "https://github.com/owner/remote-try", CloneURL: "https://github.com/owner/remote-try.git",
	}}); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("repo", "remote", "--cached", "--json")
	var rows []struct {
		LocalPath string `json:"local_path"`
		LocalKind string `json:"local_kind"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("remote JSON = %v, %s", err, out)
	}
	if rows[0].LocalPath == "" || rows[0].LocalKind != "try" {
		t.Fatalf("cached remote did not identify local Try: %+v", rows[0])
	}
}

func TestRepoAndTrySizeFlagsExposeOwnedLogicalBytesAndCache(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.repo.Root, "payload.bin"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	withoutSize := findRepoJSON(t, decodeRepoJSON(t, h.mustRun("repo", "list", "--json")), "demo")
	if withoutSize.Size != nil {
		t.Fatalf("repo size was measured without --sizes: %+v", withoutSize.Size)
	}
	body := h.mustRun("repo", "list", "--sizes", "--json")
	repository := findRepoJSON(t, decodeRepoJSON(t, body), "demo")
	if repository.Size == nil || repository.Size.OwnedBytes < 2048 || repository.Size.TotalBytes == nil ||
		!repository.Size.Complete || repository.Size.Cached {
		t.Fatalf("fresh repo size = %+v\n%s", repository.Size, body)
	}
	repository = findRepoJSON(t, decodeRepoJSON(t, h.mustRun("repo", "list", "--sizes", "--json")), "demo")
	if repository.Size == nil || !repository.Size.Cached {
		t.Fatalf("second repo size did not use cache: %+v", repository.Size)
	}
	if plain := h.mustRun("repo", "list", "--sizes"); !strings.Contains(plain, "SIZE") || !strings.Contains(plain, "KiB") {
		t.Fatalf("plain repo sizes missing:\n%s", plain)
	}

	h.mustRun("try", "sized-plain", "--no-git")
	entries, err := os.ReadDir(filepath.Join(h.home, "tries"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("Try entries = %v, %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(h.home, "tries", entries[0].Name(), "payload.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	tries := decodeTriesJSON(t, h.mustRun("tries", "list", "--refresh-sizes", "--json"))
	if len(tries) != 1 || tries[0].Size == nil || tries[0].Size.OwnedBytes != 4096 ||
		tries[0].Size.TotalBytes == nil || tries[0].Size.Cached {
		t.Fatalf("Try size = %+v", tries)
	}
}

func TestRepoListLocatesNoRemoteLocalOnlyAndMultipleUpstreams(t *testing.T) {
	h := newHarness(t)
	body := h.mustRun("repo", "list", "--no-remote", "--json")
	if !strings.Contains(body, `"remotes": []`) || !strings.Contains(body, `"branches": [`) {
		t.Fatalf("empty recovery collections are not stable arrays:\n%s", body)
	}
	rows := decodeRepoJSON(t, body)
	if len(rows) != 1 || !rows[0].Recovery.NoRemote ||
		strings.Join(rows[0].Recovery.LocalOnlyBranches, ",") != "main" {
		t.Fatalf("no-remote inventory = %+v", rows)
	}
	if plain := h.mustRun("repo", "list"); !strings.Contains(plain, "none · local:1") {
		t.Fatalf("plain repo list hides recovery risk:\n%s", plain)
	}

	remoteRoot := filepath.Join(h.home, "remotes")
	if err := os.MkdirAll(remoteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(remoteRoot, "origin.git")
	upstream := filepath.Join(remoteRoot, "upstream.git")
	h.repo.GitIn(remoteRoot, "init", "--bare", "--initial-branch=main", origin)
	h.repo.GitIn(remoteRoot, "init", "--bare", "--initial-branch=main", upstream)
	h.repo.Git("remote", "add", "origin", origin)
	h.repo.Git("remote", "add", "upstream", upstream)
	h.repo.Git("push", "-u", "origin", "main")
	h.repo.Git("branch", "local-only", "main")
	h.repo.Git("branch", "release", "main")
	h.repo.Git("push", "-u", "upstream", "release")

	if rows := decodeRepoJSON(t, h.mustRun("repo", "list", "--no-remote", "--json")); len(rows) != 0 {
		t.Fatalf("repo with remotes matched --no-remote: %+v", rows)
	}
	rows = decodeRepoJSON(t, h.mustRun("repo", "list", "--multiple-remotes", "--multiple-upstreams", "--local-only", "--json"))
	if len(rows) != 1 || !rows[0].Recovery.MultipleRemotes || !rows[0].Recovery.MultipleUpstreams ||
		strings.Join(rows[0].Recovery.UpstreamRemotes, ",") != "origin,upstream" ||
		strings.Join(rows[0].Recovery.LocalOnlyBranches, ",") != "local-only" {
		t.Fatalf("multi-upstream inventory = %+v", rows)
	}
}

func TestCorruptCatalogKeepsReadablePartialOutputAndAvoidsSuppression(t *testing.T) {
	h := newHarness(t)
	enableTryScanning(t, h)
	h.mustRun("try", "partial")
	assetsDir := filepath.Join(h.home, "state", "assets")
	if err := os.WriteFile(filepath.Join(assetsDir, "broken.toml"), []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := h.run("tries", "list", "--all", "--json")
	if err != nil {
		t.Fatalf("partial tries list: %v\n%s", err, errOut)
	}
	if rows := decodeTriesJSON(t, out); len(rows) == 0 {
		t.Fatal("valid Try was hidden by a corrupt record")
	}
	if !strings.Contains(errOut, "broken.toml") {
		t.Fatalf("corrupt catalog warning missing: %q", errOut)
	}

	out, errOut, err = h.run("repo", "list", "--json")
	if err != nil {
		t.Fatalf("partial repo list: %v\n%s", err, errOut)
	}
	rows := decodeRepoJSON(t, out)
	var visibleTry bool
	for _, row := range rows {
		if strings.Contains(row.Name, "partial") {
			visibleTry = true
		}
	}
	if !visibleTry {
		t.Fatalf("incomplete catalog silently suppressed ambiguous Try row: %+v", rows)
	}
	if !strings.Contains(errOut, "broken.toml") {
		t.Fatalf("repo catalog warning missing: %q", errOut)
	}
}

func TestMarkCommandsRequireAnExplicitMutation(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "mark-target", "--no-git")
	rows := decodeTriesJSON(t, h.mustRun("tries", "list", "--json"))
	if len(rows) != 1 {
		t.Fatalf("Try setup = %+v", rows)
	}
	if _, _, err := h.run("tries", "mark", rows[0].Identity.ID); err == nil ||
		!strings.Contains(err.Error(), "mark requires") {
		t.Fatalf("tries mark without a mutation = %v", err)
	}

	assetsBefore, err := filepath.Glob(filepath.Join(h.home, "state", "assets", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.run("repo", "mark", "demo"); err == nil ||
		!strings.Contains(err.Error(), "mark requires") {
		t.Fatalf("repo mark without a mutation = %v", err)
	}
	assetsAfter, err := filepath.Glob(filepath.Join(h.home, "state", "assets", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(assetsAfter) != len(assetsBefore) {
		t.Fatalf("no-op repo mark created catalog state: before=%d after=%d", len(assetsBefore), len(assetsAfter))
	}
}

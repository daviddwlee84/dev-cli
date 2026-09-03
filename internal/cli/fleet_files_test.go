package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/localfiles"
)

const (
	fakeTargetMachine = "33333333-3333-4333-8333-333333333333"
	fakeWrongMachine  = "44444444-4444-4444-8444-444444444444"
	fleetFilesSecret  = "TOKEN=fleet-files-secret-value"
)

type fleetFilesHarness struct {
	t            *testing.T
	sourceHome   string
	remoteHome   string
	sourceRepo   string
	targetRepo   string
	sourceConfig string
	targetConfig string
	remotes      string
	log          string
	binDir       string
}

func newFleetFilesHarness(t *testing.T) *fleetFilesHarness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake-SSH wrapper; Windows transport is covered by native domain tests")
	}
	root := t.TempDir()
	sourceHome := filepath.Join(root, "source-home")
	remoteHome := filepath.Join(root, "remote-home")
	sourceRoot := filepath.Join(root, "source-repos")
	targetRoot := filepath.Join(root, "target-repos")
	remote := filepath.Join(root, "git-remote.git")
	sourceRepo := filepath.Join(sourceRoot, "demo")
	targetRepo := filepath.Join(targetRoot, "demo")
	for _, directory := range []string{sourceHome, remoteHome, sourceRoot, targetRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fleetFilesGit(t, root, "init", "--bare", remote)
	fleetFilesGit(t, root, "init", "-b", "main", sourceRepo)
	fleetFilesGit(t, sourceRepo, "config", "user.email", "test@example.test")
	fleetFilesGit(t, sourceRepo, "config", "user.name", "Test")
	fleetFilesWrite(t, filepath.Join(sourceRepo, ".gitignore"), ".env\n")
	fleetFilesWrite(t, filepath.Join(sourceRepo, ".dev-cli", "config.toml"), "version = 1\n[local_files]\ninclude = [\".env\"]\n")
	fleetFilesWrite(t, filepath.Join(sourceRepo, "README.md"), "tracked\n")
	fleetFilesGit(t, sourceRepo, "add", ".gitignore", ".dev-cli/config.toml", "README.md")
	fleetFilesGit(t, sourceRepo, "commit", "-m", "initial")
	fleetFilesGit(t, sourceRepo, "remote", "add", "origin", remote)
	fleetFilesGit(t, sourceRepo, "push", "-u", "origin", "main")
	fleetFilesGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	fleetFilesGit(t, root, "clone", remote, targetRepo)
	const identityURL = "https://example.test/acme/fleet-files.git"
	fleetFilesGit(t, sourceRepo, "remote", "set-url", "origin", identityURL)
	fleetFilesGit(t, targetRepo, "remote", "set-url", "origin", identityURL)
	fleetFilesWrite(t, filepath.Join(sourceRepo, ".env"), fleetFilesSecret+"\n")

	sourceConfig := filepath.Join(root, "source-config.toml")
	targetConfig := filepath.Join(root, "target-config.toml")
	fleetFilesWrite(t, sourceConfig, fleetFilesConfig(sourceRepo, filepath.Join(sourceHome, "state")))
	fleetFilesWrite(t, targetConfig, fleetFilesConfig(targetRepo, filepath.Join(remoteHome, "state")))
	writeMachineIdentity(t, remoteHome, fakeTargetMachine)

	remotes := filepath.Join(root, "remotes.toml")
	writeFleetFilesRemotes(t, remotes, fakeTargetMachine)
	log := filepath.Join(root, "ssh.log")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\nexec \"$DEV_FAKE_SSH_TEST_BINARY\" -test.run '^TestFleetFilesFakeSSHProcess$' -- \"$@\"\n"
	fleetFilesWriteMode(t, filepath.Join(binDir, "ssh"), ssh, 0o755)
	t.Setenv("HOME", sourceHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sourceHome, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(sourceHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(sourceHome, ".cache"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEV_FAKE_SSH", "1")
	t.Setenv("DEV_FAKE_SSH_TEST_BINARY", testBinary)
	t.Setenv("DEV_FAKE_SSH_REMOTE_HOME", remoteHome)
	t.Setenv("DEV_FAKE_SSH_TARGET_CONFIG", targetConfig)
	t.Setenv("DEV_FAKE_SSH_LOG", log)
	return &fleetFilesHarness{
		t: t, sourceHome: sourceHome, remoteHome: remoteHome,
		sourceRepo: sourceRepo, targetRepo: targetRepo,
		sourceConfig: sourceConfig, targetConfig: targetConfig,
		remotes: remotes, log: log, binDir: binDir,
	}
}

func TestFleetFilesFakeSSHProcess(t *testing.T) {
	if os.Getenv("DEV_FAKE_SSH") != "1" {
		return
	}
	remoteCommand := os.Args[len(os.Args)-1]
	helper := ""
	for _, candidate := range []string{"_capability", "_files-plan", "_files-apply"} {
		if strings.Contains(remoteCommand, "'"+candidate+"'") {
			helper = candidate
			break
		}
	}
	if helper == "" {
		fmt.Fprintln(os.Stderr, "unsupported fake SSH command")
		os.Exit(127)
	}
	log, err := os.OpenFile(os.Getenv("DEV_FAKE_SSH_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(log, helper)
		_ = log.Close()
	}
	remoteHome := os.Getenv("DEV_FAKE_SSH_REMOTE_HOME")
	_ = os.Setenv("HOME", remoteHome)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(remoteHome, ".config"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(remoteHome, ".local", "share"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(remoteHome, ".cache"))
	root := cli.NewRootCommandWithIO(os.Stdout, os.Stderr)
	root.SetArgs([]string{"--config", os.Getenv("DEV_FAKE_SSH_TARGET_CONFIG"), "fleet", helper})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "remote protocol failed")
		os.Exit(2)
	}
	os.Exit(0)
}

func TestFleetMachineIDReportsConfiguredPinState(t *testing.T) {
	h := newFleetFilesHarness(t)
	type identityReport struct {
		SchemaVersion       int     `json:"schema_version"`
		Host                string  `json:"host"`
		MachineID           string  `json:"machine_id"`
		ConfiguredMachineID *string `json:"configured_machine_id"`
		PinState            string  `json:"pin_state"`
		Supported           bool    `json:"supported"`
	}
	assertState := func(want string) identityReport {
		t.Helper()
		out, errOut, err := h.run("fleet", "machine-id", "target", "--json")
		if err != nil {
			t.Fatalf("machine-id: %v\nstderr=%s", err, errOut)
		}
		var report identityReport
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("decode machine-id: %v\n%s", err, out)
		}
		if report.SchemaVersion != 1 || report.Host != "target" || report.MachineID != fakeTargetMachine || report.PinState != want || !report.Supported {
			t.Fatalf("machine-id report = %+v, want pin %s", report, want)
		}
		return report
	}
	if report := assertState("match"); report.ConfiguredMachineID == nil || *report.ConfiguredMachineID != fakeTargetMachine {
		t.Fatalf("configured pin = %v", report.ConfiguredMachineID)
	}

	writeFleetFilesRemotes(t, h.remotes, fakeWrongMachine)
	if report := assertState("mismatch"); report.ConfiguredMachineID == nil || *report.ConfiguredMachineID != fakeWrongMachine {
		t.Fatalf("mismatched configured pin = %v", report.ConfiguredMachineID)
	}

	writeFleetFilesRemotes(t, h.remotes, "")
	if report := assertState("unpinned"); report.ConfiguredMachineID != nil {
		t.Fatalf("unpinned report exposed configured value %v", report.ConfiguredMachineID)
	}
	out, errOut, err := h.run("fleet", "machine-id", "target")
	if err != nil || !strings.Contains(out, fakeTargetMachine) || !strings.Contains(out, "unpinned") || !strings.Contains(out, "machine_id =") {
		t.Fatalf("human machine-id output = %q, stderr=%q, err=%v", out, errOut, err)
	}
}

func TestFleetFilesFakeSSHPlanApplyAndRedaction(t *testing.T) {
	h := newFleetFilesHarness(t)
	planOut, planErr, err := h.run("fleet", "files", "demo", "--to", "target", "--json")
	if err != nil {
		t.Fatalf("plan: %v\nstderr=%s", err, planErr)
	}
	var plan localfiles.Report
	if err := json.Unmarshal([]byte(planOut), &plan); err != nil || len(plan.Files) != 1 || plan.Files[0].State != localfiles.StateReady {
		t.Fatalf("plan report = %+v, %v\n%s", plan, err, planOut)
	}
	if _, err := os.Lstat(filepath.Join(h.targetRepo, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report-only plan mutated target: %v", err)
	}

	applyOut, applyErr, err := h.run("fleet", "files", "demo", "--to", "target", "--apply", "--yes", "--json")
	if err != nil {
		t.Fatalf("apply: %v\nstderr=%s", err, applyErr)
	}
	var result localfiles.Report
	if err := json.Unmarshal([]byte(applyOut), &result); err != nil || len(result.Files) != 1 || result.Files[0].State != localfiles.StateCreated {
		t.Fatalf("apply report = %+v, %v\n%s", result, err, applyOut)
	}
	body, err := os.ReadFile(filepath.Join(h.targetRepo, ".env"))
	if err != nil || string(body) != fleetFilesSecret+"\n" {
		t.Fatalf("target content = %q, %v", body, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(h.targetRepo, ".env"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("target mode = %v, %v", info.Mode().Perm(), err)
		}
	}
	combined := planOut + planErr + applyOut + applyErr
	if strings.Contains(combined, fleetFilesSecret) || strings.Contains(combined, "sha256:") || strings.Contains(combined, "content_base64") {
		t.Fatalf("public output leaked private protocol data: %s", combined)
	}
	log, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatal(err)
	}
	sequence := strings.Fields(string(log))
	want := []string{"_capability", "_files-plan", "_capability", "_files-plan", "_files-apply"}
	if strings.Join(sequence, " ") != strings.Join(want, " ") {
		t.Fatalf("protocol order = %v, want %v", sequence, want)
	}
	assertNoSecretBelow(t, filepath.Join(h.sourceHome, ".cache"), fleetFilesSecret)
	assertNoSecretBelow(t, filepath.Join(h.remoteHome, ".cache"), fleetFilesSecret)
}

func TestFleetFilesYesDoesNotImplyReplace(t *testing.T) {
	h := newFleetFilesHarness(t)
	fleetFilesWrite(t, filepath.Join(h.targetRepo, ".env"), "target-owned\n")
	out, errOut, err := h.run("fleet", "files", "demo", "--to", "target", "--apply", "--yes", "--json")
	if !errors.Is(err, localfiles.ErrPlanBlocked) {
		t.Fatalf("conflict without --replace = %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	body, readErr := os.ReadFile(filepath.Join(h.targetRepo, ".env"))
	if readErr != nil || string(body) != "target-owned\n" {
		t.Fatalf("--yes replaced target content: %q, %v", body, readErr)
	}
}

func TestFleetFilesPreservesStableTargetPlanError(t *testing.T) {
	h := newFleetFilesHarness(t)
	fleetFilesWrite(t, filepath.Join(h.targetRepo, "target-drift.txt"), "drift\n")
	fleetFilesGit(t, h.targetRepo, "add", "target-drift.txt")
	fleetFilesGit(t, h.targetRepo, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "target drift")
	out, errOut, err := h.run("fleet", "files", "demo", "--to", "target", "--json")
	var target *localfiles.TargetError
	if !errors.As(err, &target) || target.Code != localfiles.TargetStale {
		t.Fatalf("target plan error = %v (typed=%+v)\nstdout=%s\nstderr=%s", err, target, out, errOut)
	}
	if errors.Is(err, localfiles.ErrNoRemoteDev) {
		t.Fatal("stable target state was collapsed into protocol incompatibility")
	}
}

func TestFleetFilesRequiresExactlyOneTarget(t *testing.T) {
	h := newFleetFilesHarness(t)
	for _, args := range [][]string{
		{"fleet", "files", "demo"},
		{"fleet", "files", "demo", "--to", "target", "--to", "target"},
	} {
		_, _, err := h.run(args...)
		if err == nil || !strings.Contains(err.Error(), "exactly one --to") {
			t.Fatalf("args %v error = %v", args, err)
		}
	}
}

func TestFleetFilesPinMismatchAndNoDevFailBeforePlan(t *testing.T) {
	h := newFleetFilesHarness(t)
	writeFleetFilesRemotes(t, h.remotes, fakeWrongMachine)
	out, errOut, err := h.run("fleet", "files", "demo", "--to", "target", "--apply", "--yes")
	if !errors.Is(err, localfiles.ErrMachinePin) {
		t.Fatalf("pin mismatch = %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	log, _ := os.ReadFile(h.log)
	if strings.TrimSpace(string(log)) != "_capability" {
		t.Fatalf("pin mismatch sent a later request: %q", log)
	}
	if strings.Contains(out+errOut+err.Error(), fleetFilesSecret) {
		t.Fatal("pin mismatch leaked source content")
	}

	noDevDir := filepath.Join(filepath.Dir(h.binDir), "no-dev-bin")
	if err := os.MkdirAll(noDevDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fleetFilesWriteMode(t, filepath.Join(noDevDir, "ssh"), "#!/bin/sh\nexit 127\n", 0o755)
	t.Setenv("PATH", noDevDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFleetFilesRemotes(t, h.remotes, fakeTargetMachine)
	_, _, err = h.run("fleet", "files", "demo", "--to", "target", "--file", ".env")
	if !errors.Is(err, localfiles.ErrNoRemoteDev) {
		t.Fatalf("no-dev error = %v", err)
	}
}

func (h *fleetFilesHarness) run(args ...string) (string, string, error) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errOut)
	root.SetArgs(append([]string{"--config", h.sourceConfig, "--remotes", h.remotes}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func fleetFilesConfig(repoPath, statePath string) string {
	return fmt.Sprintf("[paths]\nscan_roots = []\nrepo_paths = [%q]\nstate_dir = %q\n[runtime]\nbackend = \"none\"\n", filepath.ToSlash(repoPath), filepath.ToSlash(statePath))
}

func writeFleetFilesRemotes(t *testing.T, path, machineID string) {
	t.Helper()
	body := fmt.Sprintf(`schema_version = 1
[[hosts]]
name = "target"
machine_id = %q
ssh_alias = "fake-target"
`, machineID)
	fleetFilesWriteMode(t, path, body, 0o600)
}

func writeMachineIdentity(t *testing.T, home, machineID string) {
	t.Helper()
	path := filepath.Join(home, ".local", "share", "dev", "machine", "v1", "identity.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"schema_version\":1,\"machine_id\":%q}\n", machineID)
	fleetFilesWriteMode(t, path, body, 0o600)
}

func fleetFilesGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func fleetFilesWrite(t *testing.T, path, body string) {
	t.Helper()
	fleetFilesWriteMode(t, path, body, 0o644)
}

func fleetFilesWriteMode(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func assertNoSecretBelow(t *testing.T, root, secret string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), secret) {
			t.Errorf("cache file %s contains transferred content", path)
		}
		return nil
	})
}

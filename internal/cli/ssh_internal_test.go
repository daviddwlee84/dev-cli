package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshCLIFixture struct {
	t           *testing.T
	home        string
	configPath  string
	remotesPath string
	runner      *sshCLIRunner
}

func TestFleetEditorSubprocessHelper(t *testing.T) {
	if os.Getenv("DEV_FLEET_EDITOR_TEST_HELPER") != "1" {
		return
	}
	marker := os.Getenv("DEV_FLEET_EDITOR_TEST_MARKER")
	release := os.Getenv("DEV_FLEET_EDITOR_TEST_RELEASE")
	if marker == "" || release == "" {
		t.Fatal("fleet editor helper lacks marker paths")
	}
	if err := os.WriteFile(marker, []byte("editing"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(release); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for fleet editor release")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type sshCLIRunner struct {
	mu            sync.Mutex
	home          string
	calls         []sshhost.RunRequest
	deadlines     []bool
	blockDisplay  string
	ordinaryReady bool
	exactReady    bool
	installerExit int
	effectiveExit int
	effectiveErr  string
	publicLine    []byte
}

func newSSHCLIFixture(t *testing.T) *sshCLIFixture {
	t.Helper()
	home := cliTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("[runtime]\nbackend = \"none\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &sshCLIRunner{
		home: home, ordinaryReady: true, exactReady: true,
		publicLine: sshCLITestPublicLine(0x42, "cli-test-key"),
	}
	return &sshCLIFixture{
		t: t, home: home, configPath: configPath,
		remotesPath: filepath.Join(home, ".config", "dev", "remotes.toml"),
		runner:      runner,
	}
}

func (fixture *sshCLIFixture) run(args ...string) (string, string, error) {
	return fixture.runInteractive("", false, args...)
}

func (fixture *sshCLIFixture) runInteractive(input string, interactive bool, args ...string) (string, string, error) {
	fixture.t.Helper()
	return fixture.runContext(context.Background(), input, interactive, args...)
}

func (fixture *sshCLIFixture) runContext(ctx context.Context, input string, interactive bool, args ...string) (string, string, error) {
	fixture.t.Helper()
	var out, errOut bytes.Buffer
	app := &App{
		In: strings.NewReader(input), Out: &out, Err: &errOut,
		interactiveCheck: func() bool { return interactive },
		sshHostRunner:    fixture.runner,
	}
	root := newRootCommand(app)
	root.SetContext(ctx)
	root.SetArgs(append([]string{
		"--config", fixture.configPath,
		"--remotes", fixture.remotesPath,
		"--color", "never",
	}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func (fixture *sshCLIFixture) mustRun(args ...string) string {
	fixture.t.Helper()
	out, errOut, err := fixture.run(args...)
	if err != nil {
		fixture.t.Fatalf("dev %s: %v\nstderr: %s\nstdout: %s", strings.Join(args, " "), err, errOut, out)
	}
	return out
}

func (fixture *sshCLIFixture) initSSH() {
	fixture.t.Helper()
	fixture.mustRun("ssh", "init", "--apply", "--yes", "--json")
	fixture.runner.resetCalls()
}

func (fixture *sshCLIFixture) rootConfigPath() string {
	return filepath.Join(fixture.home, ".ssh", "config")
}

func (fixture *sshCLIFixture) appendRootConfig(body string) {
	fixture.t.Helper()
	current, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil {
		fixture.t.Fatal(err)
	}
	current = append(current, []byte(body)...)
	if err := os.WriteFile(fixture.rootConfigPath(), current, 0o600); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *sshCLIFixture) managedPath(alias string) string {
	return filepath.Join(fixture.home, ".ssh", "dev.d", alias+".conf")
}

func (fixture *sshCLIFixture) createManaged(alias, hostName string) {
	fixture.t.Helper()
	fixture.mustRun("ssh", "setup", alias, "--hostname", hostName, "--config-only", "--yes", "--json")
	fixture.runner.resetCalls()
}

func (runner *sshCLIRunner) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, request)
	_, hasDeadline := ctx.Deadline()
	runner.deadlines = append(runner.deadlines, hasDeadline)
	block := runner.blockDisplay == request.Display
	runner.mu.Unlock()
	if block {
		<-ctx.Done()
		return sshhost.RunResult{}, ctx.Err()
	}

	switch request.Name {
	case "ssh-keygen":
		if hasSSHArg(request.Args, "-y") {
			return sshhost.RunResult{Stdout: append(append([]byte(nil), runner.publicLine...), '\n')}, nil
		}
		path := sshArgValue(request.Args, "-f")
		if path == "" {
			return sshhost.RunResult{}, errors.New("fake ssh-keygen has no -f")
		}
		if err := os.WriteFile(path, []byte("PRIVATE-KEY-BYTES-MUST-NOT-LEAK\n"), 0o600); err != nil {
			return sshhost.RunResult{}, err
		}
		if err := os.WriteFile(path+".pub", append(append([]byte(nil), runner.publicLine...), '\n'), 0o600); err != nil {
			return sshhost.RunResult{}, err
		}
		return sshhost.RunResult{}, nil
	case "ssh":
		if hasSSHArg(request.Args, "-G") {
			if runner.effectiveExit != 0 {
				return sshhost.RunResult{ExitCode: runner.effectiveExit, Stderr: []byte(runner.effectiveErr)}, nil
			}
			alias := sshArgValue(request.Args, "-G")
			return sshhost.RunResult{Stdout: runner.effective(alias)}, nil
		}
		switch request.Display {
		case "ssh Windows administrator probe":
			return sshhost.RunResult{Stdout: []byte("standard\n")}, nil
		case "ssh public-key installer":
			return sshhost.RunResult{ExitCode: runner.installerExit}, nil
		case "ssh authentication proof":
			if hasSSHArg(request.Args, "-i") {
				if runner.exactReady {
					return sshhost.RunResult{}, nil
				}
				return sshhost.RunResult{ExitCode: 1}, nil
			}
			if runner.ordinaryReady {
				return sshhost.RunResult{}, nil
			}
			return sshhost.RunResult{ExitCode: 1}, nil
		case "ssh fresh authentication probe":
			if runner.ordinaryReady {
				return sshhost.RunResult{}, nil
			}
			return sshhost.RunResult{ExitCode: 255}, nil
		default:
			return sshhost.RunResult{}, nil
		}
	default:
		return sshhost.RunResult{}, fmt.Errorf("unexpected runner command %q", request.Name)
	}
}

func (runner *sshCLIRunner) effective(alias string) []byte {
	definition := sshhost.ManagedDefinition{Alias: alias, HostName: alias + ".example", User: "tester", Port: 22}
	managedPath := filepath.Join(runner.home, ".ssh", "dev.d", alias+".conf")
	if content, err := os.ReadFile(managedPath); err == nil {
		if managed, parseErr := sshhost.ParseManaged(content); parseErr == nil {
			definition = managed
		}
	}
	if definition.User == "" {
		definition.User = "tester"
	}
	if definition.Port == 0 {
		definition.Port = 22
	}
	var output strings.Builder
	fmt.Fprintf(&output, "hostname %s\n", definition.HostName)
	fmt.Fprintf(&output, "user %s\n", definition.User)
	fmt.Fprintf(&output, "port %d\n", definition.Port)
	if definition.ProxyJump == "" {
		fmt.Fprintln(&output, "proxyjump none")
	} else {
		fmt.Fprintf(&output, "proxyjump %s\n", definition.ProxyJump)
	}
	if definition.IdentityFile != "" {
		fmt.Fprintf(&output, "identityfile %s\n", definition.IdentityFile)
	}
	identitiesOnly := "no"
	if definition.IdentitiesOnly != nil && *definition.IdentitiesOnly {
		identitiesOnly = "yes"
	}
	fmt.Fprintf(&output, "identitiesonly %s\n", identitiesOnly)
	fmt.Fprintln(&output, "proxycommand none")
	return []byte(output.String())
}

func (runner *sshCLIRunner) resetCalls() {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = nil
	runner.deadlines = nil
}

func (runner *sshCLIRunner) callCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return len(runner.calls)
}

func (runner *sshCLIRunner) callSnapshot() []sshhost.RunRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]sshhost.RunRequest(nil), runner.calls...)
}

func (runner *sshCLIRunner) deadlineSnapshot() []bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]bool(nil), runner.deadlines...)
}

func hasSSHArg(args []string, key string) bool {
	for _, arg := range args {
		if arg == key {
			return true
		}
	}
	return false
}

func sshArgValue(args []string, key string) string {
	for index := range args {
		if args[index] == key && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func sshCLITestPublicLine(seed byte, comment string) []byte {
	wire := func(value []byte) []byte {
		encoded := make([]byte, 4+len(value))
		binary.BigEndian.PutUint32(encoded, uint32(len(value)))
		copy(encoded[4:], value)
		return encoded
	}
	algorithm := []byte("ssh-ed25519")
	blob := append(wire(algorithm), wire(bytes.Repeat([]byte{seed}, 32))...)
	return []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment)
}

func assertOneSSHJSON(t *testing.T, output string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("JSON stdout contains another value or trailing syntax: %v\n%s", err, output)
	}
	if document["schema_version"] != float64(sshCLISchemaVersion) {
		t.Fatalf("schema_version = %#v", document["schema_version"])
	}
	if kind, _ := document["kind"].(string); !strings.HasPrefix(kind, "ssh_") {
		t.Fatalf("kind = %#v", document["kind"])
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("JSON contains ANSI: %q", output)
	}
	return document
}

func TestSSHCommandFamilyHelpFlagsAndUnknownSubcommand(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	out, _, err := fixture.run("ssh")
	if err != nil || !strings.Contains(out, "Available Commands:") || !strings.Contains(out, "TL;DR: OpenSSH stays the source of truth") {
		t.Fatalf("bare ssh family output/error = %q / %v", out, err)
	}
	if _, _, err := fixture.run("ssh", "definitely-not-a-command"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown SSH subcommand error = %v", err)
	}
	help, _, err := fixture.run("help", "ssh")
	if err != nil || !strings.Contains(help, "# SSH hosts") {
		t.Fatalf("dev help ssh = %v\n%s", err, help)
	}
	for _, value := range []byte(out[strings.Index(out, "TL;DR:"):]) {
		if value >= 0x80 {
			t.Fatalf("SSH TLDR contains non-ASCII byte %#x", value)
		}
	}

	app := &App{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}
	root := newRootCommand(app)
	setup, _, err := root.Find([]string{"ssh", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"hostname", "user", "port", "proxy-jump", "identity-file", "identities-only",
		"config-only", "key", "generate-key", "key-path", "comment", "no-passphrase",
		"target-os", "hop-os", "install-on-working-jump", "windows-admin-authorized-keys",
		"fleet", "fleet-name", "dry-run", "yes", "json",
	} {
		if setup.Flags().Lookup(name) == nil {
			t.Errorf("setup lacks --%s", name)
		}
	}
	for _, path := range [][]string{{"ssh", "list"}, {"doctor"}} {
		command, _, findErr := root.Find(path)
		if findErr != nil || !passiveCommandSkipsNudge(command) {
			t.Errorf("%v may trigger an unrelated release network check: %v", path, findErr)
		}
	}
	for command, flags := range map[string][]string{
		"init":   {"apply", "yes", "json"},
		"list":   {"json", "format"},
		"show":   {"json"},
		"probe":  {"json"},
		"remove": {"fleet", "dry-run", "yes", "json"},
	} {
		child, _, findErr := root.Find([]string{"ssh", command})
		if findErr != nil {
			t.Fatal(findErr)
		}
		for _, flag := range flags {
			if child.Flags().Lookup(flag) == nil {
				t.Errorf("ssh %s lacks --%s", command, flag)
			}
		}
	}
}

func TestSSHJSONSyntaxErrorsStayEmptyAndOperationalErrorsEmitOneObject(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	for _, args := range [][]string{
		{"ssh", "list", "--json", "--format", "tsv"},
		{"ssh", "setup", "lab", "--json", "--key", "one", "--generate-key"},
		{"ssh", "show", "--json"},
	} {
		out, _, err := fixture.run(args...)
		if err == nil || out != "" {
			t.Fatalf("syntax invocation %v returned err=%v stdout=%q", args, err, out)
		}
	}
	fixture.initSSH()
	out, _, err := fixture.run("ssh", "show", "missing", "--json")
	if err == nil {
		t.Fatal("unknown show alias returned success")
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "not_found" || document["error_code"] != "not_found" {
		t.Fatalf("unknown show document = %#v", document)
	}
}

func TestSSHInitReportsThenRequiresExplicitApplyAndConfirmation(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	out, errOut, err := fixture.run("ssh", "init", "--json")
	if err != nil || errOut != "" {
		t.Fatalf("init plan error/stderr = %v / %q", err, errOut)
	}
	document := assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_init_plan" || document["status"] != "planned" {
		t.Fatalf("init plan = %#v", document)
	}
	if _, err := os.Lstat(fixture.rootConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("report-only init wrote root config: %v", err)
	}

	out, _, err = fixture.run("ssh", "init", "--apply", "--json")
	if err == nil {
		t.Fatal("non-interactive init apply succeeded without --yes")
	}
	document = assertOneSSHJSON(t, out)
	if document["status"] != "confirmation_required" {
		t.Fatalf("confirmation result = %#v", document)
	}
	if _, err := os.Lstat(fixture.rootConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed init wrote root config: %v", err)
	}

	out = fixture.mustRun("ssh", "init", "--apply", "--yes", "--json")
	document = assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_init_result" || document["status"] != "changed" {
		t.Fatalf("init apply = %#v", document)
	}
	content, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "Include "+sshhost.ManagedInclude+"\n" {
		t.Fatalf("root config = %q", content)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("init invoked runner %d time(s)", fixture.runner.callCount())
	}
}

func TestIndexFleetFactsCaseFoldedAndSorted(t *testing.T) {
	factsByAlias := indexFleetFacts(fleet.Config{Hosts: []fleet.Host{
		{Name: "zeta", SSHAlias: "LAB", RemoteOS: fleet.RemoteOSWindows},
		{Name: "alpha", SSHAlias: "lab", RemoteOS: fleet.RemoteOSPOSIX},
		{Name: "unicode", SSHAlias: "ſerver", RemoteOS: fleet.RemoteOSPOSIX},
		{Name: "direct", Hostname: "direct.example", RemoteOS: fleet.RemoteOSPOSIX},
	}})
	facts := factsByAlias[foldFleetAlias("lab")]
	if len(factsByAlias) != 2 || len(facts) != 2 || len(factsByAlias[foldFleetAlias("server")]) != 1 {
		t.Fatalf("fleet facts index = %#v", factsByAlias)
	}
	if facts[0].Name != "alpha" || facts[1].Name != "zeta" {
		t.Fatalf("fleet facts order = %#v", facts)
	}
}

func TestSSHListStaticHumanTSVJSONFleetAndCompletion(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.createManaged("lab", "lab.example")
	if err := os.WriteFile(filepath.Join(fixture.home, ".ssh", "dynamic.conf"), []byte("Host dynamic\n    HostName dynamic.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.appendRootConfig("Host foreign\n    HostName foreign.example\nMatch exec COMMAND-LIKE-SECRET\n    Include dynamic.conf\n")
	if _, err := fleet.WriteManagedFragment(context.Background(), fixture.remotesPath, fleet.ManagedHost{
		Name: "lab-fleet", SSHAlias: "lab", RemoteOS: fleet.RemoteOSPOSIX,
	}, nil); err != nil {
		t.Fatal(err)
	}
	fixture.runner.resetCalls()

	out := fixture.mustRun("ssh", "list", "--json")
	document := assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_list" || document["complete"] != false {
		t.Fatalf("list document = %#v", document)
	}
	managedSourceOK := false
	if aliases, ok := document["aliases"].([]any); ok {
		for _, item := range aliases {
			alias, ok := item.(map[string]any)
			if !ok || alias["name"] != "lab" {
				continue
			}
			source, _ := alias["managed_source"].(string)
			sourceInfo, sourceErr := os.Stat(source)
			managedInfo, managedErr := os.Stat(fixture.managedPath("lab"))
			managedSourceOK = sourceErr == nil && managedErr == nil && os.SameFile(sourceInfo, managedInfo)
			break
		}
	}
	if !strings.Contains(out, `"name": "lab"`) || !strings.Contains(out, `"ownership": "managed"`) ||
		!strings.Contains(out, `"name": "lab-fleet"`) || !managedSourceOK ||
		!strings.Contains(out, `"name": "dynamic"`) || !strings.Contains(out, `"status": "unknown"`) ||
		!strings.Contains(out, `"selectable": false`) {
		t.Fatalf("list JSON lacks managed/fleet/unknown provenance:\n%s", out)
	}
	if strings.Contains(out, "COMMAND-LIKE-SECRET") {
		t.Fatalf("list JSON leaked raw Match command arguments:\n%s", out)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("static list invoked runner %d time(s)", fixture.runner.callCount())
	}

	tsv := fixture.mustRun("ssh", "list", "--format", "tsv")
	if !strings.Contains(tsv, "lab\tactive\tmanaged\t"+fixture.managedPath("lab")+"\t2\tlab-fleet") ||
		!strings.Contains(tsv, "foreign\tactive\tforeign\t") {
		t.Fatalf("TSV selector output:\n%s", tsv)
	}
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		if len(strings.Split(line, "\t")) != 6 {
			t.Fatalf("TSV row has unstable column count: %q", line)
		}
	}
	human := fixture.mustRun("ssh", "list")
	if !strings.Contains(human, "ALIAS") || !strings.Contains(human, "lab-fleet") {
		t.Fatalf("human list:\n%s", human)
	}
	completion := fixture.mustRun("__complete", "ssh", "show", "la")
	if !strings.Contains(completion, "lab\tactive · managed") || !strings.HasSuffix(completion, ":4\n") {
		t.Fatalf("SSH completion:\n%s", completion)
	}
	hopCompletion := fixture.mustRun("__complete", "ssh", "setup", "--hop-os", "lab=")
	if !strings.Contains(hopCompletion, "lab=posix") || !strings.Contains(hopCompletion, "lab=windows") {
		t.Fatalf("SSH hop OS completion:\n%s", hopCompletion)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("completion invoked runner %d time(s)", fixture.runner.callCount())
	}
}

func TestSSHShowAndProbeUseInjectedRunnerAndSafeOutput(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.appendRootConfig("Host foreign\n    HostName foreign.example\n")
	primary := `schema_version = 1
[[hosts]]
name = "foreign-fleet"
ssh_alias = "foreign"
ssh_login_password_source = { type = "bitwarden", item = "PASSWORD-MUST-NOT-LEAK" }
`
	if err := os.MkdirAll(filepath.Dir(fixture.remotesPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.remotesPath, []byte(primary), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := fixture.run("ssh", "show", "foreign", "--json")
	if err != nil {
		t.Fatalf("show: %v\nstderr: %s", err, errOut)
	}
	document := assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_show" || document["status"] != "ready" || !strings.Contains(errOut, "Match exec") {
		t.Fatalf("show document/stderr = %#v / %q", document, errOut)
	}
	if strings.Contains(out, "PASSWORD-MUST-NOT-LEAK") || strings.Contains(out, `"values"`) {
		t.Fatalf("show leaked password or raw effective options:\n%s", out)
	}
	calls := fixture.runner.callSnapshot()
	if len(calls) != 1 || calls[0].Name != "ssh" || strings.Join(calls[0].Args, " ") != "-G foreign" {
		t.Fatalf("show runner calls = %#v", calls)
	}

	fixture.runner.resetCalls()
	out = fixture.mustRun("ssh", "probe", "foreign", "--json")
	document = assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_probe" || document["status"] != "ready" {
		t.Fatalf("probe document = %#v", document)
	}
	calls = fixture.runner.callSnapshot()
	if len(calls) != 1 || calls[0].Display != "ssh fresh authentication probe" || !hasSSHArg(calls[0].Args, "-S") || !hasSSHArg(calls[0].Args, "BatchMode=yes") {
		t.Fatalf("probe runner calls = %#v", calls)
	}

	fixture.runner.resetCalls()
	out = fixture.mustRun("ssh", "setup", "foreign", "--config-only", "--json")
	document = assertOneSSHJSON(t, out)
	if document["status"] != "ready" || document["alias_class"] != "foreign" {
		t.Fatalf("read-only foreign config verification = %#v", document)
	}
}

func TestSSHProbeRequiresCompleteUniqueActiveExactAliasBeforeRunner(t *testing.T) {
	tests := []struct {
		name     string
		alias    string
		root     string
		included string
	}{
		{name: "missing", alias: "missing"},
		{name: "wildcard only", alias: "wild-one", root: "Host wild-*\n    HostName wildcard.example\n"},
		{name: "inactive", alias: "inactive", root: "Host another\n    Include guarded.conf\n", included: "Host inactive\n    HostName inactive.example\n"},
		{name: "unknown", alias: "unknown", root: "Match exec never-run\n    Include guarded.conf\n", included: "Host unknown\n    HostName unknown.example\n"},
		{name: "conflict", alias: "conflict", root: "Host conflict\n    HostName one.example\nHost conflict\n    HostName two.example\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSSHCLIFixture(t)
			fixture.initSSH()
			if test.included != "" {
				if err := os.WriteFile(filepath.Join(fixture.home, ".ssh", "guarded.conf"), []byte(test.included), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fixture.appendRootConfig(test.root)
			fixture.runner.resetCalls()

			out, _, err := fixture.run("ssh", "probe", test.alias, "--json")
			if err == nil {
				t.Fatal("statically unsafe probe returned success")
			}
			document := assertOneSSHJSON(t, out)
			result, _ := document["result"].(map[string]any)
			if document["status"] != "blocked" || document["error_code"] != "blocked" || result["code"] != "static_selection_blocked" {
				t.Fatalf("blocked probe document = %#v", document)
			}
			if fixture.runner.callCount() != 0 {
				t.Fatalf("blocked probe reached runner: %#v", fixture.runner.callSnapshot())
			}
		})
	}

	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.appendRootConfig("Host conflict\n    HostName one.example\nHost conflict\n    HostName two.example\n")
	fixture.runner.resetCalls()
	if _, _, err := fixture.run("ssh", "show", "conflict", "--json"); err != nil {
		t.Fatalf("diagnostic show rejected an existing conflicting declaration: %v", err)
	}
	if fixture.runner.callCount() != 1 {
		t.Fatalf("diagnostic show runner calls = %d, want 1", fixture.runner.callCount())
	}
}

func TestSSHCommandContextsAreBoundedWithoutTimingOutInteractiveCredentials(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.appendRootConfig("Host foreign\n    HostName foreign.example\n")

	fixture.mustRun("ssh", "show", "foreign", "--json")
	if deadlines := fixture.runner.deadlineSnapshot(); len(deadlines) != 1 || !deadlines[0] {
		t.Fatalf("ssh show runner deadlines = %v, want one bounded call", deadlines)
	}
	fixture.runner.resetCalls()
	fixture.mustRun("ssh", "probe", "foreign", "--json")
	if deadlines := fixture.runner.deadlineSnapshot(); len(deadlines) != 1 || !deadlines[0] {
		t.Fatalf("ssh probe runner deadlines = %v, want one bounded call", deadlines)
	}
	fixture.runner.resetCalls()
	fixture.mustRun("ssh", "setup", "foreign", "--config-only", "--json")
	if deadlines := fixture.runner.deadlineSnapshot(); len(deadlines) != 1 || !deadlines[0] {
		t.Fatalf("noninteractive ssh setup runner deadlines = %v, want one total bound", deadlines)
	}

	fixture.runner.resetCalls()
	out, errOut, err := fixture.runInteractive("y\n", true, "ssh", "setup", "interactive", "--hostname", "interactive.example", "--config-only")
	if err != nil {
		t.Fatalf("interactive setup: %v\nstderr: %s\nstdout: %s", err, errOut, out)
	}
	if deadlines := fixture.runner.deadlineSnapshot(); len(deadlines) != 1 || deadlines[0] {
		t.Fatalf("interactive setup runner deadlines = %v, want inherited unbounded context", deadlines)
	}

	fixture.runner.resetCalls()
	fixture.runner.blockDisplay = "ssh fresh authentication probe"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, _, err = fixture.runContext(ctx, "", false, "ssh", "probe", "foreign", "--json")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocking probe error = %v, want deadline exceeded", err)
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "failed" || document["error_code"] != "timeout" {
		t.Fatalf("blocking probe document = %#v", document)
	}
}

func TestSSHSetupDryRunNeverUsesRunnerOrMutates(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	keyPath := filepath.Join(fixture.home, ".ssh", "dry-key")
	out := fixture.mustRun(
		"ssh", "setup", "dry-host", "--hostname", "dry.example",
		"--generate-key", "--key-path", keyPath, "--no-passphrase",
		"--target-os", "posix", "--fleet", "--dry-run", "--json",
	)
	document := assertOneSSHJSON(t, out)
	if document["kind"] != "ssh_setup_plan" || document["status"] != "planned" || !strings.Contains(out, `"status": "unknown"`) {
		t.Fatalf("dry-run plan = %#v\n%s", document, out)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("dry-run invoked runner %d time(s)", fixture.runner.callCount())
	}
	for _, path := range []string{
		fixture.managedPath("dry-host"), keyPath, keyPath + ".pub",
		filepath.Join(fixture.home, ".ssh", "dev.d-operations"),
		filepath.Join(fixture.home, ".dev-ssh-dev.d.operations.lock"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run created %s: %v", path, err)
		}
	}
	fleetPath, err := fleet.ManagedFragmentPath(fixture.remotesPath, "dry-host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fleetPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created fleet fragment: %v", err)
	}
}

func TestSSHSetupConfigOnlyNewManagedReconcileAndForeignProtection(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	out := fixture.mustRun("ssh", "setup", "lab", "--hostname", "one.example", "--user", "dev", "--port", "2222", "--identities-only", "--config-only", "--yes", "--json")
	document := assertOneSSHJSON(t, out)
	if document["status"] != "ready" || document["alias_class"] != "new" {
		t.Fatalf("new config-only result = %#v", document)
	}
	managed, err := os.ReadFile(fixture.managedPath("lab"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(managed), "HostName one.example") || !strings.Contains(string(managed), "Port 2222") || !strings.Contains(string(managed), "IdentitiesOnly yes") {
		t.Fatalf("managed config:\n%s", managed)
	}

	out = fixture.mustRun("ssh", "setup", "lab", "--hostname", "two.example", "--config-only", "--yes", "--json")
	document = assertOneSSHJSON(t, out)
	if document["alias_class"] != "managed" || !strings.Contains(out, `"action": "update"`) {
		t.Fatalf("managed reconcile = %#v\n%s", document, out)
	}
	fixture.appendRootConfig("Host foreign\n    HostName foreign.example\n")
	out, _, err = fixture.run("ssh", "setup", "foreign", "--hostname", "competing.example", "--config-only", "--yes", "--json")
	if err == nil {
		t.Fatal("foreign setup accepted a connection field")
	}
	document = assertOneSSHJSON(t, out)
	if document["alias_class"] != "foreign" || document["error_code"] != "foreign_alias" {
		t.Fatalf("foreign block = %#v", document)
	}
	if _, err := os.Lstat(fixture.managedPath("foreign")); !os.IsNotExist(err) {
		t.Fatalf("foreign setup created competing fragment: %v", err)
	}
}

func TestSSHSetupBlocksUnsafeDiscoveredAliasesBeforeOpenSSH(t *testing.T) {
	for _, test := range []struct {
		name       string
		alias      string
		root       string
		included   string
		aliasClass string
	}{
		{
			name: "inactive", alias: "inactive", aliasClass: "inactive",
			root: "Host another\n    Include guarded.conf\n", included: "Host inactive\n    HostName inactive.example\n",
		},
		{
			name: "unknown", alias: "unknown", aliasClass: "unknown",
			root: "Match exec never-run\n    Include guarded.conf\n", included: "Host unknown\n    HostName unknown.example\n",
		},
		{
			name: "conflict", alias: "conflict", aliasClass: "conflict",
			root: "Host conflict\n    HostName one.example\nHost conflict\n    HostName two.example\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSSHCLIFixture(t)
			fixture.initSSH()
			if test.included != "" {
				if err := os.WriteFile(filepath.Join(fixture.home, ".ssh", "guarded.conf"), []byte(test.included), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fixture.appendRootConfig(test.root)
			fixture.runner.resetCalls()

			out, _, err := fixture.run("ssh", "setup", test.alias, "--config-only", "--yes", "--json")
			if err == nil {
				t.Fatal("unsafe discovered alias setup returned success")
			}
			document := assertOneSSHJSON(t, out)
			if document["status"] != "blocked" || document["alias_class"] != test.aliasClass || document["error_code"] != "blocked" {
				t.Fatalf("blocked setup document = %#v", document)
			}
			if fixture.runner.callCount() != 0 {
				t.Fatalf("blocked alias reached OpenSSH runner: %#v", fixture.runner.callSnapshot())
			}
			if _, statErr := os.Lstat(fixture.managedPath(test.alias)); !os.IsNotExist(statErr) {
				t.Fatalf("blocked alias created managed config: %v", statErr)
			}
		})
	}
}

func TestSSHSetupFullFleetAndForeignExplicitKey(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	keyPath := filepath.Join(fixture.home, ".ssh", "full-key")
	out := fixture.mustRun(
		"ssh", "setup", "full", "--hostname", "full.example",
		"--generate-key", "--key-path", keyPath, "--comment", "safe-comment", "--no-passphrase",
		"--target-os", "posix", "--fleet", "--fleet-name", "full-fleet", "--yes", "--json",
	)
	document := assertOneSSHJSON(t, out)
	if document["status"] != "ready" || !strings.Contains(out, `"fleet_ready": true`) || !strings.Contains(out, `"action": "registered"`) {
		t.Fatalf("full setup result = %#v\n%s", document, out)
	}
	for _, path := range []string{keyPath, keyPath + ".pub", fixture.managedPath("full")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("full setup did not retain %s: %v", path, err)
		}
	}
	fleetPath, err := fleet.ManagedFragmentPath(fixture.remotesPath, "full")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := fleet.ValidateManagedFragment(fleetPath)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Name != "full-fleet" || registered.RemoteOS != fleet.RemoteOSPOSIX {
		t.Fatalf("fleet registration = %+v", registered)
	}
	publicToken := strings.Fields(string(fixture.runner.publicLine))[1]
	if strings.Contains(out, publicToken) || strings.Contains(out, "PRIVATE-KEY-BYTES-MUST-NOT-LEAK") {
		t.Fatalf("setup JSON leaked key material:\n%s", out)
	}

	fixture.appendRootConfig("Host foreign\n    HostName foreign.example\n")
	fixture.runner.resetCalls()
	out = fixture.mustRun("ssh", "setup", "foreign", "--key", keyPath, "--target-os", "posix", "--yes", "--json")
	document = assertOneSSHJSON(t, out)
	if document["status"] != "ready" || document["alias_class"] != "foreign" {
		t.Fatalf("foreign key setup = %#v", document)
	}
	if _, err := os.Lstat(fixture.managedPath("foreign")); !os.IsNotExist(err) {
		t.Fatalf("foreign key setup created a fragment: %v", err)
	}
}

func TestSSHSetupPartialRetainsLocalAssetsAndSkipsFleet(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.runner.ordinaryReady = false
	fixture.runner.exactReady = false
	fixture.runner.installerExit = 255
	keyPath := filepath.Join(fixture.home, ".ssh", "partial-key")
	out, _, err := fixture.run(
		"ssh", "setup", "partial", "--hostname", "partial.example",
		"--generate-key", "--key-path", keyPath, "--no-passphrase",
		"--target-os", "posix", "--fleet", "--yes", "--json",
	)
	if err == nil {
		t.Fatal("partial remote bootstrap returned success")
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "partial" || document["error_code"] != "bootstrap_partial" ||
		!strings.Contains(out, `"fleet_ready": false`) || !strings.Contains(out, `"code": "ordinary_gate_failed"`) {
		t.Fatalf("partial result = %#v\n%s", document, out)
	}
	for _, path := range []string{keyPath, keyPath + ".pub", fixture.managedPath("partial")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("partial setup did not retain %s: %v", path, err)
		}
	}
	fleetPath, err := fleet.ManagedFragmentPath(fixture.remotesPath, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fleetPath); !os.IsNotExist(err) {
		t.Fatalf("partial setup wrote fleet registration: %v", err)
	}
}

func TestSSHSetupNonTTYConfirmationAndInteractiveCancellation(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	out, _, err := fixture.run("ssh", "setup", "needs-yes", "--hostname", "host.example", "--config-only", "--json")
	if err == nil {
		t.Fatal("non-TTY setup applied without --yes")
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "confirmation_required" {
		t.Fatalf("non-TTY result = %#v", document)
	}
	if _, err := os.Lstat(fixture.managedPath("needs-yes")); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed setup wrote config: %v", err)
	}

	out, _, err = fixture.runInteractive("n\n", true, "ssh", "setup", "canceled", "--hostname", "host.example", "--config-only")
	if !errors.Is(err, errPromptCanceled) || !strings.Contains(out, "Apply this local SSH setup plan?") {
		t.Fatalf("interactive cancellation = %v\n%s", err, out)
	}
	if _, err := os.Lstat(fixture.managedPath("canceled")); !os.IsNotExist(err) {
		t.Fatalf("canceled setup wrote config: %v", err)
	}
}

func TestSSHRemoveRequiresExplicitFleetAndBlocksPrimaryReference(t *testing.T) {
	t.Run("generated fleet", func(t *testing.T) {
		fixture := newSSHCLIFixture(t)
		fixture.initSSH()
		fixture.createManaged("lab", "lab.example")
		fleetPath, err := fleet.WriteManagedFragment(context.Background(), fixture.remotesPath, fleet.ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: fleet.RemoteOSPOSIX}, nil)
		if err != nil {
			t.Fatal(err)
		}

		out, _, err := fixture.run("ssh", "remove", "lab", "--yes", "--json")
		if err == nil {
			t.Fatal("remove silently kept/deleted a generated fleet entry")
		}
		document := assertOneSSHJSON(t, out)
		if document["error_code"] != "fleet_removal_required" {
			t.Fatalf("remove block = %#v", document)
		}
		for _, path := range []string{fixture.managedPath("lab"), fleetPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("blocked removal changed %s: %v", path, err)
			}
		}

		out = fixture.mustRun("ssh", "remove", "lab", "--fleet", "--dry-run", "--json")
		document = assertOneSSHJSON(t, out)
		if document["status"] != "planned" {
			t.Fatalf("remove dry-run = %#v", document)
		}
		out = fixture.mustRun("ssh", "remove", "lab", "--fleet", "--yes", "--json")
		document = assertOneSSHJSON(t, out)
		if document["status"] != "removed" || !strings.Contains(out, `"action": "removed"`) {
			t.Fatalf("remove result = %#v\n%s", document, out)
		}
		for _, path := range []string{fixture.managedPath("lab"), fleetPath} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("remove left %s: %v", path, err)
			}
		}
	})

	t.Run("primary reference", func(t *testing.T) {
		fixture := newSSHCLIFixture(t)
		fixture.initSSH()
		fixture.createManaged("primary-ref", "host.example")
		if err := os.MkdirAll(filepath.Dir(fixture.remotesPath), 0o700); err != nil {
			t.Fatal(err)
		}
		primary := "schema_version = 1\n[[hosts]]\nname = \"manual\"\nssh_alias = \"primary-ref\"\n"
		if err := os.WriteFile(fixture.remotesPath, []byte(primary), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, err := fixture.run("ssh", "remove", "primary-ref", "--fleet", "--yes", "--json")
		if err == nil {
			t.Fatal("remove accepted a primary remotes.toml reference")
		}
		document := assertOneSSHJSON(t, out)
		if document["error_code"] != "primary_fleet_reference" {
			t.Fatalf("primary-reference block = %#v", document)
		}
		if _, err := os.Stat(fixture.managedPath("primary-ref")); err != nil {
			t.Fatalf("primary-reference block removed SSH config: %v", err)
		}
	})
}

func TestSSHRemoveRechecksConcurrentFleetRegistrationBeforeDeletingConfig(t *testing.T) {
	for _, test := range []struct {
		name           string
		removeFleet    bool
		initialFleet   bool
		expectedStatus string
	}{
		{name: "registration without fleet flag", expectedStatus: "blocked"},
		{name: "registration after requested fleet removal", removeFleet: true, initialFleet: true, expectedStatus: "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSSHCLIFixture(t)
			fixture.initSSH()
			fixture.createManaged("lab", "lab.example")
			fleetPath, err := fleet.ManagedFragmentPath(fixture.remotesPath, "lab")
			if err != nil {
				t.Fatal(err)
			}
			managedHost := fleet.ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: fleet.RemoteOSPOSIX}
			if test.initialFleet {
				if _, err := fleet.WriteManagedFragment(context.Background(), fixture.remotesPath, managedHost, nil); err != nil {
					t.Fatal(err)
				}
			}
			previousHook := beforeSSHRemoveFinalCheck
			beforeSSHRemoveFinalCheck = func() {
				if _, writeErr := fleet.WriteManagedFragment(context.Background(), fixture.remotesPath, managedHost, nil); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			t.Cleanup(func() { beforeSSHRemoveFinalCheck = previousHook })

			args := []string{"ssh", "remove", "lab", "--yes", "--json"}
			if test.removeFleet {
				args = append(args, "--fleet")
			}
			out, _, err := fixture.run(args...)
			if err == nil {
				t.Fatal("remove accepted a final-window fleet registration")
			}
			document := assertOneSSHJSON(t, out)
			if document["status"] != test.expectedStatus || document["error_code"] != "fleet_removal_required" {
				t.Fatalf("concurrent registration document = %#v", document)
			}
			for _, path := range []string{fixture.managedPath("lab"), fleetPath} {
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("concurrent registration should preserve %s: %v", path, statErr)
				}
			}
		})
	}
}

func TestSSHSetupAndRemoveShareOperationLock(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.createManaged("lab", "lab.example")
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseLock()
	previousHook := beforeSSHRemoveFinalCheck
	beforeSSHRemoveFinalCheck = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { beforeSSHRemoveFinalCheck = previousHook })

	type commandResult struct {
		out string
		err error
	}
	removeDone := make(chan commandResult, 1)
	go func() {
		out, _, err := fixture.run("ssh", "remove", "lab", "--yes", "--json")
		removeDone <- commandResult{out: out, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("remove did not reach final check while holding operation lock")
	}
	lockPath := filepath.Join(fixture.home, ".dev-ssh-dev.d.operations.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("operation lock is not derived from the shared SSH namespace at %s: %v", lockPath, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.home, ".ssh", "dev.d-operations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation locking created an auxiliary directory before mutation: %v", err)
	}

	otherConfig := filepath.Join(fixture.home, "other-config.toml")
	otherState := filepath.Join(fixture.home, "other-state")
	if err := os.WriteFile(otherConfig, []byte(
		"[paths]\nstate_dir = \""+filepath.ToSlash(otherState)+"\"\n[runtime]\nbackend = \"none\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	otherFixture := *fixture
	otherFixture.configPath = otherConfig
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, _, err := otherFixture.runContext(ctx, "", false, "ssh", "setup", "other", "--hostname", "other.example", "--config-only", "--yes", "--json")
	if !errors.Is(err, context.DeadlineExceeded) {
		releaseLock()
		t.Fatalf("contending setup error = %v, want deadline exceeded", err)
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "failed" || document["error_code"] != "timeout" {
		releaseLock()
		t.Fatalf("contending setup document = %#v", document)
	}
	if _, statErr := os.Lstat(fixture.managedPath("other")); !os.IsNotExist(statErr) {
		releaseLock()
		t.Fatalf("contending setup mutated before acquiring operation lock: %v", statErr)
	}

	releaseLock()
	select {
	case result := <-removeDone:
		if result.err != nil {
			t.Fatalf("remove after releasing operation lock: %v\n%s", result.err, result.out)
		}
		assertOneSSHJSON(t, result.out)
	case <-time.After(time.Second):
		t.Fatal("remove did not finish after releasing operation lock")
	}
}

func TestFleetEditorHoldsSSHOperationLockForLiveEditAndForceInit(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(fixture.home, "editor-held")
	release := filepath.Join(fixture.home, "editor-release")
	t.Setenv("DEV_FLEET_EDITOR_TEST_HELPER", "1")
	t.Setenv("DEV_FLEET_EDITOR_TEST_MARKER", marker)
	t.Setenv("DEV_FLEET_EDITOR_TEST_RELEASE", release)
	editor := strconv.Quote(executable) + " -test.run=^TestFleetEditorSubprocessHelper$ --"

	type editResult struct {
		out    string
		errOut string
		err    error
	}
	done := make(chan editResult, 1)
	go func() {
		out, errOut, runErr := fixture.run("fleet", "config", "edit", "--editor", editor)
		done <- editResult{out: out, errOut: errOut, err: runErr}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("fleet editor did not enter its live interval")
		}
		time.Sleep(5 * time.Millisecond)
	}
	starterBefore, err := os.ReadFile(fixture.remotesPath)
	if err != nil {
		t.Fatal(err)
	}

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 100*time.Millisecond)
	setupOut, _, setupErr := fixture.runContext(setupCtx, "", false,
		"ssh", "setup", "blocked-by-editor", "--hostname", "blocked.example", "--config-only", "--yes", "--json")
	cancelSetup()
	if !errors.Is(setupErr, context.DeadlineExceeded) {
		t.Fatalf("setup while editor held lock = %v", setupErr)
	}
	setupDocument := assertOneSSHJSON(t, setupOut)
	if setupDocument["error_code"] != "timeout" {
		t.Fatalf("setup lock failure document = %#v", setupDocument)
	}
	if _, err := os.Lstat(fixture.managedPath("blocked-by-editor")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup mutated while editor held lock: %v", err)
	}

	initCtx, cancelInit := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _, initErr := fixture.runContext(initCtx, "", false, "fleet", "config", "init", "--force")
	cancelInit()
	if !errors.Is(initErr, context.DeadlineExceeded) {
		t.Fatalf("force init while editor held lock = %v", initErr)
	}
	starterAfter, err := os.ReadFile(fixture.remotesPath)
	if err != nil || !bytes.Equal(starterAfter, starterBefore) {
		t.Fatalf("force init changed live editor file: equal=%v err=%v", bytes.Equal(starterAfter, starterBefore), err)
	}

	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("fleet editor after release: %v\nstdout: %s\nstderr: %s", result.err, result.out, result.errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fleet editor did not release operation lock")
	}
}

func TestSSHJSONFailureRedactsRunnerStderrAndStillEmitsOneObject(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.appendRootConfig("Host secret-host\n    HostName secret.example\n")
	fixture.runner.effectiveExit = 255
	fixture.runner.effectiveErr = "PASSWORD=top-secret --proxy-command secret-command"
	out, _, err := fixture.run("ssh", "show", "secret-host", "--json")
	if err == nil {
		t.Fatal("failed ssh -G returned success")
	}
	document := assertOneSSHJSON(t, out)
	if document["status"] != "failed" || document["error_code"] != "operation_failed" {
		t.Fatalf("failed show document = %#v", document)
	}
	for _, secret := range []string{"top-secret", "secret-command", "PASSWORD="} {
		if strings.Contains(out, secret) {
			t.Fatalf("JSON leaked %q:\n%s", secret, out)
		}
	}
}

func TestDoctorSSHChecksAreStaticAndFleetConfigMarksGeneratedHosts(t *testing.T) {
	fixture := newSSHCLIFixture(t)
	fixture.initSSH()
	fixture.createManaged("lab", "lab.example")
	if _, err := fleet.WriteManagedFragment(context.Background(), fixture.remotesPath, fleet.ManagedHost{Name: "generated-lab", SSHAlias: "lab", RemoteOS: fleet.RemoteOSPOSIX}, nil); err != nil {
		t.Fatal(err)
	}
	fixture.runner.resetCalls()

	out, _, err := fixture.run("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, marker := range []string{"ssh", "ssh-keygen", "ssh config", "ssh managed", "ssh security", "fleet fragments"} {
		if !strings.Contains(out, marker) {
			t.Errorf("doctor lacks %q:\n%s", marker, out)
		}
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("doctor invoked SSH runner %d time(s)", fixture.runner.callCount())
	}

	show := fixture.mustRun("fleet", "config", "show")
	if !strings.Contains(show, `# generated host "generated-lab"`) || !strings.Contains(show, "use dev ssh setup/remove") {
		t.Fatalf("fleet config show does not identify generated host:\n%s", show)
	}
	var warning bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: io.Discard, Err: &warning, configPath: fixture.configPath, remotesPath: fixture.remotesPath}
	if err := app.Load(); err != nil {
		t.Fatal(err)
	}
	warnManagedFleetEditorScope(app)
	if !strings.Contains(warning.String(), "opens only primary remotes.toml") || !strings.Contains(warning.String(), "generated-lab") {
		t.Fatalf("fleet editor warning = %q", warning.String())
	}
	loadedFleet, err := loadFleetConfig(app)
	if err != nil || len(loadedFleet.Hosts) != 1 || !loadedFleet.Hosts[0].Managed() {
		t.Fatalf("generated fleet config = %+v, err %v", loadedFleet.Hosts, err)
	}
	if err := fleet.SaveCache(loadedFleet.Hosts[0], fleet.Snapshot{
		SchemaVersion: fleet.SnapshotSchemaVersion, Host: "generated-lab", GeneratedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rows := cachedFleetRows(app)
	if len(rows) != 1 || rows[0].Host != "generated-lab" {
		t.Fatalf("generated host did not reach FLEET cache rows: %+v", rows)
	}
	process, err := fleetConfigEditorProcess(app, "true")
	if err != nil {
		t.Fatal(err)
	}
	joinedEditorArgs := strings.Join(process.Args, " ")
	if !strings.Contains(joinedEditorArgs, "fleet _config-edit") ||
		!strings.Contains(joinedEditorArgs, "--config "+fixture.configPath) ||
		!strings.Contains(joinedEditorArgs, "--remotes "+fixture.remotesPath) ||
		strings.Contains(joinedEditorArgs, fleet.ManagedFragmentDir(fixture.remotesPath)) {
		t.Fatalf("TUI fleet editor helper does not preserve primary-only custom paths: %#v", process.Args)
	}

	t.Setenv("PATH", t.TempDir())
	missingChecks := sshDoctorChecks(&App{remotesPath: fixture.remotesPath, sshHostRunner: fixture.runner})
	for _, binary := range []string{"ssh", "ssh-keygen"} {
		foundWarning := false
		for _, check := range missingChecks {
			if check.name == binary && check.status == checkWarn {
				foundWarning = true
			}
		}
		if !foundWarning {
			t.Errorf("missing optional %s was not a warning: %+v", binary, missingChecks)
		}
	}
}

package sshhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func boolPointer(value bool) *bool { return &value }

type runnerFunc func(context.Context, RunRequest) (RunResult, error)

func (f runnerFunc) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return f(ctx, request)
}

type managedFixtureRunner struct {
	paths Paths
}

func (r managedFixtureRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	if request.Name != "ssh" || len(request.Args) != 2 || request.Args[0] != "-G" || request.Args[1] == "" {
		return RunResult{}, fmt.Errorf("unexpected managed verification request: %s", request.Display)
	}
	path, err := r.paths.ManagedPath(request.Args[1])
	if err != nil {
		return RunResult{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RunResult{}, err
	}
	definition, err := ParseManaged(data)
	if err != nil {
		return RunResult{}, err
	}
	var output strings.Builder
	write := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&output, "%s %s\n", name, value)
		}
	}
	write("hostname", definition.HostName)
	write("user", definition.User)
	if definition.Port != 0 {
		write("port", strconv.Itoa(definition.Port))
	}
	write("proxyjump", definition.ProxyJump)
	write("identityfile", definition.IdentityFile)
	if definition.IdentitiesOnly != nil {
		if *definition.IdentitiesOnly {
			write("identitiesonly", "yes")
		} else {
			write("identitiesonly", "no")
		}
	}
	return RunResult{Stdout: []byte(output.String())}, nil
}

func TestValidateManagedAliasPortableGrammar(t *testing.T) {
	for _, alias := range []string{"a", "lab-1", "foo.bar", "host_name", "0host"} {
		if err := ValidateManagedAlias(alias); err != nil {
			t.Errorf("ValidateManagedAlias(%q): %v", alias, err)
		}
	}
	invalid := []string{
		"", "Lab", ".hidden", "trailing-", "a/b", `a\b`, "c:drive", "wild*", "neg!", "has space",
		"con", "com1.example", "lpt9", "ünicode", "line\nfeed", strings.Repeat("a", maxManagedAliasBytes+1),
	}
	for _, alias := range invalid {
		if err := ValidateManagedAlias(alias); err == nil {
			t.Errorf("ValidateManagedAlias(%q) succeeded", alias)
		}
	}
}

func TestManagedRenderingAndParsingAreCanonical(t *testing.T) {
	definition := ManagedDefinition{
		Alias: "lab-1", HostName: "lab host#1", User: "alice", Port: 2222,
		ProxyJump: "jump", IdentityFile: `~/.ssh/id with space`, IdentitiesOnly: boolPointer(true),
	}
	first, err := RenderManaged(definition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderManaged(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("rendering is not deterministic")
	}
	want := ManagedHeader + "\n" +
		"Host lab-1\n" +
		"    HostName \"lab host#1\"\n" +
		"    User alice\n" +
		"    Port 2222\n" +
		"    ProxyJump jump\n" +
		"    IdentityFile \"~/.ssh/id with space\"\n" +
		"    IdentitiesOnly yes\n"
	if string(first) != want {
		t.Fatalf("rendered content:\n%s\nwant:\n%s", first, want)
	}
	parsed, err := ParseManaged(first)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Alias != definition.Alias || parsed.HostName != definition.HostName || parsed.User != definition.User ||
		parsed.Port != definition.Port || parsed.ProxyJump != definition.ProxyJump || parsed.IdentityFile != definition.IdentityFile ||
		parsed.IdentitiesOnly == nil || !*parsed.IdentitiesOnly {
		t.Fatalf("parsed definition = %#v", parsed)
	}
}

func TestParseManagedRejectsDriftAndUnknownDirectives(t *testing.T) {
	canonical, err := RenderManaged(ManagedDefinition{Alias: "lab", HostName: "host", User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown directive": bytes.Replace(canonical, []byte("    User alice\n"), []byte("    StrictHostKeyChecking no\n"), 1),
		"reordered":         []byte(ManagedHeader + "\nHost lab\n    User alice\n    HostName host\n"),
		"duplicate":         append(bytes.TrimSuffix(canonical, []byte("\n")), []byte("\n    User bob\n")...),
		"comment":           bytes.Replace(canonical, []byte("Host lab\n"), []byte("Host lab # drift\n"), 1),
		"crlf":              bytes.ReplaceAll(canonical, []byte("\n"), []byte("\r\n")),
		"bom":               append([]byte{0xef, 0xbb, 0xbf}, canonical...),
		"missing newline":   bytes.TrimSuffix(canonical, []byte("\n")),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManaged(content); err == nil {
				t.Fatalf("ParseManaged accepted drift:\n%s", content)
			}
		})
	}
}

func TestValidateManagedDefinitionRejectsNonAllowlistedShapes(t *testing.T) {
	for _, definition := range []ManagedDefinition{
		{Alias: "lab"},
		{Alias: "lab", HostName: "host", Port: -1},
		{Alias: "lab", HostName: "host", Port: 65536},
		{Alias: "lab", HostName: "host\nother"},
	} {
		if err := ValidateManagedDefinition(definition); err == nil {
			t.Errorf("ValidateManagedDefinition(%#v) succeeded", definition)
		}
	}
}

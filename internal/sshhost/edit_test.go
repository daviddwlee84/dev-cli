//go:build linux || darwin

package sshhost

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
)

func TestFormattingPreservesOptionsAndIsIdempotent(t *testing.T) {
	raw := []byte("# header\r\n Host a b\r\n\tHostName=example.invalid\r\n  ProxyCommand ssh jump 'nc %h %p'\r\n  # note\r\nMatch exec \"never-run\"\r\n\tUser tester\r\n")
	formatted, err := FormatConfig(raw, "    ")
	if err != nil {
		t.Fatal(err)
	}
	again, err := FormatConfig(formatted, "    ")
	if err != nil || !bytes.Equal(formatted, again) {
		t.Fatal("not idempotent")
	}
	for _, value := range []string{"HostName=example.invalid", "ProxyCommand ssh jump 'nc %h %p'", "Match exec \"never-run\""} {
		if !bytes.Contains(formatted, []byte(value)) {
			t.Fatal("changed value", value)
		}
	}
	if strings.Count(string(raw), "\r\n") != strings.Count(string(formatted), "\r\n") {
		t.Fatal("changed line endings")
	}
	if _, err = FormatConfig([]byte("Host \"unterminated"), "    "); err == nil {
		t.Fatal("accepted malformed input")
	}
}
func TestOrganizeGroupsPreserveOrderCommentsAndMultiAliases(t *testing.T) {
	ctx := context.Background()
	paths := fixturePaths(t)
	raw := "# file header\nHost alpha beta\n  HostName 192.0.2.1\n  User first\n\n# personal box\nHost personal\n  HostName 192.0.2.2\n\n# another work box\nHost gamma\n  HostName 192.0.2.3\nHost *\n  User fallback\n"
	writeFixture(t, paths.RootConfig, raw)
	service := newFixtureService(t, paths, DiscoverOptions{})
	layout, err := service.Organization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Blocks) != 4 || len(layout.Blocks[0].Aliases) != 2 {
		t.Fatalf("blocks: %+v", layout.Blocks)
	}
	plan, err := service.PlanOrganize(ctx, layout, map[string]string{"1": "work", "2": "personal", "3": "work", "4": "common"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(paths.SSHDir, "config.d")); !os.IsNotExist(err) {
		t.Fatal("plan created directories")
	}
	recovery := filepath.Join(paths.Home, "recovery")
	result, err := configedit.Apply(ctx, plan, recovery)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(paths.RootConfig)
	text := string(data)
	if !(strings.Index(text, "work/alpha.conf") < strings.Index(text, "personal/personal.conf") && strings.Index(text, "personal/personal.conf") < strings.Index(text, "work/gamma.conf")) {
		t.Fatal("grouping reordered Host blocks", text)
	}
	fragment, _ := os.ReadFile(filepath.Join(paths.SSHDir, "config.d", "personal", "personal.conf"))
	if !strings.Contains(string(fragment), "# personal box") {
		t.Fatal("comment did not follow block")
	}
	multi, _ := os.ReadFile(filepath.Join(paths.SSHDir, "config.d", "work", "alpha.conf"))
	if !strings.Contains(string(multi), "Host alpha beta") {
		t.Fatal("split multi alias")
	}
	if _, err := exec.LookPath("ssh"); err == nil {
		before := filepath.Join(paths.Home, "before")
		after := filepath.Join(paths.Home, "after")
		writeFixture(t, before, raw)
		writeFixture(t, after, strings.ReplaceAll(text, "~/.ssh", paths.SSHDir))
		for _, alias := range []string{"alpha", "beta", "personal", "gamma", "other"} {
			a, e := exec.Command("ssh", "-G", "-F", before, alias).Output()
			if e != nil {
				t.Fatal(e)
			}
			b, e := exec.Command("ssh", "-G", "-F", after, alias).Output()
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("OpenSSH effective config changed for %s", alias)
			}
		}
	}
	// Regroup one block using the generated dispatcher; the old file is removed
	// only after the root points to its replacement.
	second, err := service.Organization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := service.PlanOrganize(ctx, second, map[string]string{"2": "work"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configedit.Apply(ctx, moved, recovery); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(paths.SSHDir, "config.d", "personal", "personal.conf")); !os.IsNotExist(err) {
		t.Fatal("old group fragment remains")
	}
	if _, err = configedit.RestorePlan(ctx, recovery, result.Receipt); err == nil {
		t.Fatal("older receipt must reject regrouping changes")
	}
}
func TestOrganizeDoesNotActivateDormantFilesOrOverwriteCollisions(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host box\n  HostName 192.0.2.4\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "config.d", "01_git"), "Host dormant\n")
	service := newFixtureService(t, paths, DiscoverOptions{})
	l, err := service.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := service.PlanOrganize(context.Background(), l, map[string]string{"1": "work"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Diff(func(s string) string { return s }), "config.d/*") {
		t.Fatal("broad Include introduced")
	}
	writeFixture(t, filepath.Join(paths.SSHDir, "config.d", "work", "box.conf"), "Host foreign\n")
	if _, err = service.PlanOrganize(context.Background(), l, map[string]string{"1": "work"}, false); err == nil {
		t.Fatal("collision accepted")
	}
	if _, err = service.PlanOrganize(context.Background(), l, map[string]string{"1": "../escape"}, false); err == nil {
		t.Fatal("group traversal accepted")
	}
}
func TestOrganizeRejectsPrematureWildcardActivation(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include config.d/work/*.conf\nHost box\n  HostName 192.0.2.4\n")
	service := newFixtureService(t, paths, DiscoverOptions{})
	l, err := service.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PlanOrganize(context.Background(), l, map[string]string{"1": "work"}, false); err == nil {
		t.Fatal("new fragment would activate before root switch")
	}
}

func TestGroupPathsWithSpacesAreQuotedAndReDiscoverable(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host box\n  HostName 192.0.2.5\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	ctx := context.Background()
	layout, err := s.Organization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanOrganize(ctx, layout, map[string]string{"1": "Work machines"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configedit.Apply(ctx, p, filepath.Join(paths.Home, "recovery")); err != nil {
		t.Fatal(err)
	}
	inv, err := s.Discover(ctx)
	if err != nil || !inv.Complete {
		t.Fatal(inv, err)
	}
	if _, ok := inv.Find("box"); !ok {
		t.Fatal("quoted grouped alias vanished")
	}
	again, err := s.Organization(ctx)
	if err != nil || again.Blocks[0].Group != "Work machines" {
		t.Fatal(again, err)
	}
}

func TestFormatEmptyFileIsNoopNotRemoval(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "")
	s := newFixtureService(t, paths, DiscoverOptions{})
	p, err := s.PlanFormat(context.Background(), nil, "    ")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Empty() {
		t.Fatal("empty formatting planned a file removal", p.Preview())
	}
	if _, err = os.Stat(paths.RootConfig); err != nil {
		t.Fatal("empty file disappeared")
	}
}

func TestOrganizePreservesProviderOwnedInlineRegions(t *testing.T) {
	paths := fixturePaths(t)
	raw := "# BEGIN tsnet ssh-config v1\nHost provider\n  HostName 192.0.2.10\n# END tsnet ssh-config v1\n"
	writeFixture(t, paths.RootConfig, raw)
	s := newFixtureService(t, paths, DiscoverOptions{})
	if _, err := s.Organization(context.Background()); err == nil {
		t.Fatal("provider-owned root region was offered for migration")
	}
	b, _ := os.ReadFile(paths.RootConfig)
	if string(b) != raw {
		t.Fatal("provider region changed")
	}
}

func TestRegroupingPreservesExistingFragmentBasename(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include ~/.ssh/config.d/work/friendly-name.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "config.d", "work", "friendly-name.conf"), "Host lab\n  HostName 192.0.2.11\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	l, err := s.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanOrganize(context.Background(), l, map[string]string{"1": "personal"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configedit.Apply(context.Background(), p, filepath.Join(paths.Home, "recovery")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(paths.SSHDir, "config.d", "personal", "friendly-name.conf")); err != nil {
		t.Fatal("regrouping renamed the user's fragment", err)
	}
}

func TestOrganizeDoesNotMakeBroadIncludeReadNewDirectory(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include config.d/*\nHost box\n  HostName 192.0.2.12\n")
	s := newFixtureService(t, paths, DiscoverOptions{})
	l, err := s.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanOrganize(context.Background(), l, map[string]string{"1": "work"}, false); err == nil {
		t.Fatal("new group directory would enter the existing Include glob")
	}
}

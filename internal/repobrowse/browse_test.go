package repobrowse

import (
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"testing"
)

func TestBrowseUsesLocalRemoteSelectionAndSafeURLs(t *testing.T) {
	r := gittest.New(t)
	r.Git("remote", "add", "origin", "git@github.com:owner/project.git")
	r.Git("remote", "add", "upstream", "https://gitlab.com/team/project.git")
	choices, err := Choices(t.Context(), r.Root, "")
	if err != nil || len(choices) != 1 || choices[0].URL != "https://github.com/owner/project" {
		t.Fatalf("%+v %v", choices, err)
	}
	choices, err = Choices(t.Context(), r.Root, "upstream")
	if err != nil || choices[0].URL != "https://gitlab.com/team/project" {
		t.Fatalf("%+v %v", choices, err)
	}
	r.Git("remote", "set-url", "origin", "git@unknown-alias:owner/project.git")
	if _, err := Choices(t.Context(), r.Root, ""); err == nil {
		t.Fatal("unknown SSH alias was guessed")
	}
}

func TestBrowseReturnsMultipleRemotesWithoutOrigin(t *testing.T) {
	r := gittest.New(t)
	r.Git("remote", "add", "one", "https://github.com/one/project.git")
	r.Git("remote", "add", "two", "https://github.com/two/project.git")
	choices, err := Choices(t.Context(), r.Root, "")
	if err != nil || len(choices) != 2 {
		t.Fatalf("%+v %v", choices, err)
	}
	if _, err := Choices(t.Context(), r.Root, "absent"); err == nil {
		t.Fatal("missing remote accepted")
	}
}

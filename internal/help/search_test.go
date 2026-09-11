package help

import (
	"strings"
	"testing"
)

func TestSearchTopicsRanksNamesAndPreservesSourceAnchors(t *testing.T) {
	topics := []Topic{{Name: "other", Title: "Other", Body: "# Other\nmentions status and staged somewhere"}, {Name: "status", Title: "Git status", Body: "# Git status\n\n## Changes\n+N counts staged paths"}}
	hits := SearchTopics(topics, "STATUS staged")
	if len(hits) != 2 || hits[0].Topic.Name != "status" || hits[0].Line != 3 || hits[0].Heading != "Changes" {
		t.Fatalf("hits: %+v", hits)
	}
	if got := SearchTopics(topics, "no-match"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestEmbeddedSearchFindsLiteralGitSymbols(t *testing.T) {
	hits, err := Search("?3")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if hit.Topic.Name == "git-status" {
			found = true
			lines := strings.Split(hit.Topic.Body, "\n")
			if !strings.Contains(lines[hit.Line], "?3") {
				t.Fatal("lost source location")
			}
		}
	}
	if !found {
		t.Fatal("Git symbol absent from manual search")
	}
}

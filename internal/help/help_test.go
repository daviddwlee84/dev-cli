package help_test

import (
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/help"
)

func TestListAllTopicsParse(t *testing.T) {
	all, err := help.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no help topics are embedded")
	}
	for _, topic := range all {
		if topic.Title == "" {
			t.Errorf("%s: no level-1 heading", topic.Name)
		}
		if topic.Summary == "" {
			t.Errorf("%s: no summary paragraph after the heading", topic.Name)
		}
		if strings.HasPrefix(topic.Summary, ">") || strings.HasPrefix(topic.Summary, "-") {
			t.Errorf("%s: summary keeps a markdown marker: %q", topic.Name, topic.Summary)
		}
		if len(topic.Body) < 200 {
			t.Errorf("%s: suspiciously short (%d bytes)", topic.Name, len(topic.Body))
		}
	}
}

func TestGetExactAndPrefix(t *testing.T) {
	if _, err := help.Get("worktrees"); err != nil {
		t.Fatalf("exact name: %v", err)
	}
	if got, err := help.Get("worktree"); err != nil || got.Name != "worktrees" {
		t.Errorf("prefix match: %+v %v", got, err)
	}
	if _, err := help.Get("no-such-topic"); err == nil {
		t.Error("an unknown topic should error")
	} else if !strings.Contains(err.Error(), "dev help") {
		t.Errorf("the error should point at the index, got %v", err)
	}
}

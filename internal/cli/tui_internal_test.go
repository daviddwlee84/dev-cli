package cli

import (
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
)

func TestExternalToolCommandModesAreExplicit(t *testing.T) {
	app := &App{Cfg: config.Default()}
	app.Cfg.TUI.Tools = []config.Tool{
		{Key: "N", Name: "normal", Run: "printf normal"},
		{Key: "I", Name: "interactive", Run: "my-shell-alias --flag", Interactive: true},
	}
	got := externalTools(app)
	if len(got) != 2 {
		t.Fatalf("got %d tools", len(got))
	}
	if len(got[0].Command) != 3 || got[0].Command[1] != "-c" || got[0].Command[2] != "printf normal" {
		t.Errorf("ordinary command = %v", got[0].Command)
	}
	if len(got[1].Command) != 5 || got[1].Command[1] != "-lic" ||
		got[1].Command[2] != `eval "$1"` || got[1].Command[4] != "my-shell-alias --flag" {
		t.Errorf("interactive command must evaluate after rc loading: %v", got[1].Command)
	}
}

func TestCommandRunnableCachesProbe(t *testing.T) {
	// A known executable is enough to assert a stable result. The sync.Once in
	// the closure is what prevents interactive probes from launching a login
	// shell on every TUI frame.
	check := commandRunnable("go version", false)
	first := check()
	for i := 0; i < 10; i++ {
		if check() != first {
			t.Fatal("availability result changed within one dashboard")
		}
	}
}

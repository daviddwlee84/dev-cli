// Command dev is a thin glue layer over git, worktrees, forges and agent
// runtimes. See `dev --help`, or the bundled agent skill via `dev --skill`.
package main

import (
	"os"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func main() {
	if handled, code := fleet.MaybeServeAskpass(); handled {
		os.Exit(code)
	}
	os.Exit(cli.Execute())
}

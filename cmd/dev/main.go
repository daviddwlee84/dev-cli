// Command dev is a thin glue layer over git, worktrees, forges and agent
// runtimes. See `dev --help`, or the bundled agent skill via `dev --skill`.
package main

import (
	"os"

	"github.com/daviddwlee84/dev-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}

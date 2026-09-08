//go:build !windows

package agentinterop

import (
	"context"
	"os"
	"syscall"
)

// Replacing dev preserves MCP stdio, signals, and the server's exit status;
// there is no intermediary child to orphan when the client disconnects.
func executeMCP(ctx context.Context, spec launchSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Chdir(spec.Dir); err != nil {
		return err
	}
	return syscall.Exec(spec.Program, spec.Args, spec.Env)
}

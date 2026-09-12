//go:build !windows

package agentskill

import (
	"context"
	"os/exec"
)

func providerCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, args...)
}

package cli

import "github.com/spf13/cobra"

// staticContentInvocation identifies embedded documents that need neither user
// configuration nor runtime state. Inspect the parsed flag value: --skill=false
// is still an ordinary root invocation.
func staticContentInvocation(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Parent() == nil {
		printSkill, _ := cmd.Flags().GetBool("skill")
		return printSkill
	}
	switch cmd.CommandPath() {
	case "dev help", "dev skill print":
		return true
	default:
		return false
	}
}

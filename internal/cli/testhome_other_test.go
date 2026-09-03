//go:build !windows

package cli

import "testing"

func cliTestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

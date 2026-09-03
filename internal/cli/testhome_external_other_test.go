//go:build !windows

package cli_test

import "testing"

func externalCLITestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

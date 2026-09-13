// Package testutil supplies filesystem fixtures with explicit platform limits.
package testutil

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

// Link skips only when Android itself forbids making the adversarial hard-link
// fixture. The same rejection tests continue to run on native desktop systems.
func Link(t testing.TB, oldname, newname string) error {
	t.Helper()
	err := os.Link(oldname, newname)
	if runtime.GOOS == "android" && errors.Is(err, os.ErrPermission) {
		t.Skip("Android app sandbox denies the hard-link fixture")
	}
	return err
}

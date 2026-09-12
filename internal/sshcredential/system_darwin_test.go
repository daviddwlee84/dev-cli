//go:build darwin

package sshcredential

import "testing"

// This exercises only framework symbols and temporary CoreFoundation objects.
// It never calls SecItem*, reads a real keychain item, or writes to Keychain.
func TestDarwinSystemFrameworkBindingsWithoutVaultAccess(t *testing.T) {
	a := loadMacSecurity()
	if a.err != nil {
		t.Fatal(a.err)
	}
	query, cleanup := a.query(testContext())
	defer cleanup()
	if query == 0 {
		t.Fatal("native dictionary unavailable")
	}
	value := []byte("temporary memory only")
	data := a.dataCreate(0, &value[0], int64(len(value)))
	if data == 0 {
		t.Fatal("native data unavailable")
	}
	defer a.release(data)
	if a.dataLength(data) != int64(len(value)) {
		t.Fatal("native data length mismatch")
	}
}

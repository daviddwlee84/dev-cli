//go:build unix

package sshhost

import (
	"reflect"
	"runtime"
	"testing"
)

func TestStagedXattrsIgnoreOnlyKernelProvenance(t *testing.T) {
	tag := []byte{0x01, 0x02, 0x00, 0xcb, 0x5f, 0x83, 0x21, 0x7c, 0x5a, 0x73, 0x4c}
	source := map[string][]byte{"com.apple.provenance": tag, "user.retained": []byte("value")}
	staged := map[string][]byte{"com.apple.provenance": {0x01, 0x00, 0x00, 0xa1, 0xae, 0xbe, 0xb9, 0xac, 0xcb, 0x58, 0x9b}, "user.retained": []byte("value")}
	equal := reflect.DeepEqual(xattrsWithoutKernelProvenance(source), xattrsWithoutKernelProvenance(staged))
	if equal != (runtime.GOOS == "darwin") {
		t.Fatalf("provenance exemption on %s = %t", runtime.GOOS, equal)
	}
	if reflect.DeepEqual(xattrsWithoutKernelProvenance(source), xattrsWithoutKernelProvenance(map[string][]byte{"com.apple.provenance": tag})) {
		t.Fatal("ordinary attribute loss was ignored")
	}
	malformed := map[string][]byte{"com.apple.provenance": []byte("not-a-tag"), "user.retained": []byte("value")}
	if _, ok := xattrsWithoutKernelProvenance(malformed)["com.apple.provenance"]; !ok {
		t.Fatal("unrecognized provenance format was filtered")
	}
}

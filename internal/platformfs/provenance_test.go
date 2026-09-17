package platformfs

import "testing"

func TestDarwinKernelProvenanceIsNarrowAndUnavailableElsewhere(t *testing.T) {
	tag := []byte{0x01, 0x02, 0x00, 0xcb, 0x5f, 0x83, 0x21, 0x7c, 0x5a, 0x73, 0x4c}
	if !kernelProvenance("darwin", "com.apple.provenance", tag) {
		t.Fatal("native macOS provenance tag rejected")
	}
	for _, goos := range []string{"linux", "android", "windows", "freebsd"} {
		if kernelProvenance(goos, "com.apple.provenance", tag) {
			t.Fatalf("provenance exception enabled on %s", goos)
		}
	}
	for _, name := range []string{"user.com.apple.provenance", "com.apple.quarantine", "com.apple.system.Security", "com.apple.provenance.extra"} {
		if kernelProvenance("darwin", name, tag) {
			t.Fatalf("unrelated attribute accepted: %s", name)
		}
	}
	for _, value := range [][]byte{nil, {}, tag[:10], append(append([]byte{}, tag...), 0x00), append([]byte{0x00}, tag[1:]...), append([]byte{0x02}, tag[1:]...)} {
		if kernelProvenance("darwin", "com.apple.provenance", value) {
			t.Fatalf("unexpected provenance value accepted: % x", value)
		}
	}
}

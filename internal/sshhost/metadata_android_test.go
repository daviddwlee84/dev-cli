package sshhost

import (
	"bytes"
	"os"
	"testing"
)

func TestAndroidInheritedSecurityMetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	source, err := os.CreateTemp(dir, "source-")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := platformCaptureMetadata(source.Name(), source, info)
	if err != nil || platformMetadataRoundTrip(metadata) != nil {
		t.Fatalf("native source metadata rejected: %+v %v", metadata, err)
	}
	if len(metadata.xattrs["security.selinux"]) == 0 {
		t.Fatal("native Android fixture has no inherited label")
	}
	stage, err := os.CreateTemp(dir, "stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if err := platformApplyMetadata(stage.Name(), stage, metadata); err != nil {
		t.Fatal(err)
	}
	if err := platformVerifyMetadata(stage.Name(), stage, metadata); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), metadata.xattrs["security.selinux"]...)
	metadata.xattrs["security.selinux"] = []byte("u:object_r:app_data_file:s0:c999\x00")
	if bytes.Equal(before, metadata.xattrs["security.selinux"]) {
		t.Fatal("mismatch fixture accidentally matches native label")
	}
	if err := platformApplyMetadata(stage.Name(), stage, metadata); err == nil {
		t.Fatal("mismatched label accepted")
	}
	after, err := platformReadXattrs(stage)
	if err != nil || !bytes.Equal(after["security.selinux"], before) {
		t.Fatal("mismatch changed the kernel label", err)
	}
}

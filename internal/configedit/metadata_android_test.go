package configedit

import (
	"bytes"
	"os"
	"testing"
)

func TestAndroidConfigMetadataPreservesInheritedLabel(t *testing.T) {
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
	metadata, err := captureMetadata(source.Name(), info)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), metadata.Attributes["security.selinux"]...)
	if len(before) == 0 {
		t.Fatal("native Android fixture has no inherited label")
	}
	stage, err := os.CreateTemp(dir, "stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if err := metadata.prepare(stage); err != nil {
		t.Fatal(err)
	}
	metadata.Attributes["security.selinux"] = []byte("u:object_r:app_data_file:s0:c999\x00")
	if err := metadata.prepare(stage); err == nil {
		t.Fatal("mismatched label accepted")
	}
	after, err := readAttributes(stage)
	if err != nil || !bytes.Equal(after["security.selinux"], before) {
		t.Fatal("mismatch changed the kernel label", err)
	}
}

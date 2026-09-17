//go:build linux || darwin

package configedit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRecoveryReceiptRoundTripsAttributePresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		data []byte
	}{
		{name: "no attributes"},
		{name: "empty attribute", set: true, data: []byte{}},
		{name: "nonempty attribute", set: true, data: []byte("retained")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			put(t, path, "before")
			if tc.set {
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				err = unix.Fsetxattr(int(file.Fd()), "user.dev-configedit-test", tc.data, 0)
				file.Close()
				if errors.Is(err, unix.ENOTSUP) {
					t.Skip("filesystem does not support extended attributes")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := observe(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := New(t.Context(), []string{path}, [][]byte{[]byte("after")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			recovery := filepath.Join(dir, "recovery")
			result, err := Apply(t.Context(), plan, recovery)
			if err != nil {
				t.Fatal(err)
			}
			restore, err := RestorePlan(t.Context(), recovery, result.Receipt)
			if err != nil {
				t.Fatalf("unchanged metadata rejected after JSON receipt: %v", err)
			}
			if _, err := Apply(t.Context(), restore, recovery); err != nil {
				t.Fatal(err)
			}
			after, err := observe(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before.before, after.before) || !reflect.DeepEqual(before.metadata, after.metadata) {
				t.Fatal("recovery changed original bytes or metadata")
			}
		})
	}
}

const testProvenance = "user.dev-configedit-provenance"

func setTestAttribute(t *testing.T, path, name string, value []byte) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = unix.Fsetxattr(int(file.Fd()), name, value, 0)
	if errors.Is(err, unix.ENOTSUP) {
		t.Skip("filesystem does not support extended attributes")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// simulateKernelProvenance treats testProvenance like macOS provenance: the
// predicate recognizes it and the kernel silently ignores attempts to set it.
func simulateKernelProvenance(t *testing.T) *[]string {
	t.Helper()
	oldPredicate, oldSetter := kernelProvenance, setAttribute
	t.Cleanup(func() { kernelProvenance, setAttribute = oldPredicate, oldSetter })
	var set []string
	kernelProvenance = func(name string, _ []byte) bool { return name == testProvenance }
	setAttribute = func(fd int, name string, value []byte) error {
		set = append(set, name)
		if name == testProvenance {
			return nil
		}
		return unix.Fsetxattr(fd, name, value, 0)
	}
	return &set
}

func TestPrepareNeverRestoresKernelProvenance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	put(t, path, "before")
	setTestAttribute(t, path, testProvenance, []byte("foreign-writer"))
	setTestAttribute(t, path, "user.dev-configedit-test", []byte("retained"))
	set := simulateKernelProvenance(t)
	plan, err := New(t.Context(), []string{path}, [][]byte{[]byte("after")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), plan, filepath.Join(dir, "recovery")); err != nil {
		t.Fatalf("kernel provenance blocked replacement: %v", err)
	}
	for _, name := range *set {
		if name == testProvenance {
			t.Fatal("kernel provenance was restored onto the replacement")
		}
	}
	after, err := observe(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.before) != "after" || !bytes.Equal(after.metadata.Attributes["user.dev-configedit-test"], []byte("retained")) {
		t.Fatal("ordinary attribute or content was not preserved")
	}
	if _, ok := after.metadata.Attributes[testProvenance]; ok {
		t.Fatal("simulated kernel provenance unexpectedly copied")
	}
}

func TestPrepareStillRejectsIgnoredOrdinaryAttribute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	put(t, path, "before")
	setTestAttribute(t, path, "user.dev-configedit-test", []byte("retained"))
	simulateKernelProvenance(t)
	setAttribute = func(int, string, []byte) error { return nil }
	plan, err := New(t.Context(), []string{path}, [][]byte{[]byte("after")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), plan, filepath.Join(dir, "recovery")); err == nil || !strings.Contains(err.Error(), "did not round-trip") {
		t.Fatalf("ignored ordinary attribute was not rejected: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "before" {
		t.Fatal("failed metadata preparation replaced the source")
	}
}

func TestKernelProvenanceStillBindsSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	put(t, path, "before")
	setTestAttribute(t, path, testProvenance, []byte("first-writer"))
	simulateKernelProvenance(t)
	plan, err := New(t.Context(), []string{path}, [][]byte{[]byte("after")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setTestAttribute(t, path, testProvenance, []byte("second-writer"))
	if err := plan.Check(t.Context()); !errors.Is(err, ErrStale) {
		t.Fatalf("changed source provenance was not stale: %v", err)
	}
}

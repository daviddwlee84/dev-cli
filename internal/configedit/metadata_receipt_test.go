//go:build linux || darwin

package configedit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

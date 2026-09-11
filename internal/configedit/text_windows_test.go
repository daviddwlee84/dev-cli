//go:build windows

package configedit

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPortableTextWindowsPreservesDescriptorAndRestores(t *testing.T) {
	root := t.TempDir()
	if p, e := filepath.EvalSymlinks(root); e == nil {
		root = p
	}
	path := filepath.Join(root, "data.txt")
	if e := os.WriteFile(path, []byte("before"), 0o600); e != nil {
		t.Fatal(e)
	}
	before, _, err := securityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewTextFile(t.Context(), path, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(t.TempDir(), "recovery")
	if p, e := filepath.EvalSymlinks(filepath.Dir(recovery)); e == nil {
		recovery = filepath.Join(p, "recovery")
	}
	result, err := Apply(t.Context(), plan, recovery)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := securityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("source descriptor changed")
	}
	restore, err := RestorePlan(t.Context(), recovery, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(t.Context(), restore, recovery); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "before" {
		t.Fatal("restore failed")
	}
	if err := privateWindowsReceipt(recovery, result.Receipt); err != nil {
		t.Fatal(err)
	}
}
func privateWindowsReceipt(dir, id string) error {
	p := filepath.Join(dir, id+".json")
	sd, _, err := securityAt(p)
	if err != nil {
		return err
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		return err
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		return os.ErrPermission
	}
	return nil
}
func TestPortableTextWindowsRejectsHardlinkAndStaleDescriptor(t *testing.T) {
	root := t.TempDir()
	if p, e := filepath.EvalSymlinks(root); e == nil {
		root = p
	}
	path := filepath.Join(root, "data")
	if e := os.WriteFile(path, []byte("before"), 0o600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "alias")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTextFile(t.Context(), path, []byte("after")); err == nil {
		t.Fatal("hardlink accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	plan, err := NewTextFile(t.Context(), path, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err = plan.Check(t.Context()); err == nil {
		t.Fatal("changed unsafe descriptor accepted")
	}
}

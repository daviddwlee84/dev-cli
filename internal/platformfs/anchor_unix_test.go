//go:build unix

package platformfs

import (
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type anchorInfo struct {
	mode fs.FileMode
	stat syscall.Stat_t
}

func (f anchorInfo) Name() string       { return "fixture" }
func (f anchorInfo) Size() int64        { return 0 }
func (f anchorInfo) Mode() fs.FileMode  { return f.mode }
func (f anchorInfo) ModTime() time.Time { return time.Time{} }
func (f anchorInfo) IsDir() bool        { return f.mode.IsDir() }
func (f anchorInfo) Sys() any           { return &f.stat }

func termuxFixture(container string, uid int) (map[string]anchorInfo, func(string) (fs.FileInfo, error)) {
	paths := []string{"/", "/data", "/data/user", container, container + "/com.termux", container + "/com.termux/files", container + "/com.termux/files/home"}
	files := map[string]anchorInfo{}
	for i, path := range paths {
		owner, mode := uint32(1000), fs.FileMode(0o771)
		switch {
		case path == "/":
			owner, mode = 0, 0o755
		case strings.HasSuffix(path, "/com.termux"), strings.HasSuffix(path, "/home"):
			owner, mode = uint32(uid), 0o700
		case strings.HasSuffix(path, "/files"):
			owner = uint32(uid)
		}
		files[path] = anchorInfo{mode: os.ModeDir | mode, stat: syscall.Stat_t{Uid: owner, Gid: owner, Ino: uint64(i + 1)}}
	}
	return files, func(path string) (fs.FileInfo, error) {
		if info, ok := files[path]; ok {
			return info, nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestTermuxAnchorVerifiesPrivateHomeAndSystemContainers(t *testing.T) {
	for _, test := range []struct {
		container string
		uid       int
	}{{"/data/data", 10123}, {"/data/user/0", 10123}, {"/data/user/10", 1010123}} {
		t.Run(test.container, func(t *testing.T) {
			_, lstat := termuxFixture(test.container, test.uid)
			home := test.container + "/com.termux/files/home"
			anchor, err := resolveTermux(home+"/.ssh/config", test.uid, lstat)
			if err != nil || anchor.Path != home || anchor.Verify() != nil {
				t.Fatalf("anchor=%+v err=%v", anchor, err)
			}
		})
	}
}

func TestTermuxAnchorRejectsUnsafePrefixAndRevalidates(t *testing.T) {
	app := "/data/data/com.termux"
	home := app + "/files/home"
	for _, path := range []string{"/", "/data", "/data/data", app, app + "/files", home} {
		for _, change := range []string{"symlink", "owner", "group", "world-write", "identity", "missing"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				files, lstat := termuxFixture("/data/data", 10123)
				anchor, err := resolveTermux(home+"/.ssh/config", 10123, lstat)
				if err != nil {
					t.Fatal(err)
				}
				info := files[path]
				switch change {
				case "symlink":
					info.mode = os.ModeSymlink | 0o777
				case "owner":
					info.stat.Uid = 10999
				case "group":
					info.stat.Gid = 10999
				case "world-write":
					info.mode |= 0o002
				case "identity":
					info.stat.Ino++
				}
				files[path] = info
				if change == "missing" {
					delete(files, path)
				}
				if anchor.Verify() == nil {
					t.Fatal("changed prefix accepted")
				}
				if change != "identity" {
					if _, err := resolveTermux(home+"/.ssh/config", 10123, lstat); err == nil {
						t.Fatal("unsafe prefix accepted by a new resolution")
					}
				}
			})
		}
	}
}

func TestTermuxAnchorDoesNotTrustOtherPathsOrUsers(t *testing.T) {
	_, lstat := termuxFixture("/data/data", 10123)
	for _, uid := range []int{0, 1000, 10123, 1010123} {
		for _, path := range []string{"/data/other/file", "/data/data/com.other/files/home/x", "/data/data/com.termux/files/home-other/x", "/sdcard/x", "/data/data/com.termux/files/usr/x"} {
			t.Run(strconv.Itoa(uid)+path, func(t *testing.T) {
				anchor, err := resolveTermux(path, uid, lstat)
				if err != nil || anchor.Path != "/" {
					t.Fatalf("exception escaped Termux home: %+v %v", anchor, err)
				}
			})
		}
	}
	if _, err := resolveTermux("/data/data/com.termux/files/home/../usr/x", 10123, lstat); err == nil {
		t.Fatal("unclean path accepted")
	}
}

func TestTermuxAnchorRequiresPrivateHomeEvenAsDirectTarget(t *testing.T) {
	files, lstat := termuxFixture("/data/data", 10123)
	home := "/data/data/com.termux/files/home"
	info := files[home]
	info.mode = os.ModeDir | 0o755
	files[home] = info
	if _, err := resolveTermux(home, 10123, lstat); err == nil {
		t.Fatal("public home became a private registry/cache anchor")
	}
}

func TestAndroidKernelLabelIsNarrowAndUnavailableOnDesktop(t *testing.T) {
	label := []byte("u:object_r:app_data_file:s0:c1,c256,c512,c768\x00")
	if !kernelLabel("android", "security.selinux", label) {
		t.Fatal("native inherited app-data label rejected")
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if kernelLabel(goos, "security.selinux", label) {
			t.Fatal("Android exception enabled on desktop")
		}
	}
	for _, name := range []string{"system.posix_acl_access", "security.capability", "user.selinux"} {
		if kernelLabel("android", name, label) {
			t.Fatal("unrelated security attribute accepted")
		}
	}
	for _, label := range []string{"", "u:object_r:system_file:s0", "u:object_r:app_data_file:s0\n", "u:object_r:app_data_file:s0:c1\x00extra"} {
		if kernelLabel("android", "security.selinux", []byte(label)) {
			t.Fatalf("unexpected label accepted: %q", label)
		}
	}
}

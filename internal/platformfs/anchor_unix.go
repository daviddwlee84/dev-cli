//go:build unix

package platformfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Android app UID ranges: https://android.googlesource.com/platform/system/core/+/master/libcutils/include/private/android_filesystem_config.h
//
// Resolve uses the ordinary filesystem root except for a native Android app's
// verified Termux home. Environment variables cannot expand this exception.
func Resolve(path string) (Anchor, error) {
	if runtime.GOOS != "android" {
		return filesystemRoot(path), nil
	}
	return resolveTermux(path, os.Geteuid(), os.Lstat)
}

func resolveTermux(path string, uid int, lstat func(string) (fs.FileInfo, error)) (Anchor, error) {
	fallback := filesystemRoot(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Anchor{}, errors.New("filesystem anchor requires a clean absolute path")
	}
	if uid%100000 < 10000 || uid%100000 > 19999 {
		return fallback, nil
	}
	userRoot := "/data/user/" + strconv.Itoa(uid/100000)
	containers := []string{userRoot}
	if uid/100000 == 0 {
		containers = append(containers, "/data/data")
	}
	for _, container := range containers {
		app := container + "/com.termux"
		home := app + "/files/home"
		if path != home && !strings.HasPrefix(path, home+"/") {
			continue
		}
		paths := []string{"/", "/data"}
		if container == userRoot {
			paths = append(paths, "/data/user")
		}
		paths = append(paths, container, app, app+"/files", home)
		observed := make([]fs.FileInfo, len(paths))
		for i, p := range paths {
			info, err := lstat(p)
			if err != nil {
				return Anchor{}, fmt.Errorf("inspect Termux anchor %s: %w", p, err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			valid := ok && info.IsDir() && info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
			if valid {
				switch p {
				case app:
					valid = int(stat.Uid) == uid && int(stat.Gid) == uid && info.Mode().Perm() == 0o700
				case app + "/files":
					// This fixed app-owned directory can be group-writable;
					// the enclosing 0700 app root excludes every other UID.
					valid = int(stat.Uid) == uid && int(stat.Gid) == uid && info.Mode().Perm()&0o002 == 0
				case home:
					valid = int(stat.Uid) == uid && int(stat.Gid) == uid && info.Mode().Perm() == 0o700
				case "/":
					valid = stat.Uid == 0 && stat.Gid == 0 && info.Mode().Perm()&0o022 == 0
				default:
					valid = (stat.Uid == 0 || stat.Uid == 1000) && (stat.Gid == 0 || stat.Gid == 1000) && info.Mode().Perm()&0o002 == 0
				}
			}
			if !valid {
				return Anchor{}, fmt.Errorf("unsafe Termux filesystem anchor %s", p)
			}
			observed[i] = info
		}
		anchor := Anchor{Path: home, verify: func() error {
			for i, p := range paths {
				now, err := lstat(p)
				if err != nil {
					return fmt.Errorf("Termux anchor changed at %s: %w", p, err)
				}
				before, bok := observed[i].Sys().(*syscall.Stat_t)
				after, aok := now.Sys().(*syscall.Stat_t)
				if !bok || !aok || before.Dev != after.Dev || before.Ino != after.Ino || observed[i].Mode() != now.Mode() || before.Uid != after.Uid || before.Gid != after.Gid {
					return fmt.Errorf("Termux anchor changed at %s", p)
				}
			}
			return nil
		}}
		return anchor, anchor.Verify()
	}
	return fallback, nil
}

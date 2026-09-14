//go:build darwin || linux

package sshvault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeACLDirectoryChurnKeepsSecuritySnapshot(t *testing.T) {
	directory := t.TempDir()
	before, err := os.Lstat(directory)
	if err != nil {
		t.Fatal("stat owned directory fixture")
	}
	acl, err := captureNativeACL(directory, before)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if os.Mkdir(filepath.Join(directory, "unrelated-child"), 0o700) != nil {
		t.Fatal("create only unrelated owned sibling")
	}
	after, err := os.Lstat(directory)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !nativeSameOwner(before, after) {
		t.Fatal("sibling fixture changed directory authority")
	}
	if nativeSameChangeTime(before, after) {
		t.Skip("filesystem did not expose the directory ctime transition")
	}
	observed, err := captureNativeACL(directory, before)
	if err != nil || observed != acl {
		t.Fatalf("unrelated child creation invalidated unchanged directory security: %v", err)
	}
}

func TestNativeACLRegularFileChangeTimeRemainsGuarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-file")
	if os.WriteFile(path, []byte("public fixture"), 0o600) != nil {
		t.Fatal("create owned file fixture")
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal("stat owned file fixture")
	}
	time.Sleep(5 * time.Millisecond)
	if os.Chmod(path, 0o700) != nil || os.Chmod(path, 0o600) != nil {
		t.Fatal("change and restore only owned fixture mode")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("file fixture identity was not preserved")
	}
	if nativeSameChangeTime(before, after) {
		t.Skip("filesystem did not expose the file ctime transition")
	}
	if _, err := captureNativeACL(path, before); !errors.Is(err, ErrStale) {
		t.Fatal("regular file ctime protection was weakened")
	}
}

func TestNativeACLDirectoryIdentityAndModeRemainGuarded(t *testing.T) {
	for _, change := range []string{"replacement", "mode"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reviewed")
			if os.Mkdir(path, 0o700) != nil {
				t.Fatal("create owned directory fixture")
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal("stat owned directory fixture")
			}
			if change == "replacement" {
				if os.Rename(path, path+"-retained") != nil || os.Mkdir(path, 0o700) != nil {
					t.Fatal("replace only owned directory fixture")
				}
			} else if os.Chmod(path, 0o500) != nil {
				t.Fatal("tighten only owned fixture mode")
			}
			if _, err := captureNativeACL(path, before); !errors.Is(err, ErrStale) {
				t.Fatal("directory identity or mode source guard was weakened")
			}
		})
	}
}

func TestBitwardenNativeContextToleratesUnrelatedSiblingChurn(t *testing.T) {
	fixture := newNativeFixture(t, false)
	stop, done := make(chan struct{}), make(chan struct{})
	failures := make(chan error, 1)
	sibling := filepath.Join(fixture.root, "unrelated-sibling")
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.Mkdir(sibling, 0o700); err != nil {
				failures <- err
				return
			}
			if err := os.Remove(sibling); err != nil {
				failures <- err
				return
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
		select {
		case <-failures:
			t.Error("owned sibling churn fixture failed")
		default:
		}
	})
	service, _ := countedNativeService(nil)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatalf("unchanged native authority rejected during sibling churn: %v", err)
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil || !CanSelectAgentKey(plan, result) {
		t.Fatalf("sibling churn made unchanged native context uncertain: %v", err)
	}
}

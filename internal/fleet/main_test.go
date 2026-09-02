package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

const managedFragmentConflictExitCode = 23

func TestMain(m *testing.M) {
	if os.Getenv("DEV_FLEET_MANAGED_TEST_HELPER") == "1" {
		os.Exit(runManagedFragmentTestHelper())
	}
	if handled, code := MaybeServeAskpass(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func runManagedFragmentTestHelper() int {
	primary := os.Getenv("DEV_FLEET_MANAGED_TEST_PRIMARY")
	name := os.Getenv("DEV_FLEET_MANAGED_TEST_NAME")
	if primary == "" || name == "" {
		fmt.Fprintln(os.Stderr, "managed fragment test helper requires primary and name")
		return 2
	}
	if marker := os.Getenv("DEV_FLEET_MANAGED_TEST_SNAPSHOT_MARKER"); marker != "" {
		managedFragmentBeforeLock = func(string) {
			if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
				fmt.Fprintf(os.Stderr, "write snapshot marker: %v\n", err)
			}
		}
	}
	var writer ManagedFragmentWriter
	if held := os.Getenv("DEV_FLEET_MANAGED_TEST_HELD_MARKER"); held != "" {
		release := os.Getenv("DEV_FLEET_MANAGED_TEST_RELEASE_MARKER")
		writer = ManagedFragmentWriteFunc(func(request ManagedFragmentWriteRequest) error {
			if err := os.WriteFile(held, []byte("held"), 0o600); err != nil {
				return err
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(release); err == nil {
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if time.Now().After(deadline) {
					return errors.New("timed out waiting for managed fragment test release")
				}
				time.Sleep(5 * time.Millisecond)
			}
			return writeManagedFragmentOS(request)
		})
	}
	_, err := WriteManagedFragment(context.Background(), primary, ManagedHost{
		Name: name, SSHAlias: "lab", RemoteOS: RemoteOSPOSIX,
	}, writer)
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, err)
	if errors.Is(err, ErrManagedFragmentConflict) {
		return managedFragmentConflictExitCode
	}
	return 1
}

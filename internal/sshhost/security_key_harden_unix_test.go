//go:build unix

package sshhost

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSecurityKeyHardeningNeverMutatesChangedCapturedFiles(t *testing.T) {
	for _, public := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			name := map[bool]string{false: "identity", true: "public"}[public] + "/" + map[bool]string{false: "rewrite-restored-mtime", true: "replacement"}[replace]
			t.Run(name, func(t *testing.T) {
				f := newSecurityKeyFixture(t)
				f.generate = func(request RunRequest) (RunResult, error) {
					path := sshArgForCatalogTest(request.Args, "-f")
					writeFixture(t, path, strings.Repeat("S", 80))
					writeFixture(t, path+".pub", string(f.line)+"\n")
					for _, p := range []string{path, path + ".pub"} {
						if err := os.Chmod(p, 0o644); err != nil {
							t.Fatal(err)
						}
					}
					return RunResult{}, nil
				}
				var changedPath, changedData string
				f.service.beforeSecurityKeyHarden = func(path string) {
					if strings.HasSuffix(path, ".pub") != public {
						return
					}
					before, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					changedPath, changedData = path, strings.Repeat("X", int(before.Size()))
					if replace {
						if err := os.Rename(path, path+".original"); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(path, []byte(changedData), 0o644); err != nil {
						t.Fatal(err)
					}
					if !replace {
						if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
							t.Fatal(err)
						}
					}
				}
				result, err := f.service.ApplyKey(t.Context(), f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{}))
				if !errors.Is(err, ErrSourceChanged) || result.Hardware.RecoveryIdentityPath != "" || result.Hardware.RecoveryPublicPath != "" {
					t.Fatalf("changed source was not rejected: %+v %v", result, err)
				}
				info, err := os.Stat(changedPath)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("foreign source permissions changed: %+v %v", info, err)
				}
				data, err := os.ReadFile(changedPath)
				if err != nil || string(data) != changedData {
					t.Fatalf("foreign source contents changed or removed: %v", err)
				}
				if len(f.requests) != 1 {
					t.Fatalf("stale source reached derivation: %+v", f.requests)
				}
			})
		}
	}
}

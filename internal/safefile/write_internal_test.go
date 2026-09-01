package safefile

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCreateNoClobberRollsBackPostPublicationFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		hooks func() createNoClobberHooks
	}{
		{
			name: "staging link cleanup",
			hooks: func() createNoClobberHooks {
				return createNoClobberHooks{removeStage: func(*os.Root, string) error {
					return errors.New("injected staging cleanup failure")
				}}
			},
		},
		{
			name: "directory sync",
			hooks: func() createNoClobberHooks {
				calls := 0
				return createNoClobberHooks{syncRoot: func(*os.Root) error {
					calls++
					if calls == 1 {
						return errors.New("injected directory sync failure")
					}
					return nil
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err := createNoClobberWithHooks(context.Background(), root, "secret", []byte("value"), 0o600, test.hooks()); err == nil {
				t.Fatal("post-publication failure was reported as success")
			}
			if _, err := root.Lstat("secret"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed create left destination behind: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed create left staging entries: %v", entries)
			}
		})
	}
}

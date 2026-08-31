package forge

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkLoadCacheAny(b *testing.B) {
	for _, count := range []int{56, 500} {
		b.Run(fmt.Sprintf("repos-%d", count), func(b *testing.B) {
			repositories := make([]RemoteRepo, count)
			for index := range repositories {
				repositories[index] = RemoteRepo{
					Forge: GitHub, Name: fmt.Sprintf("repo-%04d", index),
					FullName: fmt.Sprintf("owner/repo-%04d", index),
					CloneURL: fmt.Sprintf("git@github.com:owner/repo-%04d.git", index),
				}
			}
			path := filepath.Join(b.TempDir(), "remotes.json")
			if err := SaveCache(path, repositories); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, ok := LoadCacheAny(path); !ok {
					b.Fatal("cache unexpectedly missed")
				}
			}
		})
	}
}

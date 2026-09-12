package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"go.yaml.in/yaml/v3"
)

// A newly installed common-dir hook runs in every linked checkout. Require a
// readable pre-commit configuration in siblings before changing that shared
// execution path. Existing recognized hooks are retained, not reinstalled.
func (s *Service) sharedHookToken(ctx context.Context) (string, error) {
	worktrees, err := gitx.Worktrees(ctx, s.Root)
	if err != nil {
		return "", errors.New("cannot inspect shared hook checkouts")
	}
	parts := []string{}
	for _, w := range worktrees {
		if filepath.Clean(w.Path) == filepath.Clean(s.Root) || w.Bare {
			continue
		}
		id, err := gitx.DirectoryIdentity(w.Path)
		if err != nil {
			return "", errors.New("shared hook has an unavailable sibling checkout")
		}
		data, err := safefile.ReadStablePath(ctx, filepath.Join(w.Path, ".pre-commit-config.yaml"), 1<<20)
		if err != nil {
			return "", errors.New("shared hook would affect a sibling without a readable pre-commit configuration; configure siblings first")
		}
		var doc yaml.Node
		if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 {
			return "", errors.New("sibling pre-commit configuration needs manual review")
		}
		repos := mapping(doc.Content[0], "repos")
		if repos == nil || repos.Kind != yaml.SequenceNode {
			return "", errors.New("sibling pre-commit configuration needs manual review")
		}
		parts = append(parts, w.Path, id, digest(data))
	}
	data, _ := json.Marshal(parts)
	return digest(data), nil
}

func (s *Service) checkSharedHook(ctx context.Context, want string) error {
	if want == "" {
		return nil
	}
	got, err := s.sharedHookToken(ctx)
	if err != nil || got != want {
		return ErrStale
	}
	return nil
}

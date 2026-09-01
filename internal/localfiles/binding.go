package localfiles

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// FetchRemoteIdentity resolves the one normalized fetch identity used to bind a
// portable-file operation. Push URLs are intentionally excluded because they
// may name a publication mirror unrelated to the fetched repository.
func FetchRemoteIdentity(ctx context.Context, checkout, branch string) (string, error) {
	var preferred []string
	if remote, err := gitx.Run(ctx, checkout, "config", "--get", "branch."+branch+".remote"); err == nil && remote != "" && remote != "." {
		preferred = append(preferred, remote)
	}
	preferred = append(preferred, "origin")
	remotes, err := gitx.Run(ctx, checkout, "remote")
	if err != nil {
		return "", err
	}
	preferred = append(preferred, strings.Fields(remotes)...)
	seenRemote := map[string]bool{}
	for _, remote := range preferred {
		if remote == "" || seenRemote[remote] {
			continue
		}
		seenRemote[remote] = true
		urls, err := gitx.Run(ctx, checkout, "remote", "get-url", "--all", remote)
		if err != nil {
			continue
		}
		identities := map[string]struct{}{}
		for _, raw := range strings.Split(urls, "\n") {
			if identity := catalog.NormalizeRemoteIdentity(raw); identity != "" {
				identities[identity] = struct{}{}
			}
		}
		if len(identities) == 1 {
			for identity := range identities {
				return identity, nil
			}
		}
	}
	return "", errors.New("checkout has no unambiguous normalized fetch remote identity")
}

func verifySourceBinding(ctx context.Context, checkout string, binding Binding) error {
	status, err := gitx.StatusOf(ctx, checkout)
	if err != nil {
		return err
	}
	if status.Detached || status.Branch != binding.Branch {
		return fmt.Errorf("source branch changed from %s: %w", binding.Branch, ErrDrift)
	}
	head, err := gitx.Run(ctx, checkout, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != binding.HeadOID {
		return fmt.Errorf("source HEAD changed from %s: %w", binding.HeadOID, ErrDrift)
	}
	identity, err := FetchRemoteIdentity(ctx, checkout, binding.Branch)
	if err != nil {
		return err
	}
	if identity != binding.RemoteIdentity {
		return fmt.Errorf("source fetch identity changed: %w", ErrDrift)
	}
	return nil
}

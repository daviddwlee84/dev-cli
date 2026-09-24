// Package prflow owns reviewed pull-request operations shared by CLI and UI.
package prflow

import (
	"context"
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/forge"
)

type Reference = forge.PRReference
type Detail = forge.PRDetailResult
type Query = forge.PRPageQuery
type Page = forge.PRPage
type Diff = forge.PRDiff
type Size = forge.PRSize
type Provider = forge.PRProvider

func ParseReference(raw string) (Reference, error) { return forge.ParsePRReference(raw) }

type Service struct {
	Provider Provider
	StateDir string
}

func (s *Service) ListPage(ctx context.Context, q Query) (Page, error) {
	if s.Provider == nil {
		return Page{}, errors.New("pull request provider is unavailable")
	}
	return s.Provider.ListPage(ctx, q)
}
func (s *Service) Detail(ctx context.Context, r Reference) (Detail, error) {
	if s.Provider == nil {
		return Detail{}, errors.New("pull request provider is unavailable")
	}
	return s.Provider.Detail(ctx, r)
}
func (s *Service) Diff(ctx context.Context, d Detail) (Diff, error) {
	if s.Provider == nil {
		return Diff{}, errors.New("pull request provider is unavailable")
	}
	return s.Provider.Diff(ctx, d)
}

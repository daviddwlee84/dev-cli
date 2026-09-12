//go:build !darwin && !linux && !windows

package sshcredential

import "context"

type nativeSystemProvider struct{}

func (nativeSystemProvider) ID() string                      { return "system" }
func (nativeSystemProvider) Available(context.Context) error { return ErrUnavailable }
func (nativeSystemProvider) Get(context.Context, Context, Reference) ([]byte, error) {
	return nil, ErrUnavailable
}
func (nativeSystemProvider) Put(context.Context, Context, *Reference, []byte) (Reference, error) {
	return Reference{}, ErrUnavailable
}
func (nativeSystemProvider) Delete(context.Context, Context, Reference) error { return ErrUnavailable }

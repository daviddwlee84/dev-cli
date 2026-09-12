//go:build windows

package sshcredential

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"net"
	"strings"
)

func listenBroker() (net.Listener, string, func(), error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, "", nil, err
	}
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, "", nil, err
	}
	path := `\\.\pipe\dev-ssh-askpass-` + hex.EncodeToString(nonce[:])
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + u.User.Sid.String() + ")(A;;GA;;;SY)", InputBufferSize: 8192, OutputBufferSize: MaxSecretBytes + 4})
	return l, path, func() {}, err
}
func dialBroker(ctx context.Context, path string) (net.Conn, error) {
	if !strings.HasPrefix(path, `\\.\pipe\dev-ssh-askpass-`) {
		return nil, ErrDenied
	}
	return winio.DialPipeContext(ctx, path)
}
func brokerPeerAllowed(net.Conn) error { return nil } // protected DACL; go-winio rejects remote clients

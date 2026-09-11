package sshcredential

import (
	"context"
	"net"
)

// ListenPrivate and DialPrivate are shared transport boundaries for bounded
// operation metadata and askpass. They authorize the current local user, not
// remote authentication. Callers own framing, deadlines and operation guards.
func ListenPrivate() (net.Listener, string, func(), error) { return listenBroker() }
func DialPrivate(ctx context.Context, endpoint string) (net.Conn, error) {
	return dialBroker(ctx, endpoint)
}
func VerifyPrivatePeer(conn net.Conn) error { return brokerPeerAllowed(conn) }

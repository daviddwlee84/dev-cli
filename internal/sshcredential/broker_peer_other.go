//go:build unix && !darwin && !linux

package sshcredential

import "net"

func brokerPeerAllowed(net.Conn) error { return ErrUnavailable }

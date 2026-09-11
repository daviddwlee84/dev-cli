package fleet

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

func (t Transport) resolveStoredPassword(ctx context.Context, host Host) (string, error) {
	if host.PasswordKind() != "none" || t.StoredPassword == nil {
		return "", nil
	}
	secret, err := t.StoredPassword(ctx, host)
	defer sshcredential.Wipe(secret)
	if errors.Is(err, sshcredential.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", sshcredential.ErrUnavailable
	}
	if len(secret) > sshcredential.MaxSecretBytes {
		return "", sshcredential.ErrUnsafe
	}
	return string(secret), nil
}

func resolveAskpassContext(ctx context.Context, host Host) (sshcredential.PasswordContext, error) {
	// Explicit user+hostname declarations are already exact. Otherwise ask
	// OpenSSH for effective values without making a network connection.
	user, name, port := host.User, host.Hostname, host.Port
	if host.SSHAlias != "" || user == "" || name == "" {
		args := append([]string{"-G"}, sshArgs(host, false, false)...)
		args = append(args, host.Destination())
		cmd := exec.CommandContext(ctx, "ssh", args...)
		captured := newCaptureBuffer(2 << 20)
		cmd.Stdout = captured
		cmd.Stderr = io.Discard
		err := cmd.Run()
		out := captured.Bytes()
		if err != nil || captured.Exceeded() {
			return sshcredential.PasswordContext{}, sshcredential.ErrDenied
		}
		for _, line := range strings.Split(string(out), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok {
				continue
			}
			switch key {
			case "user":
				user = strings.TrimSpace(value)
			case "hostname":
				name = strings.TrimSpace(value)
			case "port":
				port, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
	}
	if port == 0 {
		port = 22
	}
	if user == "" || name == "" {
		return sshcredential.PasswordContext{}, sshcredential.ErrDenied
	}
	names := []string{name}
	if host.SSHAlias != "" && host.SSHAlias != name {
		names = append(names, host.SSHAlias)
	}
	return sshcredential.PasswordContext{ID: EndpointID(host), User: user, HostNames: names, Port: port}, nil
}

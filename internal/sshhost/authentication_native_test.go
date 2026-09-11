package sshhost

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

type nativePasswordServer struct {
	listener        net.Listener
	signer          cryptossh.Signer
	forward         string
	mu              sync.Mutex
	connections     map[net.Conn]bool
	authentications atomic.Int64
	commands        atomic.Int64
}

func startNativePasswordServer(t *testing.T, password, forward string) *nativePasswordServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := cryptossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &nativePasswordServer{listener: listener, signer: signer, forward: forward, connections: map[net.Conn]bool{}}
	config := &cryptossh.ServerConfig{PasswordCallback: func(metadata cryptossh.ConnMetadata, received []byte) (*cryptossh.Permissions, error) {
		if metadata.User() != "fixture-user" || string(received) != password {
			return nil, fmt.Errorf("fixture authentication denied")
		}
		server.authentications.Add(1)
		return nil, nil
	}}
	config.AddHostKey(signer)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.connections[conn] = true
			server.mu.Unlock()
			go server.serve(conn, config)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		for conn := range server.connections {
			_ = conn.Close()
		}
		server.mu.Unlock()
	})
	return server
}
func (server *nativePasswordServer) serve(conn net.Conn, config *cryptossh.ServerConfig) {
	defer conn.Close()
	defer func() { server.mu.Lock(); delete(server.connections, conn); server.mu.Unlock() }()
	_, channels, requests, err := cryptossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go cryptossh.DiscardRequests(requests)
	for incoming := range channels {
		switch incoming.ChannelType() {
		case "session":
			channel, requests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go server.session(channel, requests)
		case "direct-tcpip":
			var target struct {
				Address    string
				Port       uint32
				Origin     string
				OriginPort uint32
			}
			if cryptossh.Unmarshal(incoming.ExtraData(), &target) != nil || net.JoinHostPort(target.Address, strconv.Itoa(int(target.Port))) != server.forward {
				_ = incoming.Reject(cryptossh.Prohibited, "unplanned fixture endpoint")
				continue
			}
			upstream, err := net.Dial("tcp", server.forward)
			if err != nil {
				_ = incoming.Reject(cryptossh.ConnectionFailed, "fixture unavailable")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				_ = upstream.Close()
				continue
			}
			go cryptossh.DiscardRequests(requests)
			go func() {
				defer channel.Close()
				defer upstream.Close()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(channel, upstream); _ = channel.CloseWrite(); close(done) }()
				_, _ = io.Copy(upstream, channel)
				if tcp, ok := upstream.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
				<-done
			}()
		default:
			_ = incoming.Reject(cryptossh.UnknownChannelType, "unsupported fixture channel")
		}
	}
}
func (server *nativePasswordServer) session(channel cryptossh.Channel, requests <-chan *cryptossh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var command struct{ Command string }
		if cryptossh.Unmarshal(request.Payload, &command) != nil {
			return
		}
		_ = request.Reply(true, nil)
		status := uint32(0)
		switch command.Command {
		case "exit 0":
		case "consume":
			server.commands.Add(1)
			data, err := io.ReadAll(io.LimitReader(channel, 4<<20))
			if err != nil {
				status = 1
			} else {
				sum := sha256.Sum256(data)
				_, _ = fmt.Fprintf(channel, "%d:%x", len(data), sum)
			}
		case "exit 255":
			server.commands.Add(1)
			status = 255
		case "wait":
			server.commands.Add(1)
			for range requests {
			}
			return
		default:
			status = 126
		}
		_, _ = channel.SendRequest("exit-status", false, cryptossh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

type nativeFixtureConfigRunner struct{ config string }

func (r nativeFixtureConfigRunner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if len(request.Args) > 0 && request.Args[0] == "-G" {
		request.Args = append([]string{"-F", r.config}, request.Args...)
	}
	return (ExecRunner{}).Run(ctx, request)
}

func TestNativeTwoHopPasswordsUseIsolatedIPCAndStreamLargeInput(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("native OpenSSH unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	target := startNativePasswordServer(t, "target fixture password", "")
	jump := startNativePasswordServer(t, "jump fixture password", target.listener.Addr().String())
	paths := fixturePaths(t)
	known := filepath.Join(paths.SSHDir, "known_hosts_fixture")
	var hosts strings.Builder
	for _, server := range []*nativePasswordServer{jump, target} {
		_, port, _ := net.SplitHostPort(server.listener.Addr().String())
		fmt.Fprintf(&hosts, "[127.0.0.1]:%s %s", port, cryptossh.MarshalAuthorizedKey(server.signer.PublicKey()))
	}
	writeFixture(t, known, hosts.String())
	_, jumpPort, _ := net.SplitHostPort(jump.listener.Addr().String())
	_, targetPort, _ := net.SplitHostPort(target.listener.Addr().String())
	config := fmt.Sprintf("Host *\n User fixture-user\n StrictHostKeyChecking yes\n UserKnownHostsFile %s\n GlobalKnownHostsFile none\n IdentityFile none\n CertificateFile none\n IdentityAgent none\n PubkeyAuthentication no\n KbdInteractiveAuthentication no\n PasswordAuthentication yes\nHost jump\n HostName 127.0.0.1\n Port %s\nHost target\n HostName 127.0.0.1\n Port %s\n ProxyJump jump\n", quoteConfigValue(known), jumpPort, targetPort)
	writeFixture(t, paths.RootConfig, config)
	s, err := NewService(paths, nativeFixtureConfigRunner{paths.RootConfig})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.PrepareAuthentication(ctx, "target")
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	// ProxyCommand and askpass must support an executable path containing spaces.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "helper with spaces")
	if filepath.Ext(executable) == ".exe" {
		helper += ".exe"
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, data, 0o700); err != nil {
		t.Fatal(err)
	}
	op.executable = helper
	for index, password := range []string{"jump fixture password", "target fixture password"} {
		probe, err := op.ProbeHop(ctx, index)
		if err != nil || probe.Ready || !probe.HostTrusted || !probe.PasswordAvailable {
			t.Fatalf("hop%d probe=%+v error=%v", index, probe, err)
		}
		proof, err := op.ProvePassword(ctx, index, []byte(password))
		if err != nil || !proof.Verified {
			t.Fatalf("hop%d proof=%+v error=%v", index, proof, err)
		}
	}
	payload := bytes.Repeat([]byte("binary\x00payload\xff"), 180000)
	wantHash := sha256.Sum256(payload)
	result, err := op.Run(ctx, ConnectionOptions{Args: []string{"consume"}, Stdin: payload, SuppressForwarding: true})
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != fmt.Sprintf("%d:%x", len(payload), wantHash) || target.commands.Load() != 1 {
		t.Fatalf("stream result exit=%d stdout=%q stderr=%s commands=%d err=%v", result.ExitCode, result.Stdout, result.Stderr, target.commands.Load(), err)
	}
	if jump.authentications.Load() < 3 || target.authentications.Load() != 2 {
		t.Fatalf("hops did not freshly authenticate: jump=%d target=%d", jump.authentications.Load(), target.authentications.Load())
	}
	newProvenOperation := func() *AuthenticationOperation {
		next, err := s.PrepareAuthentication(ctx, "target")
		if err != nil {
			t.Fatal(err)
		}
		next.executable = helper
		t.Cleanup(func() { _ = next.Close() })
		for index, password := range []string{"jump fixture password", "target fixture password"} {
			probe, err := next.ProbeHop(ctx, index)
			if err != nil || !probe.HostTrusted {
				t.Fatal(probe, err)
			}
			if proof, err := next.ProvePassword(ctx, index, []byte(password)); err != nil || !proof.Verified {
				t.Fatal(proof, err)
			}
		}
		return next
	}
	t.Run("interactive cancellation closes all hops", func(t *testing.T) {
		next := newProvenOperation()
		runCtx, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan error, 1)
		go func() {
			_, err := next.Run(runCtx, ConnectionOptions{Args: []string{"wait"}, Interactive: true, SuppressForwarding: true})
			done <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for target.commands.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if target.commands.Load() != 2 {
			t.Fatal("native target did not start wait command")
		}
		stop()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancellation reported success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("connector tree did not cancel")
		}
		deadline = time.Now().Add(5 * time.Second)
		for {
			jump.mu.Lock()
			jumpOpen := len(jump.connections)
			jump.mu.Unlock()
			target.mu.Lock()
			targetOpen := len(target.connections)
			target.mu.Unlock()
			if jumpOpen == 0 && targetOpen == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("orphan SSH connections: jump=%d target=%d", jumpOpen, targetOpen)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("remote exit255 is never replayed", func(t *testing.T) {
		next := newProvenOperation()
		result, err := next.Run(ctx, ConnectionOptions{Args: []string{"exit 255"}, SuppressForwarding: true})
		if err != nil || result.ExitCode != 255 || target.commands.Load() != 3 {
			t.Fatal(result.ExitCode, err, target.commands.Load())
		}
	})
	t.Run("HostKeyAlias scopes native password prompt", func(t *testing.T) {
		alias := "fixture-jump-host-key"
		writeFixture(t, known, hosts.String()+alias+" "+string(cryptossh.MarshalAuthorizedKey(jump.signer.PublicKey())))
		writeFixture(t, paths.RootConfig, "Host jump\n HostKeyAlias "+alias+"\n"+config)
		next, err := s.PrepareAuthentication(ctx, "target")
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		next.executable = helper
		probe, err := next.ProbeHop(ctx, 0)
		if err != nil || !probe.HostTrusted {
			t.Fatal(probe, err)
		}
		proof, err := next.ProvePassword(ctx, 0, []byte("jump fixture password"))
		if err != nil || !proof.Verified {
			t.Fatal(proof, err)
		}
	})
	entries, err := os.ReadDir(paths.SSHDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dev-") {
			t.Fatalf("operation staging survived: %s", entry.Name())
		}
	}
}

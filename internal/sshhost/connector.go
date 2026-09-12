package sshhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

const connectorEndpointEnv = "DEV_SSH_CONNECTOR_ENDPOINT"
const maxConnectorMessage = 256 << 10

type connectorSpec struct {
	Config      string   `json:"config"`
	ConfigHash  string   `json:"config_hash"`
	Destination string   `json:"destination"`
	Forward     string   `json:"forward"`
	Env         []string `json:"env"`
}
type connectorLookup struct {
	Operation string `json:"operation"`
	Hop       int    `json:"hop"`
}
type connectorServer struct {
	mu         sync.Mutex
	listener   net.Listener
	endpoint   string
	cleanup    func()
	phases     map[string][]connectorSpec
	clients    map[net.Conn]string
	closed     bool
	done       chan struct{}
	slots      chan struct{}
	closedDone chan struct{}
}

func newConnectorServer(ctx context.Context) (*connectorServer, error) {
	l, endpoint, cleanup, err := sshcredential.ListenPrivate()
	if err != nil {
		return nil, err
	}
	s := &connectorServer{listener: l, endpoint: endpoint, cleanup: cleanup, phases: map[string][]connectorSpec{}, clients: map[net.Conn]string{}, done: make(chan struct{}), slots: make(chan struct{}, 64), closedDone: make(chan struct{})}
	go s.serve()
	go func() {
		select {
		case <-ctx.Done():
			s.close()
		case <-s.done:
		}
	}()
	return s, nil
}
func (s *connectorServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closedDone
		return
	}
	s.closed = true
	close(s.done)
	for conn := range s.clients {
		_ = conn.Close()
	}
	s.phases = nil
	s.mu.Unlock()
	_ = s.listener.Close()
	s.cleanup()
	close(s.closedDone)
}
func newConnectorID() (string, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}
func (s *connectorServer) publish(id string, specs []connectorSpec) (func(), error) {
	if len(specs) > 64 || !validConnectorID(id) {
		return nil, ErrUnsupportedRoute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("SSH connector operation is closed")
	}
	s.phases[id] = specs
	return func() {
		s.mu.Lock()
		delete(s.phases, id)
		for conn, phase := range s.clients {
			if phase == id {
				_ = conn.Close()
			}
		}
		s.mu.Unlock()
	}, nil
}
func (s *connectorServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			<-s.slots
			return
		}
		s.clients[conn] = ""
		s.mu.Unlock()
		go func() { defer func() { <-s.slots }(); s.answer(conn) }()
	}
}
func (s *connectorServer) answer(conn net.Conn) {
	defer conn.Close()
	defer func() { s.mu.Lock(); delete(s.clients, conn); s.mu.Unlock() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if sshcredential.VerifyPrivatePeer(conn) != nil {
		return
	}
	var query connectorLookup
	if readConnectorMessage(conn, &query) != nil || !validConnectorID(query.Operation) || query.Hop < 0 || query.Hop >= 64 {
		return
	}
	s.mu.Lock()
	specs, ok := s.phases[query.Operation]
	if s.closed || !ok || query.Hop >= len(specs) {
		s.mu.Unlock()
		return
	}
	spec := specs[query.Hop]
	s.clients[conn] = query.Operation
	s.mu.Unlock()
	if writeConnectorMessage(conn, spec) != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	// Keep the channel open so operation cancellation also reaches connector
	// children in an interactive process tree, without killing the user's shell.
	var one [1]byte
	_, _ = conn.Read(one[:])
}
func validConnectorID(id string) bool {
	if len(id) != 48 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func writeConnectorMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxConnectorMessage {
		return ErrUnsupportedRoute
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func readConnectorMessage(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxConnectorMessage {
		return ErrUnsupportedRoute
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func connectorCommand(executable, operation string, hop int) (string, error) {
	if !validUTF8NoControl(executable) || strings.Contains(executable, "%") || !validConnectorID(operation) || hop < 0 || hop >= 64 {
		return "", ErrUnsafePath
	}
	quoted := shellSingleQuote(executable)
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(executable, "\"\r\n%!") {
			return "", ErrUnsafePath
		}
		quoted = "\"" + executable + "\""
	}
	return quoted + " _ssh-hop --operation " + operation + " --hop " + strconv.Itoa(hop), nil
}

// MaybeServeSSHConnector is an early executable entry point, before app loading.
// It serves only an operation-owned forwarding request. Stdout is an unbuffered
// SSH byte stream and never contains diagnostics, progress, help or JSON.
func MaybeServeSSHConnector() (bool, int) {
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "_ssh-hop" {
		return false, 0
	}
	if len(args) != 5 || args[1] != "--operation" || args[3] != "--hop" || !validConnectorID(args[2]) {
		return true, 125
	}
	index, err := strconv.Atoi(args[4])
	if err != nil || index < 0 || index >= 64 {
		return true, 125
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := sshcredential.DialPrivate(ctx, os.Getenv(connectorEndpointEnv))
	if err != nil {
		return true, 125
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if writeConnectorMessage(conn, connectorLookup{Operation: args[2], Hop: index}) != nil {
		return true, 125
	}
	var spec connectorSpec
	if readConnectorMessage(conn, &spec) != nil {
		return true, 125
	}
	_ = conn.SetDeadline(time.Time{})
	go func() { var one [1]byte; _, _ = conn.Read(one[:]); cancel() }()
	snapshot, err := readSecureFile(spec.Config, false)
	if err != nil {
		return true, 125
	}
	sum := sha256.Sum256(snapshot.data)
	if hex.EncodeToString(sum[:]) != spec.ConfigHash || validateRouteLookupAlias(spec.Destination) != nil {
		return true, 125
	}
	host, port, err := net.SplitHostPort(spec.Forward)
	if err != nil || !validUTF8NoControl(host) || strings.HasPrefix(host, "-") {
		return true, 125
	}
	if _, err := parseJumpPort(port); err != nil {
		return true, 125
	}
	cmd := exec.Command("ssh", "-F", spec.Config, "-S", "none", "-W", spec.Forward, spec.Destination)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(sshcredential.ClearBrokerEnv(os.Environ()), spec.Env...)
	// Leave terminal process-group behavior native. Every connector has its
	// own cancellation channel, so all local SSH children are terminated when
	// the operation broker closes, including interactive nested children.
	if err := cmd.Start(); err != nil {
		return true, 125
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err = <-finished:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		err = <-finished
	}
	if err == nil {
		return true, 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return true, exit.ExitCode()
	}
	return true, 125
}

func connectorSnapshot(path string) (string, error) {
	snapshot, err := readSecureFile(path, false)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(snapshot.data)
	return fmt.Sprintf("%x", sum), nil
}

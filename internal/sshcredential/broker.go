package sshcredential

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const brokerMarker = "DEV_SSH_ASKPASS_BROKER"
const brokerEndpoint = "DEV_SSH_ASKPASS_ENDPOINT"
const brokerContext = "DEV_SSH_ASKPASS_CONTEXT"

type PasswordContext struct {
	ID        string
	User      string
	HostNames []string
	Port      int
}
type PasswordAnswer struct {
	Context PasswordContext
	Secret  []byte `json:"-"`
}

func (PasswordAnswer) String() string   { return "SSH password answer (redacted)" }
func (PasswordAnswer) GoString() string { return "sshcredential.PasswordAnswer{redacted}" }

type Broker struct {
	listener     net.Listener
	endpoint     string
	cleanup      func()
	mu           sync.Mutex
	answers      map[string]PasswordAnswer
	closed       bool
	done         chan struct{}
	usage        map[string]brokerUsage
	slots        chan struct{}
	acceptedDone chan struct{}
	closedDone   chan struct{}
	wg           sync.WaitGroup
}
type brokerUsage struct{ served, denied int }

func NewBroker(ctx context.Context, answers []PasswordAnswer) (*Broker, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(answers) == 0 || len(answers) > 64 {
		return nil, ErrUnsafe
	}
	b := &Broker{answers: map[string]PasswordAnswer{}, done: make(chan struct{}), usage: map[string]brokerUsage{}, slots: make(chan struct{}, 32), acceptedDone: make(chan struct{}), closedDone: make(chan struct{})}
	for _, a := range answers {
		if !cleanText(a.Context.ID) || !cleanText(a.Context.User) || len(a.Context.HostNames) == 0 || len(a.Context.HostNames) > 8 || a.Context.Port < 1 || a.Context.Port > 65535 || validateSecret(a.Secret) != nil {
			b.clear()
			return nil, ErrUnsafe
		}
		for _, h := range a.Context.HostNames {
			if !cleanText(h) || strings.ContainsAny(h, "\n\r'() ") {
				b.clear()
				return nil, ErrUnsafe
			}
		}
		if _, ok := b.answers[a.Context.ID]; ok {
			b.clear()
			return nil, ErrUnsafe
		}
		a.Secret = append([]byte(nil), a.Secret...)
		a.Context.HostNames = append([]string(nil), a.Context.HostNames...)
		b.answers[a.Context.ID] = a
		b.usage[a.Context.ID] = brokerUsage{}
	}
	l, endpoint, cleanup, err := listenBroker()
	if err != nil {
		b.clear()
		return nil, err
	}
	b.listener, b.endpoint, b.cleanup = l, endpoint, cleanup
	go b.serve()
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.done:
		}
	}()
	return b, nil
}
func (b *Broker) Env(executable, contextID string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !cleanText(executable) {
		return nil, ErrDenied
	}
	if _, ok := b.answers[contextID]; !ok {
		return nil, ErrDenied
	}
	return []string{brokerMarker + "=1", brokerEndpoint + "=" + b.endpoint, brokerContext + "=" + contextID, "SSH_ASKPASS=" + executable, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=dev-ssh"}, nil
}

// Usage is retained after Close so a caller can verify that its exact password
// was delivered once without any rejected questions before minting auth proof.
func (b *Broker) Usage(contextID string) (served, denied int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.usage[contextID]
	return u.served, u.denied
}
func ClearBrokerEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, v := range env {
		key, _, _ := strings.Cut(v, "=")
		switch key {
		case brokerMarker, brokerEndpoint, brokerContext, "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "DEV_FLEET_SSH_ASKPASS", "DEV_FLEET_SSH_ASKPASS_FD":
			continue
		}
		out = append(out, v)
	}
	return out
}
func (b *Broker) clear() {
	for k, a := range b.answers {
		Wipe(a.Secret)
		delete(b.answers, k)
	}
}
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		<-b.closedDone
		return nil
	}
	b.closed = true
	b.clear()
	close(b.done)
	b.mu.Unlock()
	err := b.listener.Close()
	<-b.acceptedDone
	b.wg.Wait()
	b.cleanup()
	close(b.closedDone)
	return err
}
func (b *Broker) serve() {
	defer close(b.acceptedDone)
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		select {
		case b.slots <- struct{}{}:
			b.wg.Add(1)
			go func() { defer b.wg.Done(); defer func() { <-b.slots }(); b.answer(conn) }()
		default:
			_ = conn.Close()
		}
	}
}
func (b *Broker) answer(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if brokerPeerAllowed(conn) != nil {
		return
	}
	reader := bufio.NewReader(io.LimitReader(conn, 8193))
	id, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	id = strings.TrimSuffix(id, "\n")
	prompt, err := reader.ReadString('\n')
	if err != nil {
		b.mu.Lock()
		if _, ok := b.usage[id]; ok {
			u := b.usage[id]
			if u.denied < 1<<20 {
				u.denied++
			}
			b.usage[id] = u
		}
		b.mu.Unlock()
		return
	}
	prompt = strings.TrimSuffix(prompt, "\n")
	b.mu.Lock()
	a, ok := b.answers[id]
	if b.closed || !ok || !passwordPromptMatches(a.Context, prompt) {
		if _, known := b.usage[id]; known {
			u := b.usage[id]
			if u.denied < 1<<20 {
				u.denied++
			}
			b.usage[id] = u
		}
		b.mu.Unlock()
		return
	}
	secret := append([]byte(nil), a.Secret...)
	b.mu.Unlock()
	defer Wipe(secret)
	err = binary.Write(conn, binary.BigEndian, uint32(len(secret)))
	if err == nil {
		var written int
		written, err = conn.Write(secret)
		if written != len(secret) {
			err = io.ErrShortWrite
		}
	}
	// The helper acknowledges only after it has written the full answer to
	// OpenSSH's pipe; an IPC read alone is not evidence of password delivery.
	if err == nil {
		var ack [1]byte
		_, err = io.ReadFull(reader, ack[:])
		if err == nil && ack[0] != 1 {
			err = ErrDenied
		}
	}
	b.mu.Lock()
	u := b.usage[id]
	if err == nil {
		if u.served < 1<<20 {
			u.served++
		}
	} else {
		if u.denied < 1<<20 {
			u.denied++
		}
	}
	b.usage[id] = u
	b.mu.Unlock()
}
func passwordPromptMatches(c PasswordContext, prompt string) bool {
	// Generic keyboard-interactive, OTP, host-key and private-key passphrase
	// questions cannot establish the selected account; never answer them.
	for _, host := range c.HostNames {
		for _, p := range []string{c.User + "@" + host + "'s password: ", c.User + "@" + host + "'s password:", "(" + c.User + "@" + host + ") Password: ", "(" + c.User + "@" + host + ") Password:"} {
			if prompt == p {
				return true
			}
		}
	}
	return false
}
func RequestPassword(ctx context.Context, endpoint, id, prompt string) ([]byte, error) {
	secret, finish, err := receivePassword(ctx, endpoint, id, prompt)
	if err != nil {
		return nil, err
	}
	finish(true)
	return secret, nil
}
func receivePassword(ctx context.Context, endpoint, id, prompt string) ([]byte, func(bool), error) {
	if !cleanText(id) || len(prompt) > 4096 || strings.ContainsAny(prompt, "\r\n") {
		return nil, nil, ErrDenied
	}
	conn, err := dialBroker(ctx, endpoint)
	if err != nil {
		return nil, nil, ErrDenied
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(conn, id+"\n"+prompt+"\n"); err != nil {
		conn.Close()
		return nil, nil, ErrDenied
	}
	var n uint32
	if binary.Read(conn, binary.BigEndian, &n) != nil || n == 0 || n > MaxSecretBytes {
		conn.Close()
		return nil, nil, ErrDenied
	}
	secret := make([]byte, n)
	if _, err = io.ReadFull(conn, secret); err != nil {
		Wipe(secret)
		conn.Close()
		return nil, nil, ErrDenied
	}
	finish := func(delivered bool) {
		if delivered {
			_, _ = conn.Write([]byte{1})
		}
		_ = conn.Close()
	}
	return secret, finish, nil
}

// MaybeServeAskpass runs before CLI parsing. Only the expected OpenSSH prompt
// is sent over private IPC; the answer exists only in memory and helper stdout.
func MaybeServeAskpass() (bool, int) {
	if os.Getenv(brokerMarker) != "1" {
		return false, 0
	}
	if len(os.Args) != 2 {
		return true, 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	secret, finish, err := receivePassword(ctx, os.Getenv(brokerEndpoint), os.Getenv(brokerContext), os.Args[1])
	if err != nil {
		return true, 2
	}
	defer Wipe(secret)
	_, err1 := os.Stdout.Write(secret)
	_, err2 := os.Stdout.Write([]byte{'\n'})
	finish(errors.Join(err1, err2) == nil)
	if errors.Join(err1, err2) != nil {
		return true, 2
	}
	return true, 0
}

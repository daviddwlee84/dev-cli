package sshcredential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testContext() Context {
	return Context{Origin: "origin-a", Profile: "profile-a", Route: "route-a", Host: "10.0.0.5", User: "alice", Port: 22, Kind: "password"}
}
func testStore(t *testing.T) *Store {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return NewStore(filepath.Join(root, "config", "dev", "ssh-credentials.toml"))
}

type fakeProvider struct {
	puts, gets int
	failure    error
	secret     []byte
	onPut      func()
	onGet      func()
	returned   []byte
	last       Context
}

func (*fakeProvider) ID() string                      { return "system" }
func (*fakeProvider) Available(context.Context) error { return nil }
func (p *fakeProvider) Get(_ context.Context, c Context, ref Reference) ([]byte, error) {
	p.gets++
	if validateNativeReference(c, ref) != nil {
		return nil, ErrUnsafe
	}
	p.returned = append([]byte(nil), p.secret...)
	if p.onGet != nil {
		p.onGet()
	}
	return p.returned, nil
}
func (p *fakeProvider) Put(_ context.Context, c Context, old *Reference, secret []byte) (Reference, error) {
	p.puts++
	p.last = c
	p.secret = append([]byte(nil), secret...)
	if p.onPut != nil {
		p.onPut()
	}
	if p.failure != nil {
		return Reference{}, p.failure
	}
	return Reference{"system", c.ID()}, nil
}
func (*fakeProvider) Delete(context.Context, Context, Reference) error { return nil }

func TestScopeNeverOvermergesAndMemoryIsOperationLocal(t *testing.T) {
	c := testContext()
	for _, change := range []func(*Context){func(c *Context) { c.Origin = "other" }, func(c *Context) { c.Profile = "other" }, func(c *Context) { c.Route = "other" }, func(c *Context) { c.User = "bob" }, func(c *Context) { c.Port = 2222 }} {
		other := c
		change(&other)
		if other.ID() == c.ID() {
			t.Fatal("contexts overmerged")
		}
	}
	var m Memory
	secret := []byte("secret-a")
	if e := m.Set(c, secret); e != nil {
		t.Fatal(e)
	}
	secret[0] = 'x'
	got, ok := m.Get(c)
	if !ok || string(got) != "secret-a" {
		t.Fatal("memory did not own secret")
	}
	Wipe(got)
	m.Close()
	if _, ok = m.Get(c); ok {
		t.Fatal("closed operation retained password")
	}
}
func TestSaveChoicesAndReferencesNeverPersistSecrets(t *testing.T) {
	ctx := context.Background()
	c := testContext()
	store := testStore(t)
	provider := &fakeProvider{}
	m := Manager{store, map[string]Provider{"system": provider}}
	secret := []byte("secret must never appear in policy or result")
	if _, e := m.Save(ctx, c, secret, SaveNo, "system"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Lstat(filepath.Dir(store.Path)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("no choice created policy paths")
	}
	r, e := m.Save(ctx, c, secret, SaveYes, "system")
	if e != nil || r.Status != "saved" || provider.puts != 1 {
		t.Fatalf("%#v %v", r, e)
	}
	b, e := os.ReadFile(store.Path)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(b)+string(encoded), string(secret)) {
		t.Fatal("secret persisted outside provider")
	}
	got, e := m.Get(ctx, c)
	if e != nil || string(got) != string(secret) {
		t.Fatal("saved reference cannot resolve", e)
	}
	Wipe(got)
	other := c
	other.Route = "other-route"
	if _, e = m.Get(ctx, other); !errors.Is(e, ErrNotFound) {
		t.Fatal("stored password crossed route scope", e)
	}
	if _, e = m.Save(ctx, other, nil, SaveNever, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Save(ctx, other, secret, SaveYes, "system"); !errors.Is(e, ErrDenied) {
		t.Fatal("never preference ignored", e)
	}
	if provider.puts != 1 {
		t.Fatal("never contacted provider")
	}
}
func TestProviderUnknownAndConcurrentPolicyRemainHonest(t *testing.T) {
	for _, mode := range []string{"unknown", "changed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			c := testContext()
			store := testStore(t)
			provider := &fakeProvider{}
			if mode == "unknown" {
				provider.failure = errors.New("provider leaked secret sentinel")
			}
			provider.onPut = func() {
				r, ok, e := store.Lookup(ctx, c)
				if e != nil || !ok || r.State != "pending" {
					t.Fatalf("provider mutation had no pending ledger: %#v %v", r, e)
				}
				if mode == "changed" {
					if _, e = store.SavePreference(ctx, c, PolicyNever); e != nil {
						t.Fatal(e)
					}
				}
			}
			m := Manager{store, map[string]Provider{"system": provider}}
			r, e := m.Save(ctx, c, []byte("secret"), SaveYes, "system")
			if !errors.Is(e, ErrUnknown) || r.Status != "unknown" || strings.Contains(e.Error(), "sentinel") {
				t.Fatalf("%#v %v", r, e)
			}
			record, _, e := store.Lookup(ctx, c)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "unknown" && record.State != "unknown" {
				t.Fatal(record)
			}
			if mode == "changed" && record.Policy != PolicyNever {
				t.Fatal("concurrent preference overwritten", record)
			}
			if _, e = m.Get(ctx, c); !errors.Is(e, ErrUnknown) {
				t.Fatal("pending credential reused", e)
			}
		})
	}
}
func TestPolicyPlanIsGuardedAndPrivate(t *testing.T) {
	ctx := context.Background()
	c := testContext()
	s := testStore(t)
	if snapshot, e := s.Read(ctx); e != nil || len(snapshot.Records) != 0 {
		t.Fatal(snapshot, e)
	}
	record := Record{ID: c.ID(), Context: c, Policy: PolicyAsk, State: "ready"}
	p, e := s.Plan(ctx, record)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Apply(ctx, p); !errors.Is(e, ErrStale) {
		t.Fatal("stale plan accepted", e)
	}
	p, e = s.Plan(ctx, record)
	if e != nil {
		t.Fatal(e)
	}
	p.After.Records[0].Policy = PolicyNever
	if _, e = s.Apply(ctx, p); !errors.Is(e, ErrStale) {
		t.Fatal("modified plan accepted", e)
	}
	info, e := os.Lstat(s.Path)
	if e != nil || credentialPrivate(s.Path, info, 0o600) != nil {
		t.Fatal("policy file is not private", e)
	}
}

func TestManagerDiscardsNativeGetCompletingAfterDeadline(t *testing.T) {
	store := testStore(t)
	provider := &fakeProvider{}
	manager := Manager{store, map[string]Provider{"system": provider}}
	c := testContext()
	if _, e := manager.Save(context.Background(), c, []byte("native secret"), SaveYes, "system"); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider.onGet = cancel
	secret, e := manager.Get(ctx, c)
	if !errors.Is(e, context.Canceled) || len(secret) != 0 {
		t.Fatal("late provider secret escaped", e)
	}
	for _, b := range provider.returned {
		if b != 0 {
			t.Fatal("late secret bytes not wiped")
		}
	}
}

type bwRun struct {
	calls     [][]string
	item      map[string]any
	scope     string
	secret    string
	foreign   bool
	failWrite bool
}

func (r *bwRun) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if name != "bw" || strings.Contains(strings.Join(args, " "), r.secret) {
		return nil, errors.New("secret leaked in argv")
	}
	switch args[0] {
	case "status":
		return []byte(`{"status":"unlocked"}`), nil
	case "get":
		item := r.item
		if r.foreign {
			item = map[string]any{"id": "item-1", "login": map[string]any{"password": "foreign"}}
		}
		return json.Marshal(item)
	case "create", "edit":
		if len(args) > 3 {
			return nil, errors.New("encoded secret passed as argv")
		}
		raw, e := base64.StdEncoding.DecodeString(string(stdin))
		if e != nil {
			return nil, e
		}
		defer Wipe(raw)
		var item map[string]any
		if e = json.Unmarshal(raw, &item); e != nil {
			return nil, e
		}
		if !bwOwned(item, r.scope) {
			return nil, ErrUnsafe
		}
		if !strings.Contains(string(raw), r.secret) {
			return nil, errors.New("secret missing from stdin")
		}
		if r.failWrite {
			return nil, errors.New("server error with secret " + r.secret)
		}
		item["id"] = "item-1"
		r.item = item
		return json.Marshal(item)
	case "delete":
		return nil, nil
	}
	return nil, ErrUnavailable
}
func TestBitwardenUsesStdinAndUpdatesOnlyExactOwnedRecords(t *testing.T) {
	ctx := context.Background()
	c := testContext()
	runner := &bwRun{scope: c.ID(), secret: "vault-secret-sentinel"}
	provider := Bitwarden{Runner: runner}
	ref, e := provider.Put(ctx, c, nil, []byte(runner.secret))
	if e != nil || ref.ID != "item-1" {
		t.Fatal(ref, e)
	}
	if _, e = provider.Put(ctx, c, &ref, []byte(runner.secret)); e != nil {
		t.Fatal(e)
	}
	got, e := provider.Get(ctx, c, ref)
	if e != nil || string(got) != runner.secret {
		t.Fatal("get failed", e)
	}
	Wipe(got)
	before := len(runner.calls)
	runner.foreign = true
	if _, e = provider.Put(ctx, c, &ref, []byte(runner.secret)); !errors.Is(e, ErrUnsafe) {
		t.Fatal("foreign item overwritten", e)
	}
	for _, args := range runner.calls[before:] {
		if args[0] == "edit" {
			t.Fatal("foreign edit ran")
		}
	}
	runner.foreign = false
	runner.failWrite = true
	if _, e = provider.Put(ctx, c, &ref, []byte(runner.secret)); !errors.Is(e, ErrUnknown) || strings.Contains(e.Error(), runner.secret) {
		t.Fatal("write outcome dishonest or leaked", e)
	}
}
func TestBrokerScopesAnswersAndRejectsUnknownPrompts(t *testing.T) {
	ctx := context.Background()
	answers := []PasswordAnswer{{PasswordContext{"hop-a", "alice", []string{"host-a"}, 22}, []byte("first")}, {PasswordContext{"hop-b", "bob", []string{"host-b"}, 22}, []byte("second")}}
	encoded, e := json.Marshal(answers)
	if e != nil || strings.Contains(string(encoded), "Zmlyc3Q=") || strings.Contains(string(encoded), "c2Vjb25k") {
		t.Fatal("secret answer serializable", e)
	}
	b, e := NewBroker(ctx, answers)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	for _, test := range []struct{ id, prompt, want string }{{"hop-a", "alice@host-a's password: ", "first"}, {"hop-b", "bob@host-b's password: ", "second"}, {"hop-a", "bob@host-b's password: ", ""}, {"hop-a", "Password:", ""}, {"hop-a", "Verification code:", ""}, {"hop-a", "Enter passphrase for key '/private': ", ""}, {"unknown", "alice@host-a's password: ", ""}} {
		got, e := RequestPassword(ctx, b.endpoint, test.id, test.prompt)
		if test.want == "" {
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("prompt accepted: %q", test.prompt)
			}
		} else if e != nil || string(got) != test.want {
			t.Fatalf("wrong password context: %v", e)
		}
		Wipe(got)
	}
	env, e := b.Env("/dev-binary", "hop-a")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(strings.Join(env, " "), "first") || strings.Contains(strings.Join(env, " "), "second") {
		t.Fatal("secret in helper env")
	}
	clean := ClearBrokerEnv(append([]string{"PATH=original"}, env...))
	if !reflect.DeepEqual(clean, []string{"PATH=original", "DISPLAY=dev-ssh"}) {
		t.Fatal(clean)
	}
	if _, e = b.Env("/dev-binary", "unknown"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = b.Close(); e != nil {
		t.Fatal(e)
	}
	served, denied := b.Usage("hop-a")
	if served != 1 || denied != 4 {
		t.Fatalf("usage after drain = %d/%d", served, denied)
	}
	served, denied = b.Usage("hop-b")
	if served != 1 || denied != 0 {
		t.Fatalf("hop-b usage = %d/%d", served, denied)
	}
	if _, e = RequestPassword(ctx, b.endpoint, "hop-a", "alice@host-a's password: "); !errors.Is(e, ErrDenied) {
		t.Fatal("closed broker answered", e)
	}
}

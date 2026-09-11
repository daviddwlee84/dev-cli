package sshcredential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"time"
)

const providerOwner = "dev-cli:ssh-password:v1"

type CommandRunner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}
type NativeRunner struct{}

func (NativeRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out limitedOutput
	out.limit = 1 << 20
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		Wipe(out.data)
		return nil, ErrUnavailable
	}
	if out.exceeded {
		Wipe(out.data)
		return nil, ErrUnavailable
	}
	return out.data, nil
}

type limitedOutput struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - len(b.data)
	if len(p) > room {
		b.exceeded = true
		p = p[:max(0, room)]
	}
	b.data = append(b.data, p...)
	return n, nil
}

type Bitwarden struct{ Runner CommandRunner }

func (Bitwarden) ID() string { return "bitwarden" }
func (p Bitwarden) run(ctx context.Context, args []string, in []byte) ([]byte, error) {
	r := p.Runner
	if r == nil {
		r = NativeRunner{}
	}
	b, e := r.Run(ctx, "bw", args, in)
	if e != nil {
		Wipe(b)
		return nil, ErrUnavailable
	}
	return b, nil
}
func (p Bitwarden) Available(ctx context.Context) error {
	b, e := p.run(ctx, []string{"status"}, nil)
	defer Wipe(b)
	if e != nil {
		return e
	}
	var s struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ErrUnavailable
	}
	if s.Status != "unlocked" {
		return ErrLocked
	}
	return nil
}
func (p Bitwarden) item(ctx context.Context, c Context, ref Reference) (map[string]any, error) {
	if c.Validate() != nil || ref.Provider != p.ID() || !validReference(ref) {
		return nil, ErrUnsafe
	}
	b, e := p.run(ctx, []string{"get", "item", ref.ID}, nil)
	defer Wipe(b)
	if e != nil {
		return nil, e
	}
	var item map[string]any
	if json.Unmarshal(b, &item) != nil {
		return nil, ErrUnavailable
	}
	if item["id"] != ref.ID || !bwOwned(item, c.ID()) {
		return nil, ErrUnsafe
	}
	return item, nil
}
func bwOwned(item map[string]any, id string) bool {
	if item["type"] != float64(1) && item["type"] != 1 {
		return false
	}
	fields, _ := item["fields"].([]any)
	owner, scope := false, false
	for _, v := range fields {
		f, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if f["name"] == "dev-owner" {
			if owner || f["value"] != providerOwner {
				return false
			}
			owner = true
		}
		if f["name"] == "dev-context" {
			if scope || f["value"] != id {
				return false
			}
			scope = true
		}
	}
	return owner && scope
}
func (p Bitwarden) Get(ctx context.Context, c Context, ref Reference) ([]byte, error) {
	item, e := p.item(ctx, c, ref)
	if e != nil {
		return nil, e
	}
	login, _ := item["login"].(map[string]any)
	password, ok := login["password"].(string)
	if !ok || validateSecret([]byte(password)) != nil {
		return nil, ErrNotFound
	}
	return []byte(password), nil
}
func (p Bitwarden) Put(ctx context.Context, c Context, old *Reference, secret []byte) (Reference, error) {
	if c.Validate() != nil || validateSecret(secret) != nil {
		return Reference{}, ErrUnsafe
	}
	if e := p.Available(ctx); e != nil {
		return Reference{}, e
	}
	var item map[string]any
	var e error
	args := []string{"create", "item"}
	if old != nil {
		item, e = p.item(ctx, c, *old)
		if e != nil {
			return Reference{}, e
		}
		args = []string{"edit", "item", old.ID}
	} else {
		item = map[string]any{"type": 1, "name": "dev SSH " + c.ID()[:16], "fields": []any{map[string]any{"name": "dev-owner", "value": providerOwner, "type": 0}, map[string]any{"name": "dev-context", "value": c.ID(), "type": 0}}}
	}
	item["login"] = map[string]any{"username": c.User, "password": string(secret)}
	raw, e := json.Marshal(item)
	if e != nil {
		return Reference{}, ErrUnsafe
	}
	defer Wipe(raw)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(encoded, raw)
	defer Wipe(encoded)
	b, e := p.run(ctx, args, encoded)
	defer Wipe(b)
	if e != nil {
		return Reference{}, ErrUnknown
	}
	var result map[string]any
	if json.Unmarshal(b, &result) != nil || !bwOwned(result, c.ID()) {
		return Reference{}, ErrUnknown
	}
	id, _ := result["id"].(string)
	if !cleanText(id) || old != nil && id != old.ID {
		return Reference{}, ErrUnknown
	}
	return Reference{Provider: p.ID(), ID: id}, nil
}
func (p Bitwarden) Delete(ctx context.Context, c Context, ref Reference) error {
	if _, e := p.item(ctx, c, ref); e != nil {
		return e
	}
	b, e := p.run(ctx, []string{"delete", "item", ref.ID}, nil)
	Wipe(b)
	if e != nil {
		return ErrUnknown
	}
	return nil
}
func SystemProvider() Provider { return nativeSystemProvider{} }
func validateNativeReference(c Context, ref Reference) error {
	if c.Validate() != nil || ref.Provider != "system" || ref.ID != c.ID() {
		return ErrUnsafe
	}
	return nil
}
func ProviderByID(id string) (Provider, error) {
	switch strings.ToLower(id) {
	case "system":
		return SystemProvider(), nil
	case "bitwarden":
		return Bitwarden{}, nil
	default:
		return nil, ErrUnavailable
	}
}

// Package triage joins host-local work into a reviewable recovery queue.
package triage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type Intent struct {
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	Until       time.Time `json:"until,omitempty"`
}
type Preferences struct {
	Version        int               `json:"version"`
	Identity       string            `json:"identity"`
	DisposableDirs []string          `json:"disposable_dirs"`
	Intents        map[string]Intent `json:"intents"`
}
type Store struct{ Dir string }

func digest(v any) string {
	body, _ := json.Marshal(v)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Identity binds destructive preferences to a particular local directory, not
// merely a path another clone could later reuse. Platforms without stable
// filesystem identity cannot enroll disposable-directory policies.
func Identity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("identity requires a real directory")
	}
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if v.IsValid() && v.Kind() == reflect.Struct {
		dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
		if dev.IsValid() && ino.IsValid() {
			return fmt.Sprintf("%s:%v:%v", path, dev, ino), nil
		}
	}
	return "", errors.New("stable local filesystem identity is unavailable")
}

func (s Store) Read(key string) (Preferences, error) {
	p := Preferences{Version: 1, DisposableDirs: []string{}, Intents: map[string]Intent{}}
	data, err := safefile.ReadRegular(context.Background(), filepath.Join(s.Dir, digest(key)+".json"), 1<<20)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if p.Version != 1 {
		return p, errors.New("unsupported triage preferences version")
	}
	if p.Intents == nil {
		p.Intents = map[string]Intent{}
	}
	if err = gitx.ValidateDisposableDirs(p.DisposableDirs); err != nil {
		return p, err
	}
	identity, e := Identity(key)
	if p.Identity != "" && (e != nil || identity != p.Identity) {
		return p, errors.New("repository replaced or moved; reset triage preferences before reuse")
	}
	return p, nil
}

func (s Store) update(ctx context.Context, key string, edit func(*Preferences) error) error {
	return lockx.WithDir(ctx, s.Dir, "triage preferences", func() error {
		p, err := s.Read(key)
		if err != nil {
			return err
		}
		if err = edit(&p); err != nil {
			return err
		}
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.Dir, ".triage-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Rename(name, filepath.Join(s.Dir, digest(key)+".json"))
	})
}

func (s Store) SetIntent(ctx context.Context, item Item, kind string, until time.Time) error {
	if kind != "" && kind != "local" && kind != "snooze" {
		return errors.New("unknown triage intent")
	}
	return s.update(ctx, item.RepositoryID, func(p *Preferences) error {
		if identity, e := Identity(item.RepositoryID); e == nil {
			p.Identity = identity
		}
		if kind == "" {
			delete(p.Intents, item.ID)
		} else {
			p.Intents[item.ID] = Intent{Kind: kind, Fingerprint: item.Fingerprint, Until: until}
		}
		return nil
	})
}

func (s Store) SetDisposable(ctx context.Context, key string, dirs []string) error {
	if err := gitx.ValidateDisposableDirs(dirs); err != nil {
		return err
	}
	identity, err := Identity(key)
	if err != nil {
		return err
	}
	return s.update(ctx, key, func(p *Preferences) error {
		p.Identity = identity
		p.DisposableDirs = append([]string{}, dirs...)
		return nil
	})
}

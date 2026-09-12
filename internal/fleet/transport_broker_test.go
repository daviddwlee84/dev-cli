package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoredPasswordResolvesBeforeOneAttemptAndLegacyTakesPriority(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" -G "*) printf '%s\n' 'hostname lab.example' 'user tester' 'port 22'; exit 0 ;;
esac
printf 'attempt\n' >> "$ATTEMPT_FILE"
if [ -n "$DEV_SSH_ASKPASS_BROKER" ]; then
  "$SSH_ASKPASS" "tester@lab.example's password: " >/dev/null || exit 42
fi
printf 'Permission denied (password).' >&2
exit 255
`
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	marker := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("ATTEMPT_FILE", marker)
	host := transportTestHost()
	calls := 0
	secret := []byte("stored sentinel")
	transport := Transport{StoredPassword: func(context.Context, Host) ([]byte, error) {
		calls++
		if _, e := os.Lstat(marker); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("stored lookup ran after SSH started")
		}
		return secret, nil
	}}
	result := transport.Run(context.Background(), host, []string{"fleet", "_sync"}, nil, false)
	if calls != 1 || result.Attempts != 1 || !result.UsedPassword || result.ExitCode != 255 {
		t.Fatalf("stored attempt = %#v calls=%d", result, calls)
	}
	for _, b := range secret {
		if b != 0 {
			t.Fatal("resolved password bytes not wiped")
		}
	}
	body, e := os.ReadFile(marker)
	if e != nil || string(body) != "attempt\n" {
		t.Fatalf("stored password retried: %q %v", body, e)
	}
	if e = os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	host.SSHLoginPasswordSource = PasswordSource{Type: "plain", Value: "legacy"}
	transport.StoredPassword = func(context.Context, Host) ([]byte, error) {
		t.Fatal("legacy source did not take priority")
		return nil, nil
	}
	result = transport.RunWithOptions(context.Background(), host, []string{"fleet", "_sync"}, nil, RunOptions{Retry: RetryNever})
	if result.Attempts != 1 || result.UsedPassword {
		t.Fatal(result)
	}
}
func TestStoredPasswordFailureDoesNotConnectOrExposeProviderError(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("STARTED_FILE", marker)
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n: > \"$STARTED_FILE\"\n"), 0o755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	transport := Transport{StoredPassword: func(context.Context, Host) ([]byte, error) {
		return []byte("secret"), errors.New("provider response includes secret sentinel")
	}}
	result := transport.Run(context.Background(), transportTestHost(), []string{"fleet", "_sync"}, nil, false)
	if result.Attempts != 0 || result.ExitCode != 125 || strings.Contains(result.TransportError, "sentinel") {
		t.Fatal(result)
	}
	if _, e := os.Lstat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("provider failure reached SSH", e)
	}
}

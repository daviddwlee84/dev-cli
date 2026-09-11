package fleet

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestWindowsSSHHelperAllowlistPreservesFixedShapes(t *testing.T) {
	for _, helper := range []string{"_ssh-capability", "_ssh-inventory", "_ssh-resolve", "_ssh-keys"} {
		if err := validateWindowsFleetHelperArgs([]string{"fleet", helper}); err != nil {
			t.Fatal(err)
		}
		if err := validateWindowsFleetHelperArgs([]string{"fleet", helper, "arbitrary"}); err == nil {
			t.Fatalf("accepted extra arg for %s", helper)
		}
	}
	request := `{"schema_version":1,"protocol_version":1,"origin_id":"origin","machine_id":"11111111-1111-4111-8111-111111111111","selection":{"origin_id":"origin","profile_id":"profile","alias":"target","fingerprint":"fingerprint"}}`
	encode := func(body string) string { return base64.RawURLEncoding.EncodeToString([]byte(body)) }
	if err := validateWindowsFleetHelperArgs([]string{"fleet", "_ssh-connect", "--request", encode(request)}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{request + `{}`, strings.Replace(request, `"alias":"target"`, `"alias":"-oProxyCommand=bad"`, 1), strings.Replace(request, `"schema_version":1`, `"schema_version":2`, 1), strings.TrimSuffix(request, "}") + `,"args":["rm"]}`} {
		if err := validateWindowsFleetHelperArgs([]string{"fleet", "_ssh-connect", "--request", encode(invalid)}); err == nil {
			t.Fatalf("accepted invalid connection request: %s", invalid)
		}
	}
	if err := validateWindowsFleetHelperArgs([]string{"fleet", "_ssh-connect", "--request", encode(request), "command"}); err == nil {
		t.Fatal("accepted command passthrough")
	}
}

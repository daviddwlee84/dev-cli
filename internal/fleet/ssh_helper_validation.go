package fleet

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/machineid"
)

// Keep this wire-shape check in fleet to avoid an import cycle with sshremote.
// The remote helper performs the complete source-ID and selection validation.
func validateEncodedSSHConnectRequest(encoded string) error {
	invalid := errors.New("invalid encoded SSH connection request")
	if encoded == "" || len(encoded) > ((64<<10)*4/3+4) {
		return invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return invalid
	}
	var request struct {
		SchemaVersion   int    `json:"schema_version"`
		ProtocolVersion int    `json:"protocol_version"`
		OriginID        string `json:"origin_id"`
		MachineID       string `json:"machine_id"`
		Selection       struct {
			OriginID    string `json:"origin_id"`
			ProfileID   string `json:"profile_id"`
			Alias       string `json:"alias"`
			Fingerprint string `json:"fingerprint"`
		} `json:"selection"`
		KeyID string `json:"key_id,omitempty"`
	}
	if UnmarshalStrict(data, 64<<10, &request) != nil || request.SchemaVersion != 1 || request.ProtocolVersion != 1 || machineid.Validate(request.MachineID) != nil {
		return invalid
	}
	if request.OriginID == "" || request.Selection.OriginID != request.OriginID || request.Selection.ProfileID == "" || request.Selection.Fingerprint == "" {
		return invalid
	}
	alias := request.Selection.Alias
	if alias == "" || len(alias) > 255 || !utf8.ValidString(alias) || strings.HasPrefix(alias, "-") || strings.ContainsAny(alias, "*?[]!") || strings.ContainsFunc(alias, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return invalid
	}
	return nil
}

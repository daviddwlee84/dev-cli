package sshvault

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"golang.org/x/crypto/ssh"
)

const vaultKeyOwner = "dev-cli:ssh-key:v1"

type bwField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  int    `json:"type"`
}

// Unknown custom fields might contain secrets. Decode only our two public
// ownership fields, not arbitrary field values from a provider response.
type bwResponseField bwField

func (field *bwResponseField) UnmarshalJSON(body []byte) error {
	var header struct {
		Name string `json:"name"`
		Type int    `json:"type"`
	}
	if json.Unmarshal(body, &header) != nil {
		return ErrUnavailable
	}
	field.Name, field.Type = header.Name, header.Type
	if header.Name != "dev-owner" && header.Name != "dev-operation" {
		return nil
	}
	var value struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(body, &value) != nil {
		return ErrUnavailable
	}
	field.Value = value.Value
	return nil
}

// The caller holds Service.mu. Public Apply reaches this only after both
// approvals and fresh native-profile checks, supplying a frozen execution path.
func (s *Service) createBitwardenUsing(ctx context.Context, state *planState, execute func(context.Context, []string, []byte) ([]byte, error)) (Result, error) {
	result := Result{Status: StatusNotStarted}
	if s == nil || state == nil || state.service != s || state.request.Provider != Bitwarden || validateRequest(state.request) != nil || !state.request.NativeContextApproved || execute == nil {
		return result, ErrUnsafe
	}
	if state.attempted {
		return result, ErrPlanUsed
	}
	generate := s.generate
	if generate == nil {
		generate = bitwardenInput
	}
	input, publicLine, fingerprint, err := generate(state)
	defer sshcredential.Wipe(input)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	state.attempted = true
	result.Status, result.BindingStatus = StatusUnknown, BindingUnknown
	body, err := execute(ctx, []string{"create", "item"}, input)
	defer sshcredential.Wipe(body)
	if err != nil {
		return result, ErrUnknown
	}
	// The type-5 response may contain the entire private key. It stays in the
	// bounded response buffer and is deliberately absent from this decoder.
	var item struct {
		ID     string            `json:"id"`
		Type   int               `json:"type"`
		Name   string            `json:"name"`
		Fields []bwResponseField `json:"fields"`
	}
	if json.Unmarshal(body, &item) != nil || !bwIDPattern.MatchString(item.ID) || item.Type != 5 || item.Name != state.request.Title || !ownedBitwardenFields(item.Fields, state.operation) {
		return result, ErrUnknown
	}
	// A matching operation marker identifies the created item even if its
	// public-key receipt is incomplete. Keep the exact ID for reconciliation.
	result.Status = StatusCreated
	result.Receipt = &Receipt{Destination: state.dest, ItemID: item.ID}
	var scope struct {
		OrganizationID *string  `json:"organizationId"`
		CollectionIDs  []string `json:"collectionIds"`
	}
	if json.Unmarshal(body, &scope) != nil || scope.OrganizationID != nil && *scope.OrganizationID != "" || len(scope.CollectionIDs) != 0 {
		return result, ErrUnknown
	}
	var public struct {
		SSHKey struct {
			PublicKey      string `json:"publicKey"`
			KeyFingerprint string `json:"keyFingerprint"`
		} `json:"sshKey"`
	}
	if json.Unmarshal(body, &public) != nil {
		return result, ErrPublicKey
	}
	actualLine, actualFingerprint, err := publicIdentity(public.SSHKey.PublicKey)
	if err != nil || actualLine != publicLine || actualFingerprint != fingerprint || public.SSHKey.KeyFingerprint != fingerprint {
		return result, ErrPublicKey
	}
	result.Receipt.PublicLine, result.Receipt.Fingerprint = publicLine, fingerprint
	return result, nil
}

func ownedBitwardenFields(fields []bwResponseField, operation string) bool {
	if len(fields) != 2 {
		return false
	}
	owner, exactOperation := false, false
	for _, field := range fields {
		if field.Type != 0 {
			return false
		}
		switch field.Name {
		case "dev-owner":
			if owner || field.Value != vaultKeyOwner {
				return false
			}
			owner = true
		case "dev-operation":
			if exactOperation || field.Value != operation {
				return false
			}
			exactOperation = true
		default:
			return false
		}
	}
	return owner && exactOperation
}

// bitwardenInput owns all directly accessible secret buffers and wipes them on
// return. The remaining encoded stdin is also secret and its caller must wipe it.
// Go/crypto/PEM implementations and the OS can retain additional copies; this is
// best-effort cleanup, not a no-swap, no-core-dump or complete-erasure guarantee.
func bitwardenInput(state *planState) ([]byte, string, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	defer sshcredential.Wipe(private)
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	block, err := ssh.MarshalPrivateKey(private, state.request.Title)
	if err != nil {
		return nil, "", "", ErrUnavailable
	}
	defer sshcredential.Wipe(block.Bytes)
	privatePEM := pem.EncodeToMemory(block)
	defer sshcredential.Wipe(privatePEM)
	publicLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	fingerprint := ssh.FingerprintSHA256(key)
	metadata := struct {
		Type           int       `json:"type"`
		Name           string    `json:"name"`
		Login          any       `json:"login"`
		OrganizationID any       `json:"organizationId"`
		CollectionIDs  []string  `json:"collectionIds"`
		Fields         []bwField `json:"fields"`
	}{Type: 5, Name: state.request.Title, CollectionIDs: []string{}, Fields: []bwField{{Name: "dev-owner", Value: vaultKeyOwner}, {Name: "dev-operation", Value: state.operation}}}
	prefix, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", "", ErrUnsafe
	}
	publicJSON, _ := json.Marshal(publicLine)
	fingerprintJSON, _ := json.Marshal(fingerprint)
	// Do not convert private PEM to a Go string or feed it to encoding/json's
	// pooled buffers. Only the public metadata above uses json.Marshal.
	body := make([]byte, 0, len(prefix)+2*len(privatePEM)+len(publicJSON)+len(fingerprintJSON)+128)
	body = append(body, prefix[:len(prefix)-1]...)
	body = append(body, `,"sshKey":{"privateKey":`...)
	body = appendPEMString(body, privatePEM)
	body = append(body, `,"publicKey":`...)
	body = append(body, publicJSON...)
	body = append(body, `,"keyFingerprint":`...)
	body = append(body, fingerprintJSON...)
	body = append(body, '}', '}')
	defer sshcredential.Wipe(body)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(body)))
	base64.StdEncoding.Encode(encoded, body)
	return encoded, publicLine, fingerprint, nil
}

// PEM is ASCII. Append its JSON representation directly so every secret buffer
// remains mutable and can receive best-effort cleanup.
func appendPEMString(destination, value []byte) []byte {
	const hexDigits = "0123456789abcdef"
	destination = append(destination, '"')
	for _, b := range value {
		switch b {
		case '"', '\\':
			destination = append(destination, '\\', b)
		case '\n':
			destination = append(destination, '\\', 'n')
		case '\r':
			destination = append(destination, '\\', 'r')
		default:
			if b < 0x20 {
				destination = append(destination, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&15])
			} else {
				destination = append(destination, b)
			}
		}
	}
	return append(destination, '"')
}

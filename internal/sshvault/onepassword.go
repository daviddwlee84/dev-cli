package sshvault

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"golang.org/x/crypto/ssh"
)

func (s *Service) createOnePassword(ctx context.Context, state *planState) (Result, error) {
	result := Result{Status: StatusUnknown, BindingStatus: BindingUnknown}
	args := []string{"item", "create", "--category", "ssh", "--title", state.request.Title, "--vault", state.dest.VaultID, "--account", state.dest.AccountID, "--format", "json"}
	body, err := s.run(ctx, "op", args, nil)
	defer sshcredential.Wipe(body)
	if err != nil {
		return result, ErrUnknown
	}
	// item create can return private fields. Do not decode them or retain its
	// complete item; the public key is fetched separately by the returned ID.
	var item struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Category string `json:"category"`
		Vault    struct {
			ID string `json:"id"`
		} `json:"vault"`
	}
	if json.Unmarshal(body, &item) != nil || !opIDPattern.MatchString(item.ID) || item.Title != state.request.Title || item.Category != "SSH_KEY" || item.Vault.ID != state.dest.VaultID {
		return result, ErrUnknown
	}
	result.Status = StatusCreated
	result.Receipt = &Receipt{Destination: state.dest, ItemID: item.ID}
	if ctx.Err() != nil {
		return result, ErrPublicKey
	}
	publicBody, err := s.run(ctx, "op", []string{"item", "get", item.ID, "--vault", state.dest.VaultID, "--account", state.dest.AccountID, "--fields", "label=public key", "--format", "json"}, nil)
	defer sshcredential.Wipe(publicBody)
	if err != nil {
		return result, ErrPublicKey
	}
	var field struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	selected := bytes.TrimSpace(publicBody)
	if len(selected) > 0 && selected[0] == '[' {
		// The CLI's selected-fields JSON may be a one-element array. Do not
		// accept multiple fields or fall back to reading the whole item.
		var fields []json.RawMessage
		if json.Unmarshal(selected, &fields) != nil {
			return result, ErrPublicKey
		}
		defer func() {
			for _, raw := range fields {
				sshcredential.Wipe(raw)
			}
		}()
		if len(fields) != 1 {
			return result, ErrPublicKey
		}
		selected = fields[0]
	}
	if json.Unmarshal(selected, &field) != nil || field.ID != "public_key" || field.Type != "STRING" {
		return result, ErrPublicKey
	}
	var public struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(selected, &public) != nil {
		return result, ErrPublicKey
	}
	line, fingerprint, err := publicIdentity(public.Value)
	if err != nil {
		return result, ErrPublicKey
	}
	result.Receipt.PublicLine, result.Receipt.Fingerprint = line, fingerprint
	return result, nil
}

func publicIdentity(line string) (string, string, error) {
	if len(line) > 4096 {
		return "", "", ErrPublicKey
	}
	line = strings.TrimSpace(line)
	if strings.ContainsAny(line, "\r\n\x00") {
		return "", "", ErrPublicKey
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line + "\n"))
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || key.Type() != ssh.KeyAlgoED25519 {
		return "", "", ErrPublicKey
	}
	// Provider comments are not identity and are not needed in a public-only
	// receipt. Return only the canonical algorithm and public blob.
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), ssh.FingerprintSHA256(key), nil
}

package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/daviddwlee84/dev-cli/internal/machineid"
)

const (
	// ProtocolSchemaVersion is the outer schema shared by new bounded fleet
	// protocols. Feature protocol versions evolve independently inside it.
	ProtocolSchemaVersion = 1
	// LocalFilesProtocolVersion is the first portable local-file protocol.
	LocalFilesProtocolVersion = 1
	// MaxCapabilityBytes bounds either side of capability negotiation.
	MaxCapabilityBytes int64 = 16 << 10
)

var ErrProtocolLimit = errors.New("fleet protocol size limit exceeded")

// FileLimits is the wire representation of a target's downward-clamped
// portable-file policy. It deliberately does not carry include patterns.
type FileLimits struct {
	MaxFiles          int   `json:"max_files"`
	MaxFileBytes      int64 `json:"max_file_bytes"`
	MaxTotalBytes     int64 `json:"max_total_bytes"`
	MaxPathBytes      int   `json:"max_path_bytes"`
	MaxComponentBytes int   `json:"max_component_bytes"`
	MaxPathDepth      int   `json:"max_path_depth"`
}

func (l FileLimits) Validate() error {
	switch {
	case l.MaxFiles <= 0:
		return errors.New("max_files must be positive")
	case l.MaxFileBytes <= 0:
		return errors.New("max_file_bytes must be positive")
	case l.MaxTotalBytes <= 0:
		return errors.New("max_total_bytes must be positive")
	case l.MaxPathBytes <= 0:
		return errors.New("max_path_bytes must be positive")
	case l.MaxComponentBytes <= 0:
		return errors.New("max_component_bytes must be positive")
	case l.MaxPathDepth <= 0:
		return errors.New("max_path_depth must be positive")
	default:
		return nil
	}
}

// CapabilityRequest is content-free and is always exchanged before any
// feature payload.
type CapabilityRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	Feature         string `json:"feature"`
	ProtocolVersion int    `json:"protocol_version"`
}

func LocalFilesCapabilityRequest() CapabilityRequest {
	return CapabilityRequest{
		SchemaVersion: ProtocolSchemaVersion, Feature: "local-files",
		ProtocolVersion: LocalFilesProtocolVersion,
	}
}

func (r CapabilityRequest) Validate() error {
	if r.SchemaVersion != ProtocolSchemaVersion {
		return fmt.Errorf("capability schema_version %d: want %d", r.SchemaVersion, ProtocolSchemaVersion)
	}
	if r.Feature != "local-files" {
		return fmt.Errorf("unsupported capability feature %q", r.Feature)
	}
	if r.ProtocolVersion != LocalFilesProtocolVersion {
		return fmt.Errorf("local-files protocol_version %d: want %d", r.ProtocolVersion, LocalFilesProtocolVersion)
	}
	return nil
}

// CapabilityResponse contains only non-secret host facts. Unsupported native
// platforms return a stable reason before the caller constructs a payload.
type CapabilityResponse struct {
	SchemaVersion   int        `json:"schema_version"`
	Feature         string     `json:"feature"`
	ProtocolVersion int        `json:"protocol_version"`
	MachineID       string     `json:"machine_id"`
	Platform        string     `json:"platform"`
	Supported       bool       `json:"supported"`
	Reason          string     `json:"reason,omitempty"`
	Limits          FileLimits `json:"limits"`
}

func (r CapabilityResponse) Validate(request CapabilityRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if r.SchemaVersion != request.SchemaVersion || r.Feature != request.Feature || r.ProtocolVersion != request.ProtocolVersion {
		return errors.New("capability response does not match the request")
	}
	if err := machineid.Validate(r.MachineID); err != nil {
		return err
	}
	if r.Platform == "" {
		return errors.New("capability response has no platform")
	}
	if r.Supported {
		if r.Reason != "" {
			return errors.New("supported capability must not include a reason")
		}
		if err := r.Limits.Validate(); err != nil {
			return fmt.Errorf("capability limits: %w", err)
		}
	} else if !validCapabilityReason(r.Reason) {
		return errors.New("unsupported capability has no valid reason code")
	}
	return nil
}

func validCapabilityReason(reason string) bool {
	if reason == "" || len(reason) > 128 {
		return false
	}
	for _, character := range reason {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// DecodeStrict reads exactly one bounded JSON value, rejects unknown fields,
// and requires EOF after optional JSON whitespace.
func DecodeStrict(reader io.Reader, maxBytes int64, target any) error {
	if reader == nil || target == nil {
		return errors.New("fleet protocol decode requires a reader and target")
	}
	if maxBytes <= 0 {
		return errors.New("fleet protocol decode requires a positive limit")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return fmt.Errorf("read fleet protocol: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("fleet protocol exceeds %d bytes: %w", maxBytes, ErrProtocolLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode fleet protocol: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode fleet protocol: multiple JSON values")
		}
		return fmt.Errorf("decode fleet protocol trailing data: %w", err)
	}
	return nil
}

// MarshalBounded encodes one protocol value and enforces the matching bound
// before it can be handed to Transport.
func MarshalBounded(value any, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("fleet protocol encode requires a positive limit")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode fleet protocol: %w", err)
	}
	if int64(len(data))+1 > maxBytes {
		return nil, fmt.Errorf("fleet protocol exceeds %d bytes: %w", maxBytes, ErrProtocolLimit)
	}
	return append(data, '\n'), nil
}

// UnmarshalStrict applies DecodeStrict to an in-memory transport response.
func UnmarshalStrict(data []byte, maxBytes int64, target any) error {
	return DecodeStrict(bytes.NewReader(data), maxBytes, target)
}

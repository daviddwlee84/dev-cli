package fleet_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func TestStrictProtocolDecodeRejectsUnknownTrailingAndOversize(t *testing.T) {
	valid := `{"schema_version":1,"feature":"local-files","protocol_version":1}`
	for _, body := range []string{
		`{"schema_version":1,"feature":"local-files","protocol_version":1,"extra":true}`,
		valid + ` {}`,
	} {
		var request fleet.CapabilityRequest
		if err := fleet.DecodeStrict(strings.NewReader(body), 1024, &request); err == nil {
			t.Fatalf("strict decoder accepted %s", body)
		}
	}
	var request fleet.CapabilityRequest
	if err := fleet.DecodeStrict(strings.NewReader(valid), 8, &request); !errors.Is(err, fleet.ErrProtocolLimit) {
		t.Fatalf("oversize error = %v", err)
	}
	if err := fleet.DecodeStrict(strings.NewReader(valid), 1024, &request); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityValidationBindsFeatureVersionMachineAndLimits(t *testing.T) {
	request := fleet.LocalFilesCapabilityRequest()
	response := fleet.CapabilityResponse{
		SchemaVersion: request.SchemaVersion, Feature: request.Feature,
		ProtocolVersion: request.ProtocolVersion,
		MachineID:       "11111111-1111-4111-8111-111111111111",
		Platform:        "linux", Supported: true,
		Limits: fleet.FileLimits{
			MaxFiles: 1, MaxFileBytes: 1, MaxTotalBytes: 1,
			MaxPathBytes: 1, MaxComponentBytes: 1, MaxPathDepth: 1,
		},
	}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.ProtocolVersion++
	if err := response.Validate(request); err == nil {
		t.Fatal("mismatched protocol version was accepted")
	}
	response.ProtocolVersion = request.ProtocolVersion
	response.Supported = false
	response.Limits = fleet.FileLimits{}
	for _, reason := range []string{"", "TOKEN=must-not-reflect", "contains space", strings.Repeat("x", 129)} {
		response.Reason = reason
		if err := response.Validate(request); err == nil {
			t.Errorf("unsupported capability accepted unsafe reason %q", reason)
		}
	}
	response.Reason = "native-windows-acl-transport-disabled"
	if err := response.Validate(request); err != nil {
		t.Fatalf("stable reason code rejected: %v", err)
	}
}

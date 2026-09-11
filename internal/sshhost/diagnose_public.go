package sshhost

import (
	"encoding/json"
	"errors"
	"strings"
)

var diagnosticPublicCodes = wordSet(`prerequisite_unavailable config_unavailable config_invalid config_ready addresses_resolved dns_unavailable route_unavailable route_observed route_source_unproven route_unsupported proxy_path bind_interface_unavailable bind_address_unavailable scope_required tcp_failed tcp_connected connection_refused connect_timeout canceled non_ssh_banner banner_failed banner_timeout ssh_banner not_requested qos_comparison_unavailable path_not_comparable qos_already_disabled baseline_ready qos_marking_unproven no_transport_timeout qos_no_progress qos_correlated_progress not_observed handshake_complete host_key_verified authenticated remote_exit_zero host_key_unknown host_key_changed host_key_rejected host_identity_reached authentication_denied agent_refused handshake_timeout session_failed ssh_unavailable ssh_failed evidence_truncated proxy_path_failed proxy_stage_unresolved ready`)

func wordSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, word := range strings.Fields(words) {
		m[word] = true
	}
	return m
}

// ParsePublicDiagnosis distrusts every string in an imported local JSON report.
// Only finite codes and bounded durations survive; endpoint/user/path data never
// enters the public projection, even when input fields are forged.
func ParsePublicDiagnosis(data []byte) (PublicDiagnosis, error) {
	if len(data) > 1<<20 {
		return PublicDiagnosis{}, errors.New("SSH diagnosis exceeds 1 MiB")
	}
	var d Diagnosis
	if json.Unmarshal(data, &d) != nil || d.SchemaVersion != 1 || d.Kind != "ssh_diagnosis" {
		return PublicDiagnosis{}, errors.New("expected schema-v1 SSH diagnosis")
	}
	return d.PublicEvidence(), nil
}
func (d Diagnosis) PublicEvidence() PublicDiagnosis {
	p := PublicDiagnosis{SchemaVersion: 1, Kind: "public_ssh_diagnosis", Status: "incomplete", Stages: []DiagnosticStage{}, AttemptCodes: []string{}, Findings: []string{}}
	if d.Status == "ready" || d.Status == "not_ready" {
		p.Status = d.Status
	}
	states := wordSet("passed failed unknown skipped unsupported canceled")
	stages := wordSet("config dns route tcp banner qos")
	for _, stage := range d.Stages {
		if !stages[stage.Name] {
			continue
		}
		delete(stages, stage.Name)
		if !states[stage.State] {
			stage.State = "unknown"
		}
		if !diagnosticPublicCodes[stage.Code] {
			stage.Code = "not_observed"
		}
		if stage.ElapsedMS < 0 || stage.ElapsedMS > 86400000 {
			stage.ElapsedMS = 0
		}
		p.Stages = append(p.Stages, stage)
	}
	for i, a := range d.Attempts {
		if i >= 2 {
			break
		}
		code := a.Code
		if !diagnosticPublicCodes[code] {
			code = "not_observed"
		}
		p.AttemptCodes = append(p.AttemptCodes, code)
	}
	seenFindings := map[string]bool{}
	for _, code := range d.Findings {
		if (code == "qos_correlated_progress" || code == "tcp_ssh_endpoints_differ") && !seenFindings[code] {
			p.Findings = append(p.Findings, code)
			seenFindings[code] = true
		}
	}
	return p
}

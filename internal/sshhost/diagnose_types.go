package sshhost

import (
	"context"
	"net"
	"time"
)

// DiagnosticHooks supplies network observation seams; nil fields use native
// read-only collectors. Neither inventory nor ordinary Probe uses these hooks.
type DiagnosticHooks struct {
	LookupIP func(context.Context, string) ([]net.IPAddr, error)
	Dial     func(context.Context, string, string) (net.Conn, error)
	Route    func(context.Context, DiagnosticRouteQuery) (DiagnosticRoute, error)
}

type DiagnoseRequest struct {
	Target     string
	CompareQoS bool
	Timeout    time.Duration
}

type DiagnosticTarget struct {
	Name          string   `json:"name"`
	Hostname      string   `json:"hostname,omitempty"`
	Port          int      `json:"port,omitempty"`
	User          string   `json:"user,omitempty"`
	IdentityFiles []string `json:"identity_files,omitempty"`
	IPQoS         string   `json:"ipqos,omitempty"`
	Proxy         string   `json:"proxy"`
	AddressFamily string   `json:"address_family,omitempty"`
	BindAddress   string   `json:"bind_address,omitempty"`
	BindInterface string   `json:"bind_interface,omitempty"`
}

type DiagnosticStage struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Code      string `json:"code"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type DiagnosticRouteQuery struct {
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Source    string `json:"source,omitempty"`
	Interface string `json:"interface,omitempty"`
}

type DiagnosticRoute struct {
	Address     string `json:"address"`
	Family      string `json:"family"`
	Interface   string `json:"interface,omitempty"`
	Source      string `json:"source,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	Destination string `json:"destination,omitempty"`
	Code        string `json:"code"`
}

type DiagnosticAttempt struct {
	Name      string            `json:"name"`
	Stages    []DiagnosticStage `json:"stages"`
	Endpoint  string            `json:"endpoint,omitempty"`
	Port      int               `json:"port,omitempty"`
	QoSMarked bool              `json:"qos_marking_observed"`
	Ready     bool              `json:"ready"`
	ElapsedMS int64             `json:"elapsed_ms"`
	Truncated bool              `json:"truncated"`
	Code      string            `json:"code"`
}

// Diagnosis contains local endpoint information but never raw subprocess output.
// PublicEvidence is the separate allowlisted attachment for feedback.
type Diagnosis struct {
	SchemaVersion    int                 `json:"schema_version"`
	Kind             string              `json:"kind"`
	Privacy          string              `json:"privacy"`
	Status           string              `json:"status"`
	Target           DiagnosticTarget    `json:"target"`
	Stages           []DiagnosticStage   `json:"stages"`
	Addresses        []string            `json:"addresses,omitempty"`
	AddressesLimited bool                `json:"addresses_limited"`
	Routes           []DiagnosticRoute   `json:"routes,omitempty"`
	SocketLocal      string              `json:"socket_local,omitempty"`
	SocketRemote     string              `json:"socket_remote,omitempty"`
	Attempts         []DiagnosticAttempt `json:"attempts"`
	Findings         []string            `json:"findings"`
	SuggestedActions []string            `json:"suggested_actions"`
}

// PublicEvidence copies only finite, program-owned codes and numeric results.
// Consumers must decode through ParsePublicDiagnosis rather than trusting a
// caller-supplied JSON object that merely claims to be a Diagnosis.
type PublicDiagnosis struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	Stages        []DiagnosticStage `json:"stages"`
	AttemptCodes  []string          `json:"attempt_codes"`
	Findings      []string          `json:"findings"`
}

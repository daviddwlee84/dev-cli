package sshhost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

// Use a typed reply on Windows: ping.exe's translated prose cannot establish
// success or round-trip time across locales. No user text is interpolated.
const diagnosticWindowsPingScript = `$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$q = [Console]::In.ReadToEnd() | ConvertFrom-Json
$p = [System.Net.NetworkInformation.Ping]::new()
try {
 $r = $p.Send([string]$q.address, 1000)
 [ordered]@{ success = ($r.Status -eq [System.Net.NetworkInformation.IPStatus]::Success); round_trip_ms = [int64]$r.RoundtripTime } | ConvertTo-Json -Compress
} finally { $p.Dispose() }
`

var pingRoundTrip = regexp.MustCompile(`(?i)time\s*([=<])\s*([0-9]+(?:\.[0-9]+)?)\s*ms`)

// Ping is an independent ICMP observation. A missing reply never prevents the
// ordinary TCP/SSH diagnostic and does not prove an endpoint is offline.
func (s *Service) diagnosePing(ctx context.Context, d *Diagnosis, hooks DiagnosticHooks) {
	started := time.Now()
	if d.Target.Proxy != "none" {
		d.stage("ping", "skipped", "proxy_path", started)
		return
	}
	if d.Target.BindAddress != "" || d.Target.BindInterface != "" {
		d.stage("ping", "unsupported", "ping_bind_unsupported", started)
		return
	}
	if len(d.Addresses) == 0 {
		return
	}
	address := d.Addresses[0]
	if host, _, err := net.SplitHostPort(d.SocketRemote); err == nil {
		address = host
	}
	ping := hooks.Ping
	if ping == nil {
		ping = func(ctx context.Context, q DiagnosticRouteQuery) (DiagnosticStage, error) {
			return collectDiagnosticPing(ctx, s.runner, runtime.GOOS, q)
		}
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stage, err := ping(pingCtx, DiagnosticRouteQuery{Address: address, Port: d.Target.Port})
	stage.Name, stage.ElapsedMS = "ping", time.Since(started).Milliseconds()
	if ctx.Err() != nil {
		stage.State, stage.Code = "canceled", "canceled"
	} else if stage.State == "" || err != nil && stage.State == "passed" {
		stage.State, stage.Code = "unknown", "ping_no_reply"
	}
	for i := range d.Stages {
		if d.Stages[i].Name == "ping" {
			d.Stages[i] = stage
		}
	}
}

func collectDiagnosticPing(ctx context.Context, runner Runner, goos string, q DiagnosticRouteQuery) (DiagnosticStage, error) {
	stage := DiagnosticStage{Name: "ping", State: "unknown", Code: "ping_no_reply"}
	ip, err := netip.ParseAddr(q.Address)
	if err != nil || q.Source != "" || q.Interface != "" || ip.Zone() != "" {
		stage.State, stage.Code = "unsupported", "ping_bind_unsupported"
		return stage, errors.New("ping requires an unbound literal endpoint")
	}
	req := RunRequest{Name: "ping", Env: []string{"LC_ALL=C"}, Display: "SSH diagnostic ICMP echo", StdoutLimit: 8192}
	switch goos {
	case "darwin":
		req.Name = "/sbin/ping"
		if ip.Is6() {
			req.Name = "/sbin/ping6"
		}
		req.Args = []string{"-n", "-c", "1", ip.String()}
	case "linux":
		family := "-4"
		if ip.Is6() {
			family = "-6"
		}
		req.Args = []string{family, "-n", "-c", "1", "-W", "1", ip.String()}
	case "windows":
		req.Name = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		req.Args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedDiagnosticScript(diagnosticWindowsPingScript)}
		req.Stdin, _ = json.Marshal(q)
	default:
		stage.State, stage.Code = "unsupported", "ping_unavailable"
		return stage, errors.New("ping platform unsupported")
	}
	run, err := runner.Run(ctx, req)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			stage.State, stage.Code = "unsupported", "ping_unavailable"
		}
		return stage, err
	}
	if run.ExitCode != 0 || run.StdoutTruncated {
		return stage, nil
	}
	if goos == "windows" {
		var reply struct {
			Success     bool     `json:"success"`
			RoundTripMS *float64 `json:"round_trip_ms"`
		}
		if json.Unmarshal(run.Stdout, &reply) == nil && reply.Success && reply.RoundTripMS != nil && *reply.RoundTripMS >= 0 && *reply.RoundTripMS <= 2000 {
			stage.State, stage.Code, stage.RoundTripMS = "passed", "ping_reply", reply.RoundTripMS
		}
		return stage, nil
	}
	// An exit code alone is insufficient on Windows (a gateway can report an
	// unreachable host with status zero). Only a measured echo is a pass.
	if match := pingRoundTrip.FindSubmatch(run.Stdout); match != nil {
		if value, e := strconv.ParseFloat(string(match[2]), 64); e == nil && value >= 0 && value <= 2000 {
			stage.State, stage.Code, stage.RoundTripMS = "passed", "ping_reply", &value
			stage.RoundTripUpperBound = string(match[1]) == "<"
		}
	} else if goos != "windows" {
		stage.State, stage.Code = "passed", "ping_reply"
	}
	return stage, nil
}

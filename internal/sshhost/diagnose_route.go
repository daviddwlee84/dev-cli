package sshhost

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf16"
)

const diagnosticWindowsRouteScript = `$ErrorActionPreference = 'Stop'
$q = [Console]::In.ReadToEnd() | ConvertFrom-Json
$p = @{RemoteIPAddress = [string]$q.address}
if ($q.source) { $p.LocalIPAddress = [string]$q.source }
if ($q.interface) { $p.InterfaceIndex = [uint32]$q.interface }
$items = @(Find-NetRoute @p)
$r = $items | Where-Object { $_.CimClass.CimClassName -eq 'MSFT_NetRoute' } | Select-Object -First 1
$a = $items | Where-Object { $_.CimClass.CimClassName -eq 'MSFT_NetIPAddress' } | Select-Object -First 1
if (!$r) { throw 'route unavailable' }
[ordered]@{ interface = [string]$r.InterfaceIndex; source = [string]$a.IPAddress; gateway = [string]$r.NextHop; destination = [string]$r.DestinationPrefix } | ConvertTo-Json -Compress
`

func encodedDiagnosticScript(script string) string {
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(data)
}
func (s *Service) diagnosticRoute(ctx context.Context, q DiagnosticRouteQuery) (DiagnosticRoute, error) {
	return collectDiagnosticRoute(ctx, s.runner, runtime.GOOS, q)
}
func collectDiagnosticRoute(ctx context.Context, runner Runner, goos string, q DiagnosticRouteQuery) (DiagnosticRoute, error) {
	result := DiagnosticRoute{Address: q.Address, Code: "route_unavailable"}
	ip, err := netip.ParseAddr(q.Address)
	if err != nil {
		return result, errors.New("invalid route address")
	}
	result.Family = "ipv6"
	if ip.Is4() {
		result.Family = "ipv4"
	}
	if q.Source != "" {
		if _, err := netip.ParseAddr(q.Source); err != nil {
			result.Code = "route_source_unproven"
			return result, errors.New("route source is not an IP literal")
		}
	}
	zone := ip.Zone()
	if q.Interface == "" {
		q.Interface = zone
	}
	if zone != "" && q.Interface != zone {
		return result, errors.New("conflicting route scope")
	}
	if ip.IsLinkLocalUnicast() && q.Interface == "" {
		result.Code = "scope_required"
		return result, errors.New("IPv6 route requires interface scope")
	}
	q.Address = ip.WithZone("").String()
	req := RunRequest{Display: "SSH diagnostic route observation", Env: []string{"LC_ALL=C"}}
	switch goos {
	case "darwin":
		family := "-inet6"
		if ip.Is4() {
			family = "-inet"
		}
		req.Name = "/sbin/route"
		req.Args = []string{"-n", "get", family, q.Address}
		if q.Interface != "" {
			req.Args = append(req.Args, "-ifscope", q.Interface)
		}
	case "linux":
		req.Name = "ip"
		if _, err := exec.LookPath(req.Name); err != nil {
			for _, path := range []string{"/usr/sbin/ip", "/sbin/ip"} {
				if st, e := os.Stat(path); e == nil && !st.IsDir() {
					req.Name = path
					break
				}
			}
		}
		family := "-6"
		if ip.Is4() {
			family = "-4"
		}
		req.Args = []string{"-j", family, "route", "get", q.Address}
		if q.Source != "" {
			req.Args = append(req.Args, "from", q.Source)
		}
		if q.Interface != "" {
			req.Args = append(req.Args, "oif", q.Interface)
		}
		req.Args = append(req.Args, "ipproto", "tcp", "dport", strconv.Itoa(q.Port))
	case "windows":
		if q.Interface != "" {
			if index, err := strconv.Atoi(q.Interface); err != nil || index <= 0 {
				iface, lookupErr := net.InterfaceByName(q.Interface)
				if lookupErr != nil {
					return result, errors.New("route interface unavailable")
				}
				q.Interface = strconv.Itoa(iface.Index)
			}
		}
		req.Name = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		req.Args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedDiagnosticScript(diagnosticWindowsRouteScript)}
		req.Stdin, _ = json.Marshal(q)
	default:
		result.Code = "route_unsupported"
		return result, errors.New("route collector unsupported")
	}
	run, err := runner.Run(ctx, req)
	if err != nil || run.ExitCode != 0 || run.StdoutTruncated {
		return result, errors.New("route observation unavailable")
	}
	result, err = parseDiagnosticRoute(goos, result, run.Stdout)
	if err == nil && goos == "darwin" && q.Source != "" {
		result.Code = "route_source_unproven"
	}
	return result, err
}
func parseDiagnosticRoute(goos string, result DiagnosticRoute, data []byte) (DiagnosticRoute, error) {
	switch goos {
	case "darwin":
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if !ok {
				continue
			}
			value = diagnosticText(strings.TrimSpace(value))
			switch key {
			case "interface":
				result.Interface = value
			case "gateway":
				result.Gateway = value
			case "destination":
				result.Destination = value
			}
		}
	case "linux":
		var rows []struct {
			Dev     string `json:"dev"`
			Gateway string `json:"gateway"`
			Prefsrc string `json:"prefsrc"`
			Src     string `json:"src"`
			Dst     string `json:"dst"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(data, &rows) != nil || len(rows) != 1 {
			return result, errors.New("invalid route observation")
		}
		row := rows[0]
		result.Interface = diagnosticText(row.Dev)
		result.Gateway = diagnosticText(row.Gateway)
		result.Source = diagnosticText(row.Prefsrc)
		if result.Source == "" {
			result.Source = diagnosticText(row.Src)
		}
		result.Destination = diagnosticText(row.Dst)
		if row.Type == "unreachable" || row.Type == "blackhole" || row.Type == "prohibit" {
			return result, errors.New("route does not permit traffic")
		}
	case "windows":
		var row struct {
			Interface   string `json:"interface"`
			Source      string `json:"source"`
			Gateway     string `json:"gateway"`
			Destination string `json:"destination"`
		}
		if json.Unmarshal(data, &row) != nil {
			return result, errors.New("invalid route observation")
		}
		result.Interface = diagnosticText(row.Interface)
		result.Source = diagnosticText(row.Source)
		result.Gateway = diagnosticText(row.Gateway)
		result.Destination = diagnosticText(row.Destination)
	}
	if result.Interface == "" {
		return result, errors.New("route interface unavailable")
	}
	result.Code = "route_observed"
	return result, nil
}

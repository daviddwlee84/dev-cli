package agentinterop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const mcpProtocol = "2025-06-18"

type ConnectionResult struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Authentication string `json:"authentication"`
	ClientLoaded   string `json:"client_loaded"`
}

func initialization() []byte {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": mcpProtocol, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "dev-cli", "version": "interop-v1"}}})
	return append(data, '\n')
}

var initialized = []byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")

func validateInitialization(data []byte) error {
	var reply struct {
		Version string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			Protocol string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.Version != "2.0" || reply.ID != 1 || len(reply.Error) > 0 || reply.Result.Protocol != mcpProtocol {
		return errors.New("MCP initialization did not negotiate the supported protocol")
	}
	return nil
}

func (s Service) Check(ctx context.Context, id string) (out ConnectionResult, err error) {
	out = ConnectionResult{ID: id, Status: "configured", Authentication: "not-checked", ClientLoaded: "not-checked"}
	st, err := s.open(false)
	if err != nil {
		return out, err
	}
	defer st.close()
	r, err := st.load(ctx, id)
	if err != nil {
		return out, err
	}
	if r.Request.Kind != "mcp" || r.Status != "applied" {
		return out, errors.New("connection checks require an applied MCP transfer")
	}
	h, err := openRoots(r)
	if err != nil {
		return out, err
	}
	defer h.close()
	if err = verifyReceipt(ctx, st, h, r); err != nil {
		return out, err
	}
	if err = validateMCPPolicy(ctx, r.Request); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if r.Bridge != nil {
		spec, e := s.prepareLaunch(ctx, id)
		if e != nil {
			return out, e
		}
		err = checkStdio(ctx, spec)
	} else {
		to := r.Request.To
		_, data, e := snapshot(ctx, h.roots[to.Root], to.Path, st.key)
		if e != nil {
			return out, e
		}
		fields, found, e := mcpFields(data, to.Agent, r.Request.TargetName)
		if e != nil || !found {
			return out, errors.New("target declaration is unavailable")
		}
		d, e := parseMCP(to.Agent, fields, "streamable-http")
		if e != nil {
			return out, e
		}
		if d.Transport == "streamable-http" {
			err = checkHTTP(ctx, d)
		} else {
			program, e := trustedMCPExecutable(d.Command, projectRoots(r.Request)...)
			if e != nil {
				return out, e
			}
			spec := launchSpec{Program: program, Args: append([]string{program}, d.Args...), Dir: to.Root}
			if d.Cwd != "" {
				spec.Dir = d.Cwd
			}
			for _, name := range []string{"PATH", "HOME", "USER", "LANG", "TMPDIR", "TMP", "TEMP", "SystemRoot", "COMSPEC", "PATHEXT"} {
				if value, ok := os.LookupEnv(name); ok {
					spec.Env = append(spec.Env, name+"="+value)
				}
			}
			for name, v := range d.Env {
				value, e := resolveProcessReference(v)
				if e != nil {
					return out, e
				}
				spec.Env = append(spec.Env, name+"="+value)
			}
			err = checkStdio(ctx, spec)
		}
	}
	if err == nil {
		out.Status = "initialized"
	}
	return out, err
}

func resolveProcessReference(v envValue) (string, error) {
	if v.Variable == "" {
		return v.Literal, nil
	}
	value, ok := os.LookupEnv(v.Variable)
	if !ok && v.Fallback != nil {
		value, ok = *v.Fallback, true
	}
	if !ok {
		return "", errors.New("required client environment reference is unavailable")
	}
	if strings.ContainsRune(value, 0) {
		return "", errors.New("invalid environment value")
	}
	return v.Prefix + value, nil
}

func checkStdio(ctx context.Context, spec launchSpec) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Program, spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stderr = io.Discard
	cleanup, err := prepareCheckProcess(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if cmd.Start() != nil {
		return errors.New("MCP check process could not start")
	}
	if err = attachCheckProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	defer func() { _ = in.Close(); cancel(); _ = cmd.Wait() }()
	if _, err = in.Write(initialization()); err != nil {
		return errors.New("MCP initialize write failed")
	}
	decoder := json.NewDecoder(io.LimitReader(out, 1<<20))
	var raw json.RawMessage
	for n := 0; n < 32; n++ {
		if decoder.Decode(&raw) != nil {
			return errors.New("MCP returned no bounded initialization response")
		}
		var meta struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &meta) != nil {
			return errors.New("invalid MCP message")
		}
		if meta.ID == nil && meta.Method != "" {
			continue
		}
		if err = validateInitialization(raw); err != nil {
			return err
		}
		if _, err = in.Write(initialized); err != nil {
			return errors.New("MCP initialized notification failed")
		}
		return nil
	}
	return errors.New("MCP initialization message limit exceeded")
}

func checkHTTP(ctx context.Context, d mcpDefinition) error {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(body []byte, session string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("invalid MCP HTTP request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		if bytes.Equal(body, initialized) {
			req.Header.Set("MCP-Protocol-Version", mcpProtocol)
		}
		for name, ref := range d.Headers {
			value, e := resolveProcessReference(ref)
			if e != nil {
				return nil, e
			}
			if strings.ContainsAny(value, "\r\n\x00") {
				return nil, errors.New("invalid MCP header reference value")
			}
			req.Header.Set(name, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("MCP HTTP connection failed")
		}
		return response, nil
	}
	response, err := post(initialization(), "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("MCP HTTP initialization was not accepted; authentication may be required")
	}
	var data []byte
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(response.Body, 1<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" && len(data) > 0 {
				break
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))...)
				data = append(data, '\n')
			}
		}
	} else {
		data, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return errors.New("MCP initialization response limit exceeded")
		}
	}
	if err = validateInitialization(data); err != nil {
		return err
	}
	session := response.Header.Get("Mcp-Session-Id")
	notified, err := post(initialized, session)
	if err != nil {
		return err
	}
	defer notified.Body.Close()
	if notified.StatusCode < 200 || notified.StatusCode >= 300 {
		return errors.New("MCP initialized notification was not accepted")
	}
	return nil
}

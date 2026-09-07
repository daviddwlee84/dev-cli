package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func rawFields(fields map[string]any) map[string]json.RawMessage {
	if fields == nil {
		return nil
	}
	data, _ := json.Marshal(fields)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	return raw
}
func anyFields(raw map[string]json.RawMessage) map[string]any {
	if raw == nil {
		return nil
	}
	out := map[string]any{}
	for key, value := range raw {
		var decoded any
		_ = json.Unmarshal(value, &decoded)
		out[key] = decoded
	}
	return out
}

// A plan guards whole file identities until apply; a durable MCP ownership
// receipt owns only its selected stanza. Unrelated later client edits must not
// disable a launcher or be overwritten by refresh/undo.
func verifyReceipt(ctx context.Context, st *store, h *heldRoots, r record) error {
	if r.Request.Kind != "mcp" || len(r.MCPEdits) == 0 {
		return verifyPost(ctx, st, h, r)
	}
	for _, edit := range r.MCPEdits {
		img, data, err := snapshot(ctx, h.roots[edit.Root], edit.Path, st.key)
		if err != nil || (img.Kind != "file" && img.Kind != "absent") {
			return ErrStale
		}
		fields, exists, err := mcpFields(data, edit.Agent, edit.Name)
		if err != nil {
			return ErrStale
		}
		if edit.After == nil {
			if exists {
				return ErrStale
			}
		} else if !exists || !fieldEquivalent(fields, anyFields(edit.After)) {
			return ErrStale
		}
	}
	return nil
}

func findMCPOwner(b *builder, req TransferRequest, target Location) (*record, error) {
	f, err := b.st.root.Open(".")
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	var owner *record
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		r, e := b.st.load(b.ctx, strings.TrimSuffix(name, ".json"))
		if e != nil {
			return nil, e
		}
		if r.Status != "applied" || r.Request.Kind != "mcp" || r.Request.Mode == "undo" || len(r.MCPEdits) == 0 {
			continue
		}
		if r.Request.To == req.To && r.Request.TargetName == req.TargetName {
			if owner != nil {
				return nil, errors.New("multiple active MCP ownership records; inspect and undo the conflicting relation")
			}
			h, e := openRoots(r)
			if e != nil {
				return nil, e
			}
			e = verifyReceipt(b.ctx, b.st, h, r)
			h.close()
			if e != nil {
				return nil, e
			}
			copy := r
			owner = &copy
		}
	}
	return owner, nil
}

func planMCPUndo(b *builder, r record) error {
	dataByPath := map[Location][]byte{}
	order := []Location{}
	for i := len(r.MCPEdits) - 1; i >= 0; i-- {
		e := r.MCPEdits[i]
		data, ok := dataByPath[e.Location]
		if !ok {
			_, current, err := b.read(e.Location)
			if err != nil {
				return err
			}
			data = current
			order = append(order, e.Location)
		}
		patched, err := patchMCP(data, e.Agent, e.Name, anyFields(e.Before), e.Before == nil)
		if err != nil {
			return err
		}
		dataByPath[e.Location] = patched
		b.r.MCPEdits = append(b.r.MCPEdits, mcpEdit{Location: e.Location, Agent: e.Agent, Name: e.Name, Before: e.After, After: e.Before})
	}
	for _, l := range order {
		if err := b.write(l, dataByPath[l], 0o600, true); err != nil {
			return err
		}
	}
	b.r.Notes = append(b.r.Notes, "Undo restores only the selected MCP stanzas. Unrelated settings/comments and empty native container files are retained.")
	return nil
}

package server

import (
	"encoding/json"
	"testing"
)

func TestConnectorConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	in := []connectorConfig{
		{ID: "kb", Name: "知识库", Command: "/usr/bin/kb-mcp", Args: []string{"--index", "/data"}, Enabled: true, Risk: "read"},
		{ID: "web", Command: "/usr/bin/web-mcp", Enabled: false},
	}
	if err := saveConnectors(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := loadConnectors()
	if len(got) != 2 {
		t.Fatalf("loaded %d connectors, want 2", len(got))
	}
	if got[0].ID != "kb" || got[0].Risk != "read" || !got[0].Enabled || len(got[0].Args) != 2 {
		t.Fatalf("connector[0] = %+v", got[0])
	}
	if got[1].Enabled {
		t.Fatalf("connector[1] should be disabled: %+v", got[1])
	}
}

// TestConnectorBadCommandRecordsError ensures a broken connector fails softly:
// no tools, an error string, and the server still works.
func TestConnectorBadCommandRecordsError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New()
	defer s.Close()

	cfg := connectorConfig{ID: "bad", Name: "Bad", Command: "/nonexistent/edgekit-mcp", Enabled: true}
	if err := saveConnectors([]connectorConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	s.conns.mu.Lock()
	s.conns.m["bad"] = &connectorState{cfg: cfg}
	s.conns.mu.Unlock()

	s.conns.connect("bad")

	var found connectorView
	for _, v := range s.conns.views() {
		if v.ID == "bad" {
			found = v
		}
	}
	if found.Connected {
		t.Fatal("broken connector must not report connected")
	}
	if found.Error == "" {
		t.Fatal("broken connector should record an error")
	}
	if _, ok := s.kits.Tool("bad_anything"); ok {
		t.Fatal("failed connector must not register tools")
	}
}

// TestConnectorAddRemoveViaProtocol exercises the UI-facing add/remove path used
// by the right-hand MCP panel to attach an external service.
func TestConnectorAddRemoveViaProtocol(t *testing.T) {
	_, c := startE2EServer(t)
	c.until("connectors") // drain the initial status snapshot

	c.send("connectors.add", map[string]any{
		"id": "kb", "name": "知识库", "command": "/nonexistent/kb-mcp",
		"args": []string{"--index", "/data"}, "risk": "read", "enabled": false,
	})

	var v struct {
		Connectors []connectorView `json:"connectors"`
	}
	decodeConnectors(t, c.until("connectors").Payload, &v)
	if len(v.Connectors) != 1 || v.Connectors[0].ID != "kb" || v.Connectors[0].Name != "知识库" {
		t.Fatalf("connectors = %+v", v.Connectors)
	}
	onDisk := loadConnectors()
	if len(onDisk) != 1 || onDisk[0].ID != "kb" || onDisk[0].Risk != "read" || len(onDisk[0].Args) != 2 {
		t.Fatalf("persisted = %+v", onDisk)
	}

	c.send("connectors.remove", map[string]any{"id": "kb"})
	decodeConnectors(t, c.until("connectors").Payload, &v)
	if len(v.Connectors) != 0 {
		t.Fatalf("after remove connectors = %+v", v.Connectors)
	}
	if got := loadConnectors(); len(got) != 0 {
		t.Fatalf("after remove persisted = %+v", got)
	}
}

// TestConnectorAddRejectsBadID ensures an unsafe namespace is refused.
func TestConnectorAddRejectsBadID(t *testing.T) {
	_, c := startE2EServer(t)
	c.until("connectors")

	c.send("connectors.add", map[string]any{"id": "bad id!", "command": "/bin/true", "enabled": false})
	m := c.until("error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(m.Payload, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Message == "" {
		t.Fatal("expected an error message")
	}
	if got := loadConnectors(); len(got) != 0 {
		t.Fatalf("invalid connector must not persist: %+v", got)
	}
}

func decodeConnectors(t *testing.T, payload json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(payload, v); err != nil {
		t.Fatalf("decode connectors: %v", err)
	}
}

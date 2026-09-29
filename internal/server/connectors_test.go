package server

import "testing"

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

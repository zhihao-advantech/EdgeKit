package kits

import (
	"context"
	"testing"

	"edgekit/internal/kit"
	"edgekit/internal/mcpclient"
)

type fakeSource struct {
	tools []mcpclient.Tool
	alive bool
}

func (f *fakeSource) Tools() []mcpclient.Tool { return f.tools }
func (f *fakeSource) Alive() bool             { return f.alive }
func (f *fakeSource) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	return "ok:" + name, nil
}

func TestMCPKitNamespacingAndRisk(t *testing.T) {
	src := &fakeSource{alive: true, tools: []mcpclient.Tool{
		{Name: "search", Description: "检索"},
		{Name: "weird.name/x", Description: "bad chars"},
	}}

	read := NewMCPKit("kb", "知识库", src, kit.RiskRead)
	if m := read.Manifest(); m.Runtime != "mcp" || m.ID != "edgekit.connector.kb" {
		t.Fatalf("manifest = %+v", m)
	}
	tools := read.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Name != "kb_search" || tools[0].Risk != kit.RiskRead {
		t.Fatalf("tool[0] = %+v", tools[0])
	}
	if tools[1].Name != "kb_weird_name_x" {
		t.Fatalf("tool[1] name should be sanitised, got %q", tools[1].Name)
	}
	out, err := tools[0].Call(context.Background(), nil)
	if err != nil || out != "ok:search" {
		t.Fatalf("call = %q, %v", out, err)
	}

	// Unknown risk defaults to mutate (approval-gated).
	def := NewMCPKit("x", "X", src, "")
	if def.Tools()[0].Risk != kit.RiskMutate {
		t.Fatal("external tools must default to mutate risk")
	}

	// A dead client reports an error rather than hanging.
	src.alive = false
	if _, err := tools[0].Call(context.Background(), nil); err == nil {
		t.Fatal("dead connector should error")
	}
}

func TestRegistryReplaceAndRemove(t *testing.T) {
	reg := kit.NewRegistry()
	src := &fakeSource{alive: true, tools: []mcpclient.Tool{{Name: "a"}}}
	reg.Replace(NewMCPKit("kb", "KB", src, kit.RiskRead))

	if id, ok := reg.ToolKit("kb_a"); !ok || id != "edgekit.connector.kb" {
		t.Fatalf("kb_a not registered: %q %v", id, ok)
	}

	// Reconnect with an updated tool set replaces the old one.
	src.tools = []mcpclient.Tool{{Name: "a"}, {Name: "b"}}
	reg.Replace(NewMCPKit("kb", "KB", src, kit.RiskRead))
	if got := len(reg.KitTools("edgekit.connector.kb")); got != 2 {
		t.Fatalf("after replace expected 2 tools, got %d", got)
	}
	// Manifests must not be duplicated.
	if got := len(reg.Manifests()); got != 1 {
		t.Fatalf("replace duplicated the kit: %d manifests", got)
	}

	reg.Remove("edgekit.connector.kb")
	if _, ok := reg.Tool("kb_a"); ok {
		t.Fatal("removed connector tools must disappear")
	}
	if got := len(reg.Manifests()); got != 0 {
		t.Fatalf("remove left manifests: %d", got)
	}
}

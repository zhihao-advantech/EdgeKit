package kits

import (
	"testing"

	"edgekit/internal/kit"
)

func buildRegistry() *kit.Registry {
	reg := kit.NewRegistry()
	for _, k := range Builtin(kit.Deps{}) {
		reg.Register(k)
	}
	return reg
}

func TestBuiltinKitsContributeTools(t *testing.T) {
	reg := buildRegistry()
	if got := len(reg.Tools()); got != 18 {
		t.Fatalf("expected 18 tools from the built-in kits, got %d", got)
	}
	if got := len(reg.Manifests()); got != 6 {
		t.Fatalf("expected 6 kits, got %d", got)
	}
}

func TestManifestsAreWellFormed(t *testing.T) {
	reg := buildRegistry()
	for _, m := range reg.Manifests() {
		if m.ID == "" || m.Name == "" || m.Version == "" {
			t.Fatalf("manifest missing identity: %+v", m)
		}
		if m.License == "" {
			t.Fatalf("manifest %s has no license", m.ID)
		}
		if m.Runtime != "builtin" {
			t.Fatalf("manifest %s runtime = %q, want builtin", m.ID, m.Runtime)
		}
		if len(m.Activation) == 0 {
			t.Fatalf("manifest %s declares no activation", m.ID)
		}
	}
}

func TestToolRiskClassification(t *testing.T) {
	reg := buildRegistry()
	mutating := []string{"local_exec", "serial_write", "serial_exec", "ssh_exec", "sftp_upload", "workspace_write"}
	for _, name := range mutating {
		tool, ok := reg.Tool(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		if !tool.Mutating() {
			t.Fatalf("tool %s should require approval", name)
		}
	}
	readOnly := []string{"local_info", "net_ping", "net_check_port", "net_resolve",
		"serial_status", "serial_read", "ssh_status", "sftp_status", "sftp_list",
		"sftp_download", "workspace_list", "workspace_read"}
	for _, name := range readOnly {
		tool, ok := reg.Tool(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		if tool.Mutating() {
			t.Fatalf("tool %s should be read-only", name)
		}
	}
	if _, ok := reg.Tool("does_not_exist"); ok {
		t.Fatal("unknown tool should not resolve")
	}
}

func TestRegistryIgnoresDuplicateToolNames(t *testing.T) {
	reg := kit.NewRegistry()
	reg.Register(hostKit{})
	reg.Register(hostKit{})
	if got := len(reg.Tools()); got != 2 {
		t.Fatalf("duplicate kit should not add tools again, got %d", got)
	}
}

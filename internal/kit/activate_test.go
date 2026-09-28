package kit

import (
	"context"
	"testing"
)

// fakeKit is a minimal kit for exercising the activation engine.
type fakeKit struct {
	manifest Manifest
	tools    []Tool
}

func (k fakeKit) Manifest() Manifest { return k.manifest }
func (k fakeKit) Tools() []Tool      { return k.tools }

func fakeTool(name string) Tool {
	return Tool{Name: name, Call: func(context.Context, map[string]any) (string, error) { return "", nil }}
}

func TestRegistryActivationEvents(t *testing.T) {
	reg := NewRegistry()
	reg.Register(fakeKit{
		manifest: Manifest{ID: "k.serial", Activation: []string{DeviceKindEvent("serial")}},
		tools:    []Tool{fakeTool("serial_read")},
	})
	reg.Register(fakeKit{
		manifest: Manifest{ID: "k.host", Activation: []string{EventStartup}},
		tools:    []Tool{fakeTool("local_info")},
	})
	reg.Register(fakeKit{
		manifest: Manifest{ID: "k.dev", Activation: []string{DeviceKindEvent("serial"), DeviceKindEvent("ssh")}},
		tools:    []Tool{fakeTool("wait_for_output")},
	})

	// onStartup is satisfied from the beginning; device kinds are not.
	if _, ok := reg.Tool("local_info"); !ok {
		t.Fatal("onStartup kit should be exposed")
	}
	if _, ok := reg.Tool("serial_read"); ok {
		t.Fatal("serial kit should be hidden without the event")
	}
	if got := len(reg.Tools()); got != 1 {
		t.Fatalf("expected only the startup tool, got %d", got)
	}

	reg.SetEvent(DeviceKindEvent("serial"), true)
	if _, ok := reg.Tool("serial_read"); !ok {
		t.Fatal("serial kit should be exposed once the event fires")
	}
	// A kit with two device events activates on either one.
	if _, ok := reg.Tool("wait_for_output"); !ok {
		t.Fatal("multi-event kit should activate on any event")
	}

	// Disabling beats activation.
	reg.SetEnabled("k.serial", false)
	if _, ok := reg.Tool("serial_read"); ok {
		t.Fatal("disabled kit must stay hidden while its event is active")
	}

	// Deactivating hides again.
	reg.SetEvent(DeviceKindEvent("serial"), false)
	if _, ok := reg.Tool("wait_for_output"); ok {
		t.Fatal("kit should hide when its last event goes away")
	}
	// Manifests stay listed regardless of state.
	if got := len(reg.Manifests()); got != 3 {
		t.Fatalf("manifests should stay listed, got %d", got)
	}
	if got := len(reg.Events()); got != 1 || !reg.Events()[EventStartup] {
		t.Fatalf("Events() should report satisfied events, got %v", reg.Events())
	}
}

func TestRegistryWithoutActivationIsAlwaysExposed(t *testing.T) {
	reg := NewRegistry()
	reg.Register(fakeKit{manifest: Manifest{ID: "k.plain"}, tools: []Tool{fakeTool("plain_tool")}})
	if _, ok := reg.Tool("plain_tool"); !ok {
		t.Fatal("a kit that declares no activation should stay exposed")
	}
}

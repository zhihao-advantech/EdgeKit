package server

import (
	"testing"

	"edgekit/internal/serial"
)

// TestKitsPayloadReflectsActivation checks the UI payload marks a
// device-dependent kit as enabled-but-not-active until a device connects.
func TestKitsPayloadReflectsActivation(t *testing.T) {
	s := newTestServer()
	find := func(id string) kitView {
		for _, v := range s.kitsPayload()["kits"].([]kitView) {
			if v.ID == id {
				return v
			}
		}
		t.Fatalf("kit %s missing from payload", id)
		return kitView{}
	}

	if v := find("edgekit.kit.serial"); !v.Enabled || v.Active {
		t.Fatalf("serial kit should be enabled but not active, got %+v", v)
	}
	if v := find("edgekit.kit.host"); !v.Enabled || !v.Active {
		t.Fatalf("host kit should be enabled and active, got %+v", v)
	}

	s.registerSession(&deviceSession{id: "serial-1", kind: "serial", label: "ttyUSB0", serial: serial.New(nil)})
	if v := find("edgekit.kit.serial"); !v.Active {
		t.Fatalf("serial kit should be active once a session exists, got %+v", v)
	}

	// Disabling wins over activation.
	s.kits.SetEnabled("edgekit.kit.serial", false)
	if v := find("edgekit.kit.serial"); v.Active {
		t.Fatalf("disabled kit must not be active, got %+v", v)
	}
}

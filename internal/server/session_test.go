package server

import (
	"testing"

	"edgekit/internal/kit"
	"edgekit/internal/kits"
	"edgekit/internal/serial"
	"edgekit/internal/sftpx"
	"edgekit/internal/sshclient"
)

func newTestServer() *Server {
	s := &Server{
		sessions: make(map[string]*deviceSession),
		clients:  make(map[*client]struct{}),
		kits:     kit.NewRegistry(),
	}
	for _, k := range kits.Builtin(kit.Deps{}) {
		s.kits.Register(k)
	}
	return s
}

func TestSessionRegistry(t *testing.T) {
	s := newTestServer()

	ser := &deviceSession{
		id: "serial-1", kind: "serial", label: "ttyUSB0",
		serial: serial.New(nil),
	}
	s.registerSession(ser)

	ssh := &deviceSession{
		id: "ssh-2", kind: "ssh", label: "user@192.0.2.10:22",
		ssh:  sshclient.New(nil),
		sftp: sftpx.New(nil),
	}
	s.registerSession(ssh)

	if got := len(s.sessionsSnapshot()["sessions"].([]map[string]any)); got != 2 {
		t.Fatalf("expected 2 sessions, got %d", got)
	}
	if s.session("").id != "serial-1" {
		t.Fatalf("first session should be focused, got %q", s.session("").id)
	}

	s.setFocus("ssh-2")
	if s.session("").id != "ssh-2" {
		t.Fatalf("focus should follow setFocus, got %q", s.session("").id)
	}

	s.closeSession("ssh-2")
	if _, ok := s.sessions["ssh-2"]; ok {
		t.Fatal("ssh-2 should be removed")
	}
	if s.session("").id != "serial-1" {
		t.Fatalf("focus should fall back to the remaining session, got %q", s.session("").id)
	}
	if s.session("missing") != nil {
		t.Fatal("unknown id should resolve to nothing")
	}
}

// TestDeviceEventsFollowSessions checks that device-dependent kits are exposed
// exactly while a session of their kind exists.
func TestDeviceEventsFollowSessions(t *testing.T) {
	s := newTestServer()
	if _, ok := s.kits.Tool("serial_read"); ok {
		t.Fatal("serial tools must be hidden before any session")
	}

	s.registerSession(&deviceSession{id: "serial-1", kind: "serial", label: "ttyUSB0", serial: serial.New(nil)})
	if _, ok := s.kits.Tool("serial_read"); !ok {
		t.Fatal("serial tools must appear once a serial session exists")
	}
	if _, ok := s.kits.Tool("ssh_exec"); ok {
		t.Fatal("ssh tools must stay hidden")
	}

	s.closeSession("serial-1")
	if _, ok := s.kits.Tool("serial_read"); ok {
		t.Fatal("serial tools must hide once the last serial session is gone")
	}
}

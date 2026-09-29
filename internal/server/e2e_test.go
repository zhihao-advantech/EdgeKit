package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"edgekit/internal/testrun"
	"edgekit/internal/workspace"

	"github.com/gorilla/websocket"
)

// e2eClient drives the public WebSocket protocol the UI and the MCP bridge use.
type e2eClient struct {
	t    *testing.T
	conn *websocket.Conn
	seq  int64

	// pendingSerial buffers serial events seen while waiting for another
	// session's, so a session's output is never lost to a concurrent wait.
	pendingSerial []e2eSerialEvent
}

type e2eSerialEvent struct {
	SessionID string
	Direction string
	Data      []byte
}

type e2eMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// startE2EServer starts a real server (own config dir) and connects a client.
func startE2EServer(t *testing.T) (*Server, *e2eClient) {
	t.Helper()
	// Keep the run hermetic: settings/runtime/workspace files land in temp dirs.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	s := New()
	url, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return s, &e2eClient{t: t, conn: conn}
}

func (c *e2eClient) send(typ string, payload any) {
	c.t.Helper()
	msg := map[string]any{"type": typ}
	if payload != nil {
		msg["payload"] = payload
	}
	data, _ := json.Marshal(msg)
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.t.Fatalf("send %s: %v", typ, err)
	}
}

func (c *e2eClient) recv() e2eMsg {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("recv: %v", err)
	}
	var m e2eMsg
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("decode envelope: %v", err)
	}
	return m
}

// until reads (and discards) messages until one of the wanted types arrives.
func (c *e2eClient) until(types ...string) e2eMsg {
	c.t.Helper()
	want := map[string]bool{}
	for _, typ := range types {
		want[typ] = true
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m := c.recv(); want[m.Type] {
			return m
		}
	}
	c.t.Fatalf("did not see %v within 15s", types)
	return e2eMsg{}
}

type e2eToolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func (c *e2eClient) toolCall(name string, args map[string]any) e2eToolResult {
	c.t.Helper()
	id := fmt.Sprintf("e2e-%d", atomic.AddInt64(&c.seq, 1))
	return c.toolCallID(id, name, args)
}

// toolCallID sends a tool.call and waits for the matching tool.result.
func (c *e2eClient) toolCallID(id, name string, args map[string]any) e2eToolResult {
	c.t.Helper()
	c.send("tool.call", map[string]any{"id": id, "name": name, "args": args})
	return c.waitToolResult(id)
}

// waitToolResult waits for the result of an already-sent tool.call. It must not
// be used on the same connection that still has to approve the call: the
// approval gate blocks that connection's worker.
func (c *e2eClient) waitToolResult(id string) e2eToolResult {
	c.t.Helper()
	for {
		m := c.until("tool.result", "error")
		if m.Type == "error" {
			var p struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(m.Payload, &p)
			c.t.Fatalf("tool.call %s: server error: %s", id, p.Message)
		}
		var r e2eToolResult
		if err := json.Unmarshal(m.Payload, &r); err != nil {
			c.t.Fatalf("decode tool.result: %v", err)
		}
		if r.ID == id {
			return r
		}
	}
}

// dialClient opens a second connection to the same server, standing in for the
// UI: it receives broadcasts and can answer approval prompts while the calling
// connection's worker is blocked in the gate.
func (c *e2eClient) dialClient(t *testing.T, s *Server) *e2eClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(s.URL(), nil)
	if err != nil {
		t.Fatalf("dial ui client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &e2eClient{t: t, conn: conn}
}

// approveNext waits for a mutating-tool approval on this connection and allows
// (or denies) it, returning the tool name.
func (c *e2eClient) approveNext(t *testing.T, allow bool) string {
	t.Helper()
	for {
		m := c.until("agent.event")
		var ev struct {
			Kind  string `json:"kind"`
			ID    string `json:"id"`
			Tool  string `json:"tool"`
			State string `json:"state"`
		}
		if json.Unmarshal(m.Payload, &ev) != nil || ev.Kind != "approval" {
			continue
		}
		c.send("agent.approve", map[string]any{"id": ev.ID, "allow": allow})
		return ev.Tool
	}
}

// tools lists the tool names currently advertised to consumers.
func (c *e2eClient) tools() map[string]bool {
	c.t.Helper()
	c.send("tools.list", nil)
	m := c.until("tools.defs")
	var p struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		c.t.Fatalf("decode tools.defs: %v", err)
	}
	out := map[string]bool{}
	for _, td := range p.Tools {
		out[td.Name] = true
	}
	return out
}

func mustContain(t *testing.T, got, want, label string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s: %q does not contain %q", label, got, want)
	}
}

// TestE2EProtocol exercises the always-available surface: the connect snapshot,
// the kits payload, activation-dependent tool surface, schema validation and
// kit enable/disable, all over the real WebSocket protocol.
func TestE2EProtocol(t *testing.T) {
	_, c := startE2EServer(t)

	// Connect snapshot: no sessions, all nine kits listed.
	sess := c.until("sessions")
	mustContain(t, string(sess.Payload), `"sessions":[]`, "sessions snapshot")

	kits := c.until("kits")
	mustContain(t, string(kits.Payload), "edgekit.kit.serial", "kits payload")
	mustContain(t, string(kits.Payload), `"active":false`, "kits payload should mark hidden kits")

	// With no device session, device kits contribute no tools.
	tools := c.tools()
	for _, want := range []string{"local_info", "workspace_write", "sessions_list", "code_diff"} {
		if !tools[want] {
			t.Fatalf("always-on tool %s missing from %v", want, tools)
		}
	}
	for _, hidden := range []string{"serial_read", "ssh_exec", "sftp_list", "wait_for_output"} {
		if tools[hidden] {
			t.Fatalf("device tool %s should be hidden without a session", hidden)
		}
	}

	// sessions_list reports the empty directory.
	if r := c.toolCall("sessions_list", nil); !r.OK || !strings.Contains(r.Output, "没有设备会话") {
		t.Fatalf("sessions_list: %+v", r)
	}

	// Schema validation runs before the tool and the approval gate.
	if r := c.toolCall("workspace_write", map[string]any{"path": "x.txt"}); r.OK || !strings.Contains(r.Error, "参数缺少必填项: content") {
		t.Fatalf("missing required arg should be rejected, got %+v", r)
	}
	if r := c.toolCall("workspace_write", map[string]any{"path": 7, "content": "x"}); r.OK || !strings.Contains(r.Error, "参数类型错误") {
		t.Fatalf("wrong arg type should be rejected, got %+v", r)
	}
	if r := c.toolCall("no_such_tool", nil); r.OK || !strings.Contains(r.Error, "未知工具") {
		t.Fatalf("unknown tool should be rejected, got %+v", r)
	}

	// Disabling a kit hides its tools immediately (and re-enabling restores).
	c.send("kits.setEnabled", map[string]any{"id": "edgekit.kit.workspace", "enabled": false})
	c.until("kits")
	if tools := c.tools(); tools["workspace_write"] {
		t.Fatal("disabled kit should not expose tools")
	}
	c.send("kits.setEnabled", map[string]any{"id": "edgekit.kit.workspace", "enabled": true})
	c.until("kits")
	if tools := c.tools(); !tools["workspace_write"] {
		t.Fatal("re-enabled kit should expose tools again")
	}

	// Terminal display preferences persist to the settings file (and survive a
	// subsequent client update).
	c.send("settings.set", map[string]any{"term-ts": false, "term-hex": true})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := loadSettings(); st["term-ts"] == false && st["term-hex"] == true {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := loadSettings()
	if st["term-ts"] != false || st["term-hex"] != true {
		t.Fatalf("term preferences not persisted: %#v", st)
	}
	if _, ok := st["kits.disabled"]; !ok {
		t.Fatalf("client settings update dropped host-managed keys: %#v", st)
	}
}

// startVirtualSerialPair wires two pseudo-terminals through socat: data written
// to the peer end appears on the edge end (and vice versa).
func startVirtualSerialPair(t *testing.T) (edge, peer string) {
	t.Helper()
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not installed; skipping virtual serial test")
	}
	dir := t.TempDir()
	edge = filepath.Join(dir, "edge.pty")
	peer = filepath.Join(dir, "peer.pty")
	cmd := exec.Command("socat", "-d", "-d",
		"pty,raw,echo=0,link="+edge,
		"pty,raw,echo=0,link="+peer)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("socat: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(edge); err == nil {
			if _, err := os.Stat(peer); err == nil {
				return edge, peer
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("socat did not create the pty links")
	return "", ""
}

func openPeer(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0o600)
	if err != nil {
		t.Fatalf("open peer %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// readPeer waits until substr shows up on the peer end.
func readPeer(t *testing.T, f *os.File, substr string, d time.Duration) {
	t.Helper()
	ch := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		var sb strings.Builder
		for {
			n, err := f.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), substr) {
					close(ch)
					return
				}
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("peer did not receive %q within %s", substr, d)
	}
}

// expectSilence fails if any byte arrives on the peer end within d.
func expectSilence(t *testing.T, f *os.File, d time.Duration) {
	t.Helper()
	ch := make(chan int, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := f.Read(buf)
		ch <- n
	}()
	select {
	case n := <-ch:
		if n > 0 {
			t.Fatalf("unexpected %d bytes on the device", n)
		}
	case <-time.After(d):
	}
}

// openSerial opens a port over the protocol and returns the new session id.
func (c *e2eClient) openSerial(t *testing.T, port string) string {
	t.Helper()
	c.send("serial.open", map[string]any{
		"port": port, "baud": 115200, "dataBits": 8, "parity": "none", "stopBits": 1,
	})
	m := c.until("serial.status", "error")
	if m.Type == "error" {
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(m.Payload, &p)
		t.Fatalf("serial.open %s: %s", port, p.Message)
	}
	var st struct {
		SessionID string `json:"sessionId"`
		Open      bool   `json:"open"`
	}
	if err := json.Unmarshal(m.Payload, &st); err != nil {
		t.Fatalf("decode serial.status: %v", err)
	}
	if !st.Open || st.SessionID == "" {
		t.Fatalf("serial session not open: %+v", st)
	}
	return st.SessionID
}

// waitSerialEvent waits for a serial.event of the given session containing
// substr (data is base64 on the wire). Events for other sessions are buffered.
func (c *e2eClient) waitSerialEvent(t *testing.T, sessionID, substr string) {
	t.Helper()
	for _, ev := range c.pendingSerial {
		if ev.SessionID == sessionID && strings.Contains(string(ev.Data), substr) {
			return
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m := c.recv()
		if m.Type != "serial.event" {
			continue
		}
		var ev struct {
			SessionID string `json:"sessionId"`
			Direction string `json:"direction"`
			Data      string `json:"data"`
		}
		if err := json.Unmarshal(m.Payload, &ev); err != nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(ev.Data)
		if err != nil {
			continue
		}
		if ev.SessionID == sessionID && strings.Contains(string(raw), substr) {
			return
		}
		c.pendingSerial = append(c.pendingSerial, e2eSerialEvent{ev.SessionID, ev.Direction, raw})
	}
	t.Fatalf("no serial.event for %s containing %q", sessionID, substr)
}

// TestE2ESerialMultiBoard drives two real pty-backed boards: device output
// reaches the timeline, wait_for_output blocks on it, sessions_list reports
// them, and a mutating write is routed by the `session` argument after the
// approval gate allows it.
func TestE2ESerialMultiBoard(t *testing.T) {
	edge1, peer1 := startVirtualSerialPair(t)
	edge2, peer2 := startVirtualSerialPair(t)
	p1 := openPeer(t, peer1)
	p2 := openPeer(t, peer2)

	s, c := startE2EServer(t)
	ui := c.dialClient(t, s) // approves prompts, like the app window

	id1 := c.openSerial(t, edge1) // focused session
	id2 := c.openSerial(t, edge2)
	if id1 == id2 {
		t.Fatalf("expected distinct session ids, both %q", id1)
	}

	// A serial session exposed the device-dependent kits.
	tools := c.tools()
	for _, want := range []string{"serial_read", "serial_write", "wait_for_output", "sessions_list"} {
		if !tools[want] {
			t.Fatalf("tool %s should be exposed once a serial session exists", want)
		}
	}
	if tools["ssh_exec"] {
		t.Fatal("ssh tool should stay hidden without an ssh session")
	}

	// Board output on both ends reaches each session's own timeline.
	if _, err := p1.Write([]byte("board-1 busybox login:\n")); err != nil {
		t.Fatalf("peer1 write: %v", err)
	}
	if _, err := p2.Write([]byte("board-2 kernel ready\n")); err != nil {
		t.Fatalf("peer2 write: %v", err)
	}
	c.waitSerialEvent(t, id1, "board-1 busybox login:")
	c.waitSerialEvent(t, id2, "board-2 kernel ready")

	// wait_for_output sees output that arrived before the call (lookback).
	r := c.toolCall("wait_for_output", map[string]any{"pattern": "busybox login:", "channel": "serial", "timeout_ms": 2000})
	if !r.OK {
		t.Fatalf("wait_for_output: %+v", r)
	}
	mustContain(t, r.Output, "board-1 busybox login:", "focused wait_for_output")

	// Multi-board addressing: target board 2 explicitly.
	r = c.toolCall("wait_for_output", map[string]any{"pattern": "kernel ready", "session": id2, "timeout_ms": 2000})
	if !r.OK {
		t.Fatalf("wait_for_output session=%s: %+v", id2, r)
	}
	mustContain(t, r.Output, "board-2 kernel ready", "addressed wait_for_output")

	// Board 2's record is not on board 1's session.
	r = c.toolCall("wait_for_output", map[string]any{"pattern": "kernel ready", "session": id1, "timeout_ms": 300})
	if !r.OK || !strings.Contains(r.Output, "等待超时") {
		t.Fatalf("board 1 should not see board 2's output, got %+v", r)
	}

	// Unknown session ids are rejected with the available directory.
	r = c.toolCall("wait_for_output", map[string]any{"pattern": "x", "session": "serial-999", "timeout_ms": 100})
	if r.OK || !strings.Contains(r.Error, "未知设备会话") {
		t.Fatalf("unknown session should fail, got %+v", r)
	}

	// sessions_list reports both boards.
	r = c.toolCall("sessions_list", nil)
	mustContain(t, r.Output, id1, "sessions_list")
	mustContain(t, r.Output, id2, "sessions_list")

	// Device-tool schema validation.
	if r := c.toolCall("serial_write", map[string]any{}); r.OK || !strings.Contains(r.Error, "参数缺少必填项: data") {
		t.Fatalf("serial_write without data should fail, got %+v", r)
	}

	// A mutating write raises an approval and is routed by `session`: the bytes
	// must land on board 2, not on the focused board 1. The approval is
	// answered on the second (UI) connection, exactly like the app window.
	c.send("tool.call", map[string]any{"id": "approval-1", "name": "serial_write",
		"args": map[string]any{"data": "ping board2", "session": id2}})
	if tool := ui.approveNext(t, true); tool != "serial_write" {
		t.Fatalf("expected a serial_write approval, got %q", tool)
	}

	res := c.waitToolResult("approval-1")
	if !res.OK {
		t.Fatalf("approved write should run, got %+v", res)
	}
	readPeer(t, p2, "ping board2", 5*time.Second)
}

// TestE2EApprovalDenied checks a denied mutating call never reaches the device.
func TestE2EApprovalDenied(t *testing.T) {
	edge, peer := startVirtualSerialPair(t)
	p := openPeer(t, peer)
	s, c := startE2EServer(t)
	ui := c.dialClient(t, s)
	c.openSerial(t, edge)

	c.send("tool.call", map[string]any{"id": "deny-1", "name": "serial_write", "args": map[string]any{"data": "should-not-send"}})
	if tool := ui.approveNext(t, false); tool != "serial_write" {
		t.Fatalf("expected a serial_write approval, got %q", tool)
	}

	res := c.waitToolResult("deny-1")
	if res.OK || !strings.Contains(res.Error, "拒绝") {
		t.Fatalf("denied write should be refused, got %+v", res)
	}
	expectSilence(t, p, 400*time.Millisecond)
}

// waitRun waits for the next test.state broadcast and decodes it.
func (c *e2eClient) waitRun() testrun.Run {
	c.t.Helper()
	m := c.until("test.state")
	var run testrun.Run
	if err := json.Unmarshal(m.Payload, &run); err != nil {
		c.t.Fatalf("decode test.state: %v", err)
	}
	return run
}

// TestE2ETestRunPipeline drives a whole test run against a real pty-backed
// board: connect → run the checks → generate a report → archive to the
// workspace, all over the protocol the UI uses.
func TestE2ETestRunPipeline(t *testing.T) {
	edge, peer := startVirtualSerialPair(t)
	p := openPeer(t, peer)
	_, c := startE2EServer(t)
	id := c.openSerial(t, edge)

	// The device prints a readiness banner.
	if _, err := p.Write([]byte("READY\n")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	c.waitSerialEvent(t, id, "READY")

	c.send("test.new", map[string]any{
		"name": "smoke", "sessionId": id,
		"definition": map[string]any{
			"name": "smoke",
			"checks": []any{
				map[string]any{"name": "ready", "expect": "READY"},
				map[string]any{"name": "absent", "expect": "PANIC", "expectNot": true},
			},
		},
	})
	run := c.waitRun()
	if run.ID == "" || run.Status != testrun.StatusIdle {
		t.Fatalf("initial run state: %+v", run)
	}

	c.send("test.phase", map[string]any{"runId": run.ID, "phase": "all"})

	deadline := time.Now().Add(15 * time.Second)
	for {
		run = c.waitRun()
		if run.Finished() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish: status=%s", run.Status)
		}
	}
	if run.Status != testrun.StatusPassed {
		t.Fatalf("run status = %s, want passed (%+v)", run.Status, run.Phases)
	}
	for _, ph := range run.Phases {
		if ph.Status != testrun.StatusPassed {
			t.Fatalf("phase %s = %s (%s)", ph.Key, ph.Status, ph.Summary)
		}
	}
	if p := run.Phase(testrun.PhaseRun); len(p.Checks) != 2 || p.Checks[0].Status != testrun.CheckPass {
		t.Fatalf("checks = %+v", p.Checks)
	}
	if run.ArchivedPath == "" {
		t.Fatal("run was not archived")
	}
	if _, err := workspace.Read(run.ArchivedPath + "/report.md"); err != nil {
		t.Fatalf("archived report unreadable: %v", err)
	}
	if _, err := workspace.Read(run.ArchivedPath + "/run.json"); err != nil {
		t.Fatalf("archived run.json unreadable: %v", err)
	}
}

// TestE2ETestDefinitionsAndHistory covers the workspace-backed definition flow:
// save → list → load, plus opening an archived run.
func TestE2ETestDefinitionsAndHistory(t *testing.T) {
	_, c := startE2EServer(t)

	c.send("test.save", map[string]any{"name": "smoke", "definition": map[string]any{
		"name":   "smoke",
		"checks": []any{map[string]any{"name": "ok", "command": "true", "exitZero": true}},
	}})
	var saved struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.until("test.saved").Payload, &saved); err != nil {
		t.Fatalf("decode test.saved: %v", err)
	}
	if saved.Path == "" || saved.Name != "smoke" {
		t.Fatalf("saved = %+v", saved)
	}

	var defs struct {
		Definitions []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(c.until("test.defs").Payload, &defs); err != nil {
		t.Fatalf("decode test.defs: %v", err)
	}
	found := false
	for _, d := range defs.Definitions {
		if d.Name == "smoke" && d.Path == saved.Path {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved definition not listed: %+v", defs.Definitions)
	}

	c.send("test.load", map[string]any{"path": saved.Path})
	var loaded struct {
		Path       string             `json:"path"`
		Definition testrun.Definition `json:"definition"`
	}
	if err := json.Unmarshal(c.until("test.definition").Payload, &loaded); err != nil {
		t.Fatalf("decode test.definition: %v", err)
	}
	if loaded.Definition.Name != "smoke" || len(loaded.Definition.Checks) != 1 {
		t.Fatalf("loaded = %+v", loaded.Definition)
	}

	// Opening an archived run replays it to the client.
	run := testrun.NewRun("run-arch-1", "arch", "serial-1", testrun.Definition{Name: "arch"})
	run.Status = testrun.StatusPassed
	if _, err := (testrun.Store{}).Archive(run); err != nil {
		t.Fatalf("archive: %v", err)
	}
	c.send("test.open", map[string]any{"runId": "run-arch-1"})
	var got testrun.Run
	if err := json.Unmarshal(c.until("test.state").Payload, &got); err != nil {
		t.Fatalf("decode test.state: %v", err)
	}
	if got.ID != "run-arch-1" || got.Name != "arch" {
		t.Fatalf("opened run = %+v", got)
	}
}

// TestE2ETestToolRun drives a test run through the tool surface the agent and
// MCP use (test_run needs approval; test_list is read-only).
func TestE2ETestToolRun(t *testing.T) {
	edge, peer := startVirtualSerialPair(t)
	p := openPeer(t, peer)
	s, c := startE2EServer(t)
	ui := c.dialClient(t, s)
	id := c.openSerial(t, edge)

	if _, err := p.Write([]byte("READY\n")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	c.waitSerialEvent(t, id, "READY")

	tools := c.tools()
	for _, want := range []string{"test_run", "test_list", "test_report"} {
		if !tools[want] {
			t.Fatalf("tool %s missing once a device is connected: %v", want, tools)
		}
	}

	// test_run is mutating: the approval gate must fire, then the run executes.
	c.send("tool.call", map[string]any{"id": "tr-1", "name": "test_run", "args": map[string]any{
		"session": id, "name": "smoke", "expect": "READY", "timeout_ms": 2000,
	}})
	if tool := ui.approveNext(t, true); tool != "test_run" {
		t.Fatalf("expected a test_run approval, got %q", tool)
	}
	res := c.waitToolResult("tr-1")
	if !res.OK || !strings.Contains(res.Output, "通过") {
		t.Fatalf("test_run via tool surface: %+v", res)
	}

	// The run was archived and shows up in test_list.
	res = c.toolCall("test_list", nil)
	if !res.OK || !strings.Contains(res.Output, "已保存的测试定义") {
		t.Fatalf("test_list: %+v", res)
	}
}

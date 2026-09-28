package server

import (
	"encoding/json"
	"strings"
	"testing"

	"edgekit/internal/kit"
	"edgekit/internal/policy"
	"edgekit/internal/serial"
	"edgekit/internal/timeline"
)

// toolResult is the reply payload of a tool.call.
type toolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

// callTool drives handleToolCall and returns its tool.result reply.
func callTool(t *testing.T, s *Server, id, name string, args map[string]any) toolResult {
	t.Helper()
	c := &client{send: make(chan []byte, 4), quit: make(chan struct{})}
	defer c.close()

	payload, err := json.Marshal(map[string]any{"id": id, "name": name, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	s.handleToolCall(c, message{Type: "tool.call", Payload: payload})

	select {
	case b := <-c.send:
		var env struct {
			Type    string     `json:"type"`
			Payload toolResult `json:"payload"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("bad reply: %v", err)
		}
		if env.Type != "tool.result" {
			t.Fatalf("reply type = %q, want tool.result", env.Type)
		}
		return env.Payload
	default:
		t.Fatal("no tool.result reply")
		return toolResult{}
	}
}

func newToolCallServer() *Server {
	s := newTestServer()
	s.gate = policy.New(nil)
	s.gate.SetAutoRun(true)
	// Simulate a connected board so the device-dependent kits are exposed,
	// exactly as the host does when a session appears.
	s.kits.SetEvent(kit.DeviceKindEvent("serial"), true)
	return s
}

func TestHandleToolCallValidatesArgs(t *testing.T) {
	s := newToolCallServer()

	// serial_write declares "data" as required; the call must be rejected
	// before the tool (and before the approval gate) runs.
	res := callTool(t, s, "1", "serial_write", map[string]any{})
	if res.OK || res.Error == "" {
		t.Fatalf("missing required arg should fail, got %+v", res)
	}
	if want := "参数缺少必填项: data"; !strings.Contains(res.Error, want) {
		t.Fatalf("error = %q, want %q", res.Error, want)
	}

	res = callTool(t, s, "2", "serial_write", map[string]any{"data": 123})
	if res.OK || !strings.Contains(res.Error, "参数类型错误: data 应为 string") {
		t.Fatalf("wrong arg type should fail, got %+v", res)
	}
}

func TestHandleToolCallUnknownTool(t *testing.T) {
	s := newToolCallServer()
	res := callTool(t, s, "1", "no_such_tool", nil)
	if res.OK || !strings.Contains(res.Error, "未知工具") {
		t.Fatalf("unknown tool should fail, got %+v", res)
	}
}

func TestHandleToolCallRecordsTimeline(t *testing.T) {
	s := newToolCallServer()
	tl := timeline.New(100)
	s.registerSession(&deviceSession{
		id: "serial-1", kind: "serial", label: "ttyUSB0",
		serial: serial.New(nil), tl: tl,
	})

	res := callTool(t, s, "1", "workspace_list", map[string]any{})
	if !res.OK {
		t.Fatalf("valid call should run, got %+v", res)
	}

	records := tl.Since(0, 0)
	if len(records) != 2 {
		t.Fatalf("expected action + result records, got %+v", records)
	}
	if records[0].Kind != "action" || !strings.Contains(string(records[0].Data), "workspace_list") {
		t.Fatalf("action record = %+v", records[0])
	}
	if records[1].Kind != "result" || !strings.Contains(string(records[1].Data), "workspace_list ok:") {
		t.Fatalf("result record = %+v", records[1])
	}
	for _, r := range records {
		if r.Channel != timeline.ChannelAgent {
			t.Fatalf("audit record on wrong channel: %+v", r)
		}
	}
}

// TestHandleToolCallAddressesSession drives the shared `session` argument
// end-to-end: the same tool acts on different boards by id.
func TestHandleToolCallAddressesSession(t *testing.T) {
	s := newToolCallServer()
	tl1 := timeline.New(10)
	tl1.Append(timeline.Record{Channel: timeline.ChannelSerial, Kind: "rx", Data: []byte("board-A ready")})
	tl2 := timeline.New(10)
	tl2.Append(timeline.Record{Channel: timeline.ChannelSerial, Kind: "rx", Data: []byte("board-B ready")})
	s.registerSession(&deviceSession{id: "serial-1", kind: "serial", label: "ttyUSB0", serial: serial.New(nil), tl: tl1})
	s.registerSession(&deviceSession{id: "serial-2", kind: "serial", label: "ttyUSB1", serial: serial.New(nil), tl: tl2})

	// Without a session argument the call acts on the focused session.
	res := callTool(t, s, "1", "wait_for_output", map[string]any{"pattern": "board-A ready", "timeout_ms": 200})
	if !res.OK || !strings.Contains(res.Output, "board-A") {
		t.Fatalf("focused call should read serial-1, got %+v", res)
	}

	// An explicit session targets another board.
	res = callTool(t, s, "2", "wait_for_output", map[string]any{"pattern": "board-B ready", "session": "serial-2", "timeout_ms": 200})
	if !res.OK || !strings.Contains(res.Output, "board-B") {
		t.Fatalf("session=serial-2 should read serial-2, got %+v", res)
	}
	// The audit record followed the target session too.
	var sawAudit bool
	for _, r := range tl2.Since(0, 0) {
		if r.Channel == timeline.ChannelAgent && strings.Contains(string(r.Data), "wait_for_output") {
			sawAudit = true
		}
	}
	if !sawAudit {
		t.Fatal("audit record should land on the targeted session's timeline")
	}

	// An unknown session is rejected with the available ids.
	res = callTool(t, s, "3", "wait_for_output", map[string]any{"pattern": "x", "session": "nope", "timeout_ms": 100})
	if res.OK || !strings.Contains(res.Error, "未知设备会话") {
		t.Fatalf("unknown session should fail, got %+v", res)
	}

	// The directory exposes both sessions for discovery.
	res = callTool(t, s, "4", "sessions_list", nil)
	if !res.OK || !strings.Contains(res.Output, "serial-1") || !strings.Contains(res.Output, "serial-2") {
		t.Fatalf("sessions_list should list both sessions, got %+v", res)
	}
}

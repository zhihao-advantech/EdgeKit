package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/kits"
)

type stubSerial struct {
	open    bool
	written []byte
	recent  []byte
}

func (s *stubSerial) IsOpen() bool         { return s.open }
func (s *stubSerial) Port() string         { return "/dev/stub" }
func (s *stubSerial) Write(p []byte) error { s.written = append(s.written, p...); return nil }
func (s *stubSerial) Recent() []byte       { return s.recent }
func (s *stubSerial) RunCapture(command string, quiet, timeout time.Duration) (string, error) {
	return "stub output for " + command, nil
}

type stubSSH struct {
	out string
}

func (s *stubSSH) IsConnected() bool { return true }
func (s *stubSSH) Target() string    { return "user@127.0.0.1:22" }
func (s *stubSSH) ExecCapture(command string, maxBytes int) (string, error) {
	return s.out, nil
}

type collector struct {
	mu     sync.Mutex
	events []Event
	done   chan struct{}
	once   sync.Once
}

func newCollector() *collector {
	return &collector{done: make(chan struct{})}
}

func (c *collector) on(ev Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	if ev.Kind == KindDone {
		c.once.Do(func() { close(c.done) })
	}
}

func (c *collector) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(8 * time.Second):
		t.Fatal("agent turn timed out")
	}
}

func (c *collector) find(kind string) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Event
	for _, e := range c.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// mockLLM returns a scripted sequence of responses.
func mockLLM(t *testing.T, responses []string, requests *[]chatRequest) *httptest.Server {
	t.Helper()
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		_ = json.Unmarshal(body, &req)
		if requests != nil {
			*requests = append(*requests, req)
		}
		w.Header().Set("Content-Type", "application/json")
		if i >= len(responses) {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"(end)"}}]}`)
			return
		}
		io.WriteString(w, responses[i])
		i++
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestManager builds an agent backed by the built-in kits.
func newTestManager(deps Deps, on func(Event)) *Manager {
	reg := kit.NewRegistry()
	for _, k := range kits.Builtin(deps) {
		reg.Register(k)
	}
	return New(reg, deps, on)
}

func TestAgentLLMToolCall(t *testing.T) {
	var reqs []chatRequest
	srv := mockLLM(t, []string{
		`{"choices":[{"message":{"role":"assistant","content":"先检查端口","tool_calls":[{"id":"call_1","type":"function","function":{"name":"net_check_port","arguments":"{\"host\":\"127.0.0.1\",\"port\":22}"}}]}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"检查完成"}}]}`,
	}, &reqs)

	c := newCollector()
	ag := newTestManager(Deps{}, c.on)
	ag.SetConfig(Config{BaseURL: srv.URL, APIKey: "test", Model: "mock"})
	ag.Send("检查 127.0.0.1 的 22 端口")
	c.wait(t)

	tools := c.find(KindTool)
	if len(tools) == 0 {
		t.Fatal("expected a tool event")
	}
	var ok bool
	for _, e := range tools {
		if e.Tool == "net_check_port" && e.State == "ok" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected net_check_port ok, got %+v", tools)
	}

	assistant := c.find(KindAssistant)
	if len(assistant) == 0 || !strings.Contains(assistant[len(assistant)-1].Text, "检查完成") {
		t.Fatalf("expected final assistant message, got %+v", assistant)
	}

	// second request must carry the tool result back to the model
	if len(reqs) < 2 {
		t.Fatalf("expected 2 model calls, got %d", len(reqs))
	}
	if len(reqs[1].Tools) == 0 {
		t.Fatal("tool schemas missing from request")
	}
	var sawToolMsg bool
	for _, m := range reqs[1].Messages {
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			sawToolMsg = true
		}
	}
	if !sawToolMsg {
		t.Fatal("tool result not sent back to the model")
	}
}

func TestAgentApproval(t *testing.T) {
	serial := &stubSerial{open: true}
	srv := mockLLM(t, []string{
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"serial_write","arguments":"{\"data\":\"reboot\"}"}}]}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"已重启"}}]}`,
	}, nil)

	c := newCollector()
	ag := newTestManager(Deps{Serial: serial}, c.on)
	ag.SetConfig(Config{BaseURL: srv.URL, APIKey: "test", Model: "mock", AutoRun: false})
	ag.Send("重启设备")

	// wait for the approval request, then allow it
	deadline := time.After(5 * time.Second)
	for {
		appr := c.find(KindApproval)
		if len(appr) > 0 {
			ag.Approve(appr[0].ID, true)
			break
		}
		select {
		case <-deadline:
			t.Fatal("no approval request emitted")
		case <-time.After(20 * time.Millisecond):
		}
	}
	c.wait(t)

	if string(serial.written) != "reboot\n" {
		t.Fatalf("expected serial write, got %q", serial.written)
	}
}

func TestAgentLocalHelp(t *testing.T) {
	c := newCollector()
	ag := newTestManager(Deps{}, c.on)
	ag.Send("你好")
	c.wait(t)

	assistant := c.find(KindAssistant)
	if len(assistant) == 0 || !strings.Contains(assistant[0].Text, "内置流程") {
		t.Fatalf("expected local help text, got %+v", assistant)
	}
}

func toolNames(c *collector) []string {
	var names []string
	for _, e := range c.find(KindTool) {
		if e.State == "running" {
			names = append(names, e.Tool)
		}
	}
	return names
}

func TestTargetBackwardCompat(t *testing.T) {
	ag := newTestManager(Deps{}, func(Event) {})
	cases := []struct {
		in   string
		want string
	}{
		{"", TargetRemote},
		{TargetRemote, TargetRemote},
		{TargetLocal, TargetLocal},
		{"edge", TargetRemote}, // legacy value
		{"host", TargetLocal},  // legacy value
	}
	for _, c := range cases {
		ag.SetConfig(Config{Target: c.in})
		if got := ag.Target(); got != c.want {
			t.Fatalf("Target(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalTargetRunsOnLocal(t *testing.T) {
	c := newCollector()
	ag := newTestManager(Deps{}, c.on)
	ag.SetConfig(Config{Target: TargetLocal})
	ag.Send("查看磁盘使用")
	c.wait(t)

	names := toolNames(c)
	if len(names) == 0 || names[0] != "local_exec" {
		t.Fatalf("local target should use local_exec, got %v", names)
	}
}

func TestRemoteTargetWithoutConnection(t *testing.T) {
	c := newCollector()
	ag := newTestManager(Deps{}, c.on)
	ag.SetConfig(Config{Target: TargetRemote})
	ag.Send("查看磁盘使用")
	c.wait(t)

	found := false
	for _, e := range c.find(KindAssistant) {
		if strings.Contains(e.Text, "远端未连接") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a remote-not-connected notice, got %+v", c.find(KindAssistant))
	}
	if names := toolNames(c); len(names) != 0 {
		t.Fatalf("no tool should run without a connection, got %v", names)
	}
}

func TestRemoteTargetFallsBackToSerial(t *testing.T) {
	serial := &stubSerial{open: true}
	c := newCollector()
	ag := newTestManager(Deps{Serial: serial}, c.on)
	ag.SetConfig(Config{Target: TargetRemote})
	ag.Send("查看系统版本")
	c.wait(t)

	names := toolNames(c)
	if len(names) == 0 || names[0] != "serial_exec" {
		t.Fatalf("remote target should fall back to serial_exec, got %v", names)
	}
}

func TestAgentLocalInspect(t *testing.T) {
	serial := &stubSerial{open: true, recent: []byte("user@board:~# ")}
	ssh := &stubSSH{out: "Linux board 5.15.0"}
	c := newCollector()
	ag := newTestManager(Deps{Serial: serial, SSH: ssh}, c.on)
	ag.Send("巡检设备状态")
	c.wait(t)

	var names []string
	for _, e := range c.find(KindTool) {
		if e.State == "running" {
			names = append(names, e.Tool)
		}
	}
	for _, want := range []string{"local_info", "ssh_exec", "serial_read"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("inspection did not call %s (called %v)", want, names)
		}
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"edgekit/internal/testrun"
	"edgekit/internal/timeline"
)

// testManager owns the running test runs of one server instance.
type testManager struct {
	s    *Server
	mu   sync.Mutex
	runs map[string]*testrun.Runner
	seq  int
}

func newTestManager(s *Server) *testManager {
	return &testManager{s: s, runs: make(map[string]*testrun.Runner)}
}

func (m *testManager) newID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return fmt.Sprintf("run-%s-%d", time.Now().Format("20060102-150405"), m.seq)
}

// onUpdate pushes the run state to every client.
func (m *testManager) onUpdate(run testrun.Run) {
	m.s.broadcast("test.state", run)
}

// handleNew creates an idle run bound to a device session.
func (m *testManager) handleNew(c *client, msg message) {
	var p struct {
		Name       string             `json:"name"`
		SessionID  string             `json:"sessionId"`
		Definition testrun.Definition `json:"definition"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}

	sid := p.SessionID
	if sid == "" {
		if ds := m.s.session(""); ds != nil {
			sid = ds.id
		}
	}
	if ds := m.s.session(sid); ds == nil {
		m.s.sendError(c, fmt.Errorf("没有可用的设备会话（请先连接串口或 SSH）"))
		return
	}

	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = p.Definition.Name
	}
	if name == "" {
		name = "测试"
	}
	if p.Definition.Name == "" {
		p.Definition.Name = name
	}

	id := m.newID()
	runner := testrun.New(id, name, sid, p.Definition, testDevice{s: m.s, id: sid}, testrun.Store{}, m.onUpdate)

	m.mu.Lock()
	m.runs[id] = runner
	m.mu.Unlock()

	m.onUpdate(runner.Snapshot())
}

// handlePhase runs one phase ("connect"/"run"/"generate"/"archive") or "all".
func (m *testManager) handlePhase(c *client, msg message) {
	var p struct {
		RunID string `json:"runId"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	runner := m.runner(p.RunID)
	if runner == nil {
		m.s.sendError(c, fmt.Errorf("测试不存在: %s", p.RunID))
		return
	}

	// Run off the client worker so the connection stays responsive; progress
	// arrives as test.state broadcasts.
	go func() {
		if p.Phase == "all" || p.Phase == "" {
			runner.RunAll(context.Background())
			return
		}
		if err := runner.RunPhase(context.Background(), p.Phase); err != nil {
			m.s.sendError(c, err)
		}
	}()
}

// handleAbort cancels the run's current phase.
func (m *testManager) handleAbort(c *client, msg message) {
	var p struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if runner := m.runner(p.RunID); runner != nil {
		runner.Abort()
	}
}

func (m *testManager) runner(id string) *testrun.Runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id]
}

/* ------------------------------------------------------------------ *
 * device adapter: drives one session through the host capabilities
 * ------------------------------------------------------------------ */

// testDevice implements testrun.Device over a device session.
type testDevice struct {
	s  *Server
	id string
}

func (d testDevice) Info() testrun.Info {
	ds := d.s.session(d.id)
	if ds == nil {
		return testrun.Info{ID: d.id, Connected: false, Label: "(会话不存在)"}
	}
	connected := false
	switch ds.kind {
	case "serial":
		connected = ds.serial != nil && ds.serial.IsOpen()
	case "ssh", "desktop":
		connected = ds.ssh != nil && ds.ssh.IsConnected()
	}
	return testrun.Info{ID: ds.id, Kind: ds.kind, Label: ds.label, Connected: connected}
}

func (d testDevice) Exec(ctx context.Context, command string) (string, bool, error) {
	ds := d.s.session(d.id)
	if ds == nil {
		return "", false, fmt.Errorf("会话不存在: %s", d.id)
	}
	switch {
	case ds.ssh != nil && ds.ssh.IsConnected():
		out, err := ds.ssh.ExecCapture(command, 64*1024)
		if err != nil {
			return out, false, err
		}
		// sshclient appends "(exit: ...)" to the output on a non-zero exit.
		return out, !strings.Contains(out, "(exit:"), nil
	case ds.serial != nil && ds.serial.IsOpen():
		out, err := ds.serial.RunCapture(command, 500*time.Millisecond, 8*time.Second)
		if err != nil {
			return out, false, err
		}
		return out, true, nil
	default:
		return "", false, fmt.Errorf("会话 %s 未连接", d.id)
	}
}

func (d testDevice) Wait(ctx context.Context, pattern string, timeout time.Duration) (string, error) {
	ds := d.s.session(d.id)
	if ds == nil || ds.tl == nil {
		return "", fmt.Errorf("会话不存在: %s", d.id)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	// Look back a little so output that arrived just before the call is seen,
	// then block for anything newer (same contract as wait_for_output).
	const lookback = 500
	last := ds.tl.LastSeq()
	after := last
	if after > lookback {
		after -= lookback
	} else {
		after = 0
	}
	f := timeline.Filter{
		Channels: []string{timeline.ChannelSerial, timeline.ChannelSSH},
		Pattern:  re,
		AfterSeq: after,
	}
	for _, rec := range ds.tl.Since(after, 0) {
		if f.Match(rec) {
			return string(rec.Data), nil
		}
	}
	f.AfterSeq = last
	rec, err := ds.tl.Wait(ctx, f, timeout)
	if err != nil {
		return "", err
	}
	return string(rec.Data), nil
}

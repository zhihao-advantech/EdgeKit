package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"edgekit/internal/testrun"
	"edgekit/internal/timeline"
	"edgekit/internal/workspace"
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

	runner := m.newRunner(name, sid, p.Definition)

	// Tell the caller which run it just created, then publish the initial state.
	m.s.sendTo(c, "test.created", map[string]any{"runId": runner.Snapshot().ID})
	m.onUpdate(runner.Snapshot())
}

// newRunner builds and registers a runner, wiring in the previous run of the
// same name so the report can call out regressions.
func (m *testManager) newRunner(name, sid string, def testrun.Definition) *testrun.Runner {
	id := m.newID()
	runner := testrun.New(id, name, sid, def, testDevice{s: m.s, id: sid}, testrun.Store{}, m.onUpdate)
	if prev, ok := (testrun.Store{}).Previous(name, id); ok {
		runner.SetPrevious(&prev)
	}
	m.mu.Lock()
	m.runs[id] = runner
	m.mu.Unlock()
	return runner
}

// handleBatch runs one case on several device sessions (all connected ones when
// none are named) and returns the created run ids.
func (m *testManager) handleBatch(c *client, msg message) {
	var p struct {
		Name       string             `json:"name"`
		Path       string             `json:"path"`
		Definition testrun.Definition `json:"definition"`
		Sessions   []string           `json:"sessions"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}

	def := p.Definition
	if strings.TrimSpace(p.Path) != "" {
		got, err := (testrun.Store{}).ReadDefinition(p.Path)
		if err != nil {
			m.s.sendError(c, err)
			return
		}
		def = got
	}
	if def.Name == "" {
		def.Name = p.Name
	}
	if def.Name == "" {
		def.Name = "测试"
	}

	targets := p.Sessions
	if len(targets) == 0 {
		for _, si := range m.s.sessionsDirectory() {
			if si.Connected && (si.Kind == "serial" || si.Kind == "ssh") {
				targets = append(targets, si.ID)
			}
		}
	}
	if len(targets) == 0 {
		m.s.sendError(c, fmt.Errorf("没有已连接的设备会话"))
		return
	}

	runners := make([]*testrun.Runner, 0, len(targets))
	ids := make([]string, 0, len(targets))
	for _, sid := range targets {
		if m.s.session(sid) == nil {
			continue
		}
		runner := m.newRunner(def.Name, sid, def)
		runners = append(runners, runner)
		ids = append(ids, runner.Snapshot().ID)
	}
	// Announce the batch before starting, so the first state updates are not
	// missed by the caller.
	m.s.sendTo(c, "test.batch", map[string]any{"runs": ids})
	for _, runner := range runners {
		go runner.RunAll(context.Background())
	}
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
 * workspace definitions and history
 * ------------------------------------------------------------------ */

// handleDefs lists the saved test cases and archived runs.
func (m *testManager) handleDefs(c *client) {
	store := testrun.Store{}
	m.s.sendTo(c, "test.defs", map[string]any{
		"definitions": store.ListDefinitions(),
		"runs":        store.ListRuns(),
		"scripts":     store.ListScripts(),
	})
}

// handleSave stores a test case in the workspace.
func (m *testManager) handleSave(c *client, msg message) {
	var p struct {
		Name       string             `json:"name"`
		Definition testrun.Definition `json:"definition"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if p.Definition.Name == "" {
		p.Definition.Name = p.Name
	}
	rel, err := (testrun.Store{}).WriteDefinition(p.Definition)
	if err != nil {
		m.s.sendError(c, err)
		return
	}
	m.s.sendTo(c, "test.saved", map[string]any{"path": rel, "name": p.Definition.Name})
	m.broadcastDefs()
}

// handleLoad reads a saved test case into the editor.
func (m *testManager) handleLoad(c *client, msg message) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	def, err := (testrun.Store{}).ReadDefinition(p.Path)
	if err != nil {
		m.s.sendError(c, err)
		return
	}
	m.s.sendTo(c, "test.definition", map[string]any{"path": p.Path, "definition": def})
}

// handleOpen loads an archived run and replays it to the caller.
func (m *testManager) handleOpen(c *client, msg message) {
	var p struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	run, err := (testrun.Store{}).ReadRun(p.RunID)
	if err != nil {
		m.s.sendError(c, err)
		return
	}
	m.s.sendTo(c, "test.state", run)
}

func (m *testManager) broadcastDefs() {
	store := testrun.Store{}
	m.s.broadcast("test.defs", map[string]any{
		"definitions": store.ListDefinitions(),
		"runs":        store.ListRuns(),
		"scripts":     store.ListScripts(),
	})
}

/* ------------------------------------------------------------------ *
 * test capability (consumed by the Test kit / agent / MCP)
 * ------------------------------------------------------------------ */

// Definitions lists the saved test cases.
func (m *testManager) Definitions() []testrun.DefinitionRef {
	return (testrun.Store{}).ListDefinitions()
}

// Runs lists the archived runs.
func (m *testManager) Runs() []testrun.RunSummary {
	return (testrun.Store{}).ListRuns()
}

// Report returns an archived run's generated report.
func (m *testManager) Report(runID string) (string, error) {
	run, err := (testrun.Store{}).ReadRun(runID)
	if err != nil {
		return "", err
	}
	return run.PhaseOutput(testrun.PhaseGenerate), nil
}

// Run executes a case to completion on a device session and returns the result.
func (m *testManager) Run(ctx context.Context, sessionID, path string, def *testrun.Definition) (testrun.Run, error) {
	var d testrun.Definition
	switch {
	case def != nil:
		d = *def
	case strings.TrimSpace(path) != "":
		got, err := (testrun.Store{}).ReadDefinition(path)
		if err != nil {
			return testrun.Run{}, err
		}
		d = got
	default:
		return testrun.Run{}, fmt.Errorf("请提供 path（工作区定义）或内联定义")
	}

	sid := sessionID
	if sid == "" {
		if ds := m.s.session(""); ds != nil {
			sid = ds.id
		}
	}
	if m.s.session(sid) == nil {
		return testrun.Run{}, fmt.Errorf("没有可用的设备会话（请先连接串口或 SSH）")
	}

	name := d.Name
	if name == "" {
		name = "测试"
	}
	runner := m.newRunner(name, sid, d)
	runner.RunAll(ctx)
	return runner.Snapshot(), nil
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
		// Silent: the test's command and its echo must not pollute the terminal.
		out, err := ds.serial.RunCaptureSilent(command, 500*time.Millisecond, 8*time.Second)
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

// RunScript executes a workspace script (tests/*.sh) on the device. With SSH it
// uploads the script over SFTP and runs it; on a serial console it feeds the
// script to the device shell through a heredoc and reads back the exit code.
func (d testDevice) RunScript(ctx context.Context, script string) (string, bool, error) {
	ds := d.s.session(d.id)
	if ds == nil {
		return "", false, fmt.Errorf("会话不存在: %s", d.id)
	}
	clean := path.Clean(strings.ReplaceAll(script, "\\", "/"))
	if !strings.HasPrefix(clean, "tests/") || !strings.HasSuffix(clean, ".sh") {
		return "", false, fmt.Errorf("脚本需位于工作区 tests/ 且以 .sh 结尾: %s", script)
	}
	data, err := workspace.Read(clean)
	if err != nil {
		return "", false, err
	}

	remote := fmt.Sprintf("/tmp/edgekit-test-%d.sh", time.Now().UnixNano())

	// SSH: upload, run, clean up.
	if ds.sftp != nil && ds.sftp.IsConnected() && ds.ssh != nil && ds.ssh.IsConnected() {
		if err := ds.sftp.Upload(remote, data); err != nil {
			return "", false, err
		}
		out, err := ds.ssh.ExecCapture("sh "+remote, 64*1024)
		_ = ds.sftp.Delete(remote)
		if err != nil {
			return out, false, err
		}
		return out, !strings.Contains(out, "(exit:"), nil
	}

	// Serial: write the script with a heredoc, then run it and echo the status.
	if ds.serial != nil && ds.serial.IsOpen() {
		const tag = "__EDGEKIT_EXIT__"
		cmd := "cat > " + remote + " <<'EK_SCRIPT_EOF'\n" + string(data) +
			"\nEK_SCRIPT_EOF\nsh " + remote + "; echo " + tag + "$?"
		out, err := ds.serial.RunCaptureSilent(cmd, 500*time.Millisecond, 60*time.Second)
		if err != nil {
			return out, false, err
		}
		code := 0
		if m := regexp.MustCompile(tag + `(\d+)`).FindStringSubmatch(out); m != nil {
			code, _ = strconv.Atoi(m[1])
		}
		return out, code == 0, nil
	}

	return "", false, fmt.Errorf("脚本测试需要已连接的 SSH（含 SFTP）或串口会话")
}

// handleDeleteDef removes a saved test case from the workspace.
func (m *testManager) handleDeleteDef(c *client, msg message) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if err := (testrun.Store{}).DeleteDefinition(p.Path); err != nil {
		m.s.sendError(c, err)
		return
	}
	m.s.sendTo(c, "test.deleted", map[string]any{"path": p.Path})
	m.broadcastDefs()
}

// handleDeleteRun removes an archived run (and stops it first if it is still
// running). It also drops the run from the live set so the UI list loses it.
func (m *testManager) handleDeleteRun(c *client, msg message) {
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
	// RemoveAll on a missing directory is a no-op, so a run that never archived
	// is deleted too.
	if err := (testrun.Store{}).DeleteRun(p.RunID); err != nil {
		m.s.sendError(c, err)
		return
	}
	m.mu.Lock()
	delete(m.runs, p.RunID)
	m.mu.Unlock()
	m.s.sendTo(c, "test.deleted", map[string]any{"runId": p.RunID})
	m.broadcastDefs()
}

// handleOpenDir opens a workspace folder in the desktop file manager so scripts
// can be dropped in.
func (m *testManager) handleOpenDir(c *client, msg message) {
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(msg.Payload, &p)
	rel := strings.TrimSpace(p.Path)
	if rel == "" {
		rel = "tests"
	}
	abs, err := workspace.Resolve(rel)
	if err != nil {
		m.s.sendError(c, err)
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		m.s.sendError(c, err)
		return
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		m.s.sendError(c, fmt.Errorf("未找到 xdg-open，无法打开文件夹：%s", abs))
		return
	}
	cmd := exec.Command("xdg-open", abs)
	if err := cmd.Start(); err != nil {
		m.s.sendError(c, err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
